package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/davidsugianto/herdr-session-hub/internal/herdr"
)

// HerdrSource adapts the herdr socket client to the loop's Source interface.
//
// herdr has no session-wide agent status subscription: pane.agent_status_changed
// needs a pane_id, and pane events carry only pane ids. So the adapter
//
//   - subscribes to pane lifecycle events plus one status subscription per agent
//     pane that exists when it subscribes,
//   - turns a status event into a full AgentUpsert by re-reading the agent
//     (events are signals, not state),
//   - maps pane ids back to terminal ids (the loop's key) using the last
//     snapshot, and
//   - reports Resync when a pane is created, moved or gains an agent, because
//     the status subscriptions then no longer cover every agent.
type HerdrSource struct {
	SocketPath string

	mu       sync.Mutex
	paneTerm map[string]string // pane id -> terminal id
}

// NewHerdrSource returns a Source for the herdr session socket at socketPath.
func NewHerdrSource(socketPath string) *HerdrSource {
	return &HerdrSource{SocketPath: socketPath, paneTerm: map[string]string{}}
}

// subscribeAttempts bounds retries when herdr rejects a subscription because a
// pane it names disappeared between the read and the subscribe.
const subscribeAttempts = 3

func agentFromInfo(a herdr.AgentInfo) Agent {
	title := a.TerminalTitleStripped
	if title == "" {
		title = a.Title
	}
	return Agent{
		ID:     a.TerminalID,
		PaneID: a.PaneID,
		Name:   a.Name,
		Kind:   a.Agent,
		Status: string(a.AgentStatus),
		Cwd:    a.Cwd,
		Title:  title,
	}
}

func (s *HerdrSource) setPanes(agents []herdr.AgentInfo) {
	m := make(map[string]string, len(agents))
	for _, a := range agents {
		m[a.PaneID] = a.TerminalID
	}
	s.mu.Lock()
	s.paneTerm = m
	s.mu.Unlock()
}

func (s *HerdrSource) rememberPane(pane, term string) {
	s.mu.Lock()
	s.paneTerm[pane] = term
	s.mu.Unlock()
}

// forgetPane removes pane from the map and returns its terminal id, if known.
func (s *HerdrSource) forgetPane(pane string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	term, ok := s.paneTerm[pane]
	delete(s.paneTerm, pane)
	return term, ok
}

func (s *HerdrSource) readSnapshot(ctx context.Context) (herdr.SessionSnapshot, error) {
	var res herdr.SessionSnapshotResult
	if err := herdr.CallOnce(ctx, s.SocketPath, herdr.MethodSessionSnapshot, herdr.EmptyParams{}, &res); err != nil {
		return herdr.SessionSnapshot{}, err
	}
	s.setPanes(res.Snapshot.Agents)
	return res.Snapshot, nil
}

// Snapshot reads every agent on its own connection and refreshes the
// pane -> terminal map.
func (s *HerdrSource) Snapshot(ctx context.Context) ([]Agent, error) {
	snap, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(snap.Agents))
	for _, a := range snap.Agents {
		agents = append(agents, agentFromInfo(a))
	}
	return agents, nil
}

// Subscribe lists the current agent panes, then subscribes to lifecycle events
// and to the status of each of those panes. It returns once herdr acknowledged.
//
// A pane created between the listing and the ack would have neither a
// lifecycle event nor a status subscription, so after the ack the panes are
// listed again; if the set moved the subscription is dropped and retried. When
// the set never settles, the stream starts with a Resync so the loop tries
// again instead of silently missing agents.
func (s *HerdrSource) Subscribe(ctx context.Context) (Stream, error) {
	var lastErr error
	for attempt := 0; attempt < subscribeAttempts; attempt++ {
		snap, err := s.readSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		specs := []herdr.SubscriptionSpec{
			{Type: herdr.SubPaneCreated},
			{Type: herdr.SubPaneClosed},
			{Type: herdr.SubPaneExited},
			{Type: herdr.SubPaneMoved},
			{Type: herdr.SubPaneAgentDetected},
		}
		for _, a := range snap.Agents {
			specs = append(specs, herdr.SubscriptionSpec{Type: herdr.SubPaneAgentStatusChanged, PaneID: a.PaneID})
		}
		sub, err := herdr.Subscribe(ctx, s.SocketPath, herdr.EventsSubscribeParams{Subscriptions: specs})
		var he *herdr.Error
		if errors.As(err, &he) {
			lastErr = err // typically a pane that vanished meanwhile: re-read and retry
			continue
		}
		if err != nil {
			return nil, err
		}
		// The subscription is live now; anything created later is signalled.
		after, err := s.readSnapshot(ctx)
		if err != nil {
			_ = sub.Close()
			return nil, err
		}
		if samePanes(snap.Agents, after.Agents) {
			return newHerdrStream(ctx, s, sub, false), nil
		}
		if attempt == subscribeAttempts-1 {
			return newHerdrStream(ctx, s, sub, true), nil
		}
		_ = sub.Close()
	}
	return nil, lastErr
}

// samePanes reports whether a and b cover the same set of agent panes.
func samePanes(a, b []herdr.AgentInfo) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, x := range a {
		seen[x.PaneID] = struct{}{}
	}
	for _, y := range b {
		if _, ok := seen[y.PaneID]; !ok {
			return false
		}
	}
	return true
}

// herdrStream translates raw herdr event lines into SourceEvents.
type herdrStream struct {
	src *HerdrSource
	sub *herdr.Subscription
	ctx context.Context

	out    chan SourceEvent
	cancel context.CancelFunc
	done   chan struct{}

	startResync bool // emit a Resync before any event (the pane set never settled)
}

func newHerdrStream(ctx context.Context, src *HerdrSource, sub *herdr.Subscription, startResync bool) *herdrStream {
	ctx, cancel := context.WithCancel(ctx)
	st := &herdrStream{src: src, sub: sub, ctx: ctx, out: make(chan SourceEvent, 16), cancel: cancel, done: make(chan struct{}), startResync: startResync}
	go st.run()
	return st
}

func (st *herdrStream) Events() <-chan SourceEvent { return st.out }
func (st *herdrStream) Err() error                 { return st.sub.Err() }

// Close ends the subscription and waits for the translator to stop. It is safe
// to call more than once and after the stream has ended.
func (st *herdrStream) Close() error {
	st.cancel()
	err := st.sub.Close()
	<-st.done
	return err
}

func (st *herdrStream) run() {
	defer close(st.done)
	defer close(st.out)
	if st.startResync {
		select {
		case st.out <- SourceEvent{Kind: Resync}:
		case <-st.ctx.Done():
			return
		}
	}
	for raw := range st.sub.Events() {
		for _, ev := range st.translate(raw) {
			select {
			case st.out <- ev:
			case <-st.ctx.Done():
				return
			}
		}
	}
}

type rawEvent struct {
	Event string `json:"event"`
	Data  struct {
		PaneID string `json:"pane_id"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// translate turns one wire line into zero or more SourceEvents. Lifecycle
// events are underscored (pane_closed) and subscription events dotted
// (pane.agent_status_changed); both spellings are accepted.
func (st *herdrStream) translate(raw json.RawMessage) []SourceEvent {
	var e rawEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	if e.Error != nil {
		if e.Error.Code == "events_lost" {
			return []SourceEvent{{Kind: EventsLost}}
		}
		return nil
	}
	switch strings.ReplaceAll(e.Event, ".", "_") {
	case "pane_agent_status_changed":
		return st.reread(e.Data.PaneID)
	case "pane_closed", "pane_exited":
		return st.remove(e.Data.PaneID)
	case "pane_created", "pane_moved", "pane_agent_detected":
		return []SourceEvent{{Kind: Resync}}
	}
	return nil
}

func (st *herdrStream) remove(pane string) []SourceEvent {
	if term, ok := st.src.forgetPane(pane); ok {
		return []SourceEvent{{Kind: AgentRemoved, Agent: Agent{ID: term}}}
	}
	return nil
}

// reread turns a status signal for pane into the agent's full current state.
func (st *herdrStream) reread(pane string) []SourceEvent {
	var res herdr.AgentGetResult
	err := herdr.CallOnce(st.ctx, st.src.SocketPath, herdr.MethodAgentGet, herdr.AgentGetParams{Target: pane}, &res)
	var he *herdr.Error
	switch {
	case errors.As(err, &he) && (he.Code == "agent_not_found" || he.Code == "not_found"):
		return st.remove(pane)
	case err != nil:
		return []SourceEvent{{Kind: Resync}}
	case res.Agent.TerminalID == "":
		return st.remove(pane)
	}
	st.src.rememberPane(pane, res.Agent.TerminalID)
	return []SourceEvent{{Kind: AgentUpsert, Agent: agentFromInfo(res.Agent)}}
}
