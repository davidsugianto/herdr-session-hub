package model

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
)

const (
	defaultFlapGuard = 3 * time.Second
	subscriberBuffer = 256
)

// Options tunes an Engine. The zero value is usable.
type Options struct {
	Now       func() time.Time            // default time.Now
	FlapGuard time.Duration               // default 3s
	Label     func(session string) string // display label; default identity
}

// Engine folds sessions.Update values into the hub's agent/session state and
// publishes Changes to subscribers. All methods are goroutine-safe.
type Engine struct {
	now   func() time.Time
	guard time.Duration
	label func(string) string

	mu       sync.Mutex
	sessions map[string]*sessionState
	agents   map[string]*agentState
	subs     map[*subscriber]struct{}
}

type sessionState struct {
	online bool
}

type agentState struct {
	agent Agent
	// lastBlocked is the last time the agent was known to be blocked: set on
	// entering blocked and again on leaving it. Zero if it never was.
	lastBlocked time.Time
}

type subscriber struct {
	ch   chan Change
	quit chan struct{} // closed when the engine drops the subscriber
}

// NewEngine returns an empty engine.
func NewEngine(o Options) *Engine {
	e := &Engine{
		now:      o.Now,
		guard:    o.FlapGuard,
		label:    o.Label,
		sessions: map[string]*sessionState{},
		agents:   map[string]*agentState{},
		subs:     map[*subscriber]struct{}{},
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.guard <= 0 {
		e.guard = defaultFlapGuard
	}
	if e.label == nil {
		e.label = func(s string) string { return s }
	}
	return e
}

// mapStatus maps a raw herdr status to a hub State.
func mapStatus(raw string) State {
	switch raw {
	case "blocked":
		return StateBlocked
	case "done":
		return StateFinished
	case "working":
		return StateWorking
	case "idle":
		return StateWaiting
	}
	return StateUnknown
}

func agentID(session, terminal string) string { return session + "/" + terminal }

// Apply feeds one loop update into the engine.
func (e *Engine) Apply(u sessions.Update) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	switch u.Type {
	case sessions.UpdateOnline:
		e.setOnline(u.Session, true, now)
	case sessions.UpdateOffline:
		e.setOnline(u.Session, false, now)
	case sessions.UpdateAdded:
		e.session(u.Session)
		e.upsert(u.Session, u.Agent, now, false)
	case sessions.UpdateChanged:
		e.session(u.Session)
		e.upsert(u.Session, u.Agent, now, true)
	case sessions.UpdateTransition:
		e.session(u.Session)
		e.upsert(u.Session, u.Agent, now, false)
	case sessions.UpdateRemoved:
		id := agentID(u.Session, u.Agent.ID)
		st, ok := e.agents[id]
		if !ok {
			return
		}
		delete(e.agents, id)
		e.publish(Change{Type: ChangeRemoved, Session: u.Session, Agent: st.agent, At: now})
	}
}

func (e *Engine) session(name string) *sessionState {
	s, ok := e.sessions[name]
	if !ok {
		s = &sessionState{}
		e.sessions[name] = s
	}
	return s
}

func (e *Engine) setOnline(name string, online bool, now time.Time) {
	e.session(name).online = online
	for _, st := range e.agents {
		if st.agent.Session == name {
			st.agent.Online = online
		}
	}
	t := ChangeSessionOffline
	if online {
		t = ChangeSessionOnline
	}
	e.publish(Change{Type: t, Session: name, At: now})
}

// upsert applies the loop's view of an agent. A new agent publishes
// ChangeAdded. For a known agent a mapped-state change publishes ChangeState
// (even if fields changed too); otherwise ChangeUpdated is published when
// stored fields changed, or always if forceUpdate is set (UpdateChanged).
func (e *Engine) upsert(session string, la sessions.Agent, now time.Time, forceUpdate bool) {
	id := agentID(session, la.ID)
	state := mapStatus(la.Status)
	name := la.Name
	if name == "" {
		name = la.Kind
	}
	st, known := e.agents[id]
	if !known {
		a := Agent{
			ID:           id,
			Session:      session,
			SessionLabel: e.label(session),
			TerminalID:   la.ID,
			PaneID:       la.PaneID,
			Name:         name,
			Kind:         la.Kind,
			Cwd:          la.Cwd,
			Title:        la.Title,
			State:        state,
			RawStatus:    la.Status,
			Since:        now,
			Online:       e.session(session).online,
		}
		st = &agentState{agent: a}
		if state == StateBlocked {
			st.lastBlocked = now
		}
		e.agents[id] = st
		e.publish(Change{Type: ChangeAdded, Session: session, Agent: a, At: now})
		return
	}

	a := &st.agent
	fieldsChanged := a.PaneID != la.PaneID || a.Name != name || a.Kind != la.Kind || a.Cwd != la.Cwd || a.Title != la.Title
	a.PaneID, a.Name, a.Kind, a.Cwd, a.Title = la.PaneID, name, la.Kind, la.Cwd, la.Title
	rawChanged := a.RawStatus != la.Status
	a.RawStatus = la.Status

	// A Waiting agent that was marked seen keeps raw status "done"; a repeat
	// of that same raw status (no status change) must not revive Finished.
	if !rawChanged && a.State == StateWaiting && state == StateFinished {
		state = StateWaiting
	}

	if state != a.State {
		e.changeState(st, state, now)
		return
	}
	if fieldsChanged || forceUpdate {
		e.publish(Change{Type: ChangeUpdated, Session: session, Agent: *a, At: now})
	}
}

// changeState moves st to the new state and publishes the ChangeState.
func (e *Engine) changeState(st *agentState, to State, now time.Time) {
	a := &st.agent
	from := a.State
	a.State = to
	a.Since = now
	c := Change{Type: ChangeState, Session: a.Session, From: from, To: to, At: now}
	if to == StateBlocked {
		bounced := !st.lastBlocked.IsZero() && now.Sub(st.lastBlocked) < e.guard
		c.NewEpisode = !bounced
		st.lastBlocked = now
	}
	if from == StateBlocked {
		st.lastBlocked = now
	}
	c.Agent = *a
	e.publish(c)
}

// publish delivers c to every subscriber without blocking; a subscriber whose
// buffer is full is dropped (its channel is closed). e.mu must be held.
func (e *Engine) publish(c Change) {
	for s := range e.subs {
		select {
		case s.ch <- c:
		default:
			delete(e.subs, s)
			close(s.ch)
			close(s.quit)
		}
	}
}

// Sessions returns every known session sorted by name.
func (e *Engine) Sessions() []SessionInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{}
	for _, st := range e.agents {
		counts[st.agent.Session]++
	}
	out := make([]SessionInfo, 0, len(e.sessions))
	for name, s := range e.sessions {
		out = append(out, SessionInfo{Name: name, Label: e.label(name), Online: s.online, Agents: counts[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var stateRank = map[State]int{
	StateBlocked:  0,
	StateFinished: 1,
	StateWorking:  2,
	StateWaiting:  3,
	StateUnknown:  4,
}

// Agents returns the agents of one session ("" = all sessions), ordered
// blocked, finished, working, waiting, unknown, then by ID. Nothing is hidden.
func (e *Engine) Agents(session string) []Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Agent, 0, len(e.agents))
	for _, st := range e.agents {
		if session == "" || st.agent.Session == session {
			out = append(out, st.agent)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := stateRank[out[i].State], stateRank[out[j].State]
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Agent returns one agent by hub ID ("<session>/<terminal_id>").
func (e *Engine) Agent(id string) (Agent, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.agents[id]
	if !ok {
		return Agent{}, false
	}
	return st.agent, true
}

// MarkSeen moves a Finished agent to Waiting (publishing a ChangeState) and
// reports whether the agent exists. It is a no-op for other states.
func (e *Engine) MarkSeen(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.agents[id]
	if !ok {
		return false
	}
	if st.agent.State == StateFinished {
		e.changeState(st, StateWaiting, e.now())
	}
	return true
}

// Subscribe returns a channel of Changes. It is closed when ctx is done or
// when the subscriber falls behind and is dropped.
func (e *Engine) Subscribe(ctx context.Context) <-chan Change {
	s := &subscriber{ch: make(chan Change, subscriberBuffer), quit: make(chan struct{})}
	e.mu.Lock()
	if ctx.Err() != nil {
		close(s.ch)
		e.mu.Unlock()
		return s.ch
	}
	e.subs[s] = struct{}{}
	e.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.quit:
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, ok := e.subs[s]; ok {
			delete(e.subs, s)
			close(s.ch)
		}
	}()
	return s.ch
}
