package sessions

import (
	"context"
	"errors"
	"sort"
	"time"
)

// This file implements the per-session sync loop. It is deliberately
// decoupled from the herdr socket client: everything it needs from herdr is
// expressed by the small consumer-side Source / Stream interfaces below, and
// an adapter over the real client is written elsewhere.
//
// The loop follows the cache-client order herdr documents:
//
//  1. open the event subscription and wait for its acknowledgement,
//  2. only then take a snapshot (on a second connection),
//  3. treat later events as updates to the snapshot-built cache,
//  4. on events_lost or connection loss, resubscribe, re-snapshot and emit
//     the diff against the cache instead of trusting it.

// Agent is the loop's view of one herdr agent.
type Agent struct {
	ID     string // terminal id; the stable key (pane ids change when panes move)
	PaneID string // current pane address, e.g. "w1:p2"
	Name   string // agent name, e.g. "claude"
	Kind   string
	Status string // raw herdr status string (idle, working, blocked, done, unknown); passed through unchanged
	Cwd    string
	Title  string
}

// SourceEventKind is the kind of a SourceEvent.
type SourceEventKind string

const (
	// AgentUpsert: Agent holds the full, current state of an agent (new or changed).
	// herdr's pane events carry only a pane id (and for status changes just the
	// status), so the adapter must resolve them: keep a pane->terminal map built
	// from the snapshot and re-read the agent (agent.get) on each pane event.
	AgentUpsert SourceEventKind = "agent_upsert"
	// AgentRemoved: the agent is gone; only Agent.ID (the terminal id) is
	// meaningful. herdr's pane_closed/pane_exited carry a pane id, which the
	// adapter must map back to its terminal id.
	AgentRemoved SourceEventKind = "agent_removed"
	// EventsLost: the server dropped events; the cache can no longer be trusted.
	// In protocol 22 this is not an event: herdr sends an error response with
	// code "events_lost" (using the subscribe request id) and closes the
	// connection, so the adapter maps that line to this kind.
	EventsLost SourceEventKind = "events_lost"
	// Resync: the session topology changed (a pane appeared, moved or gained an
	// agent), so the current subscription no longer covers every agent. The loop
	// resubscribes and re-snapshots immediately. Unlike EventsLost this is
	// routine, not a failure: it never counts towards the loss backoff.
	Resync SourceEventKind = "resync"
)

// SourceEvent is one already-decoded event from a subscription.
type SourceEvent struct {
	Kind  SourceEventKind
	Agent Agent // for AgentUpsert; for AgentRemoved only ID is set
}

// Stream is one live event subscription.
type Stream interface {
	// Events delivers events in order. The channel is closed when the
	// connection ends (cleanly or not).
	Events() <-chan SourceEvent
	// Err reports why the stream ended; meaningful once Events is closed.
	Err() error
	// Close releases the connection. It must be safe to call after the
	// stream has already ended, and more than once.
	Close() error
}

// Source is the herdr client as seen by the loop.
type Source interface {
	// Subscribe opens an event subscription for the whole session. It MUST
	// return only after the server acknowledged the subscription, so that a
	// Snapshot taken afterwards cannot miss changes. It should abort when
	// ctx is cancelled.
	Subscribe(ctx context.Context) (Stream, error)
	// Snapshot reads all agents on a connection separate from the
	// subscription. The loop only calls it after Subscribe has returned.
	Snapshot(ctx context.Context) ([]Agent, error)
}

// UpdateType is the kind of an Update.
type UpdateType string

// The constants carry an Update prefix because the bare names Added and
// Removed are already taken by the discovery code in this package.
const (
	// UpdateTransition: an agent's status changed; From/To are raw statuses.
	UpdateTransition UpdateType = "transition"
	// UpdateAdded: an agent appeared (first sync, a new agent, or found on resync).
	UpdateAdded UpdateType = "added"
	// UpdateRemoved: an agent disappeared; Agent is its last known state.
	UpdateRemoved UpdateType = "removed"
	// UpdateOnline: the session is connected and the cache is in sync.
	UpdateOnline UpdateType = "online"
	// UpdateOffline: the session connection failed. Sent once per outage.
	UpdateOffline UpdateType = "offline"
)

// Update is what the loop emits.
type Update struct {
	Session  string
	Type     UpdateType
	Agent    Agent  // zero for Online/Offline
	From, To string // statuses; set for Transition only
	At       time.Time
	Err      error // cause; set for Offline only
}

// Options tunes Run. The zero value is usable.
type Options struct {
	// Backoff returns the delay before retry number attempt (0-based, reset
	// after every successful sync). Default: 250ms doubling, capped at 5s.
	Backoff func(attempt int) time.Duration
	// Now supplies Update.At. Default: time.Now.
	Now func() time.Time
	// OnError, if set, is called with every failure (subscribe, snapshot,
	// stream end), including those that do not produce a new Offline update.
	OnError func(session string, err error)
}

const (
	defaultBackoffBase = 250 * time.Millisecond
	defaultBackoffMax  = 5 * time.Second
)

func defaultBackoff(attempt int) time.Duration {
	d := defaultBackoffBase
	for i := 0; i < attempt && d < defaultBackoffMax; i++ {
		d *= 2
	}
	if d > defaultBackoffMax {
		d = defaultBackoffMax
	}
	return d
}

var (
	errEventsLost   = errors.New("sessions: events lost")
	errResync       = errors.New("sessions: resync requested")
	errStreamClosed = errors.New("sessions: event stream closed")
	errNilStream    = errors.New("sessions: Subscribe returned a nil stream")
)

type connState int

const (
	stateUnknown connState = iota // nothing emitted yet
	stateOnline
	stateOffline
)

type runner struct {
	session string
	src     Source
	out     chan<- Update
	opts    Options

	cache  map[string]Agent
	state  connState
	synced bool // current attempt reached a successful sync (resets backoff)

	gotEvent   bool // the current cycle applied at least one event
	lostStreak int  // consecutive EventsLost cycles with no progress in between
}

// Run keeps the agent cache of one herdr session in sync and reports changes
// on out. It blocks until ctx is done and never returns an error: failures
// become Offline updates followed by retries with backoff. Run does not close
// out (several Runs may share it). Sends on out abort when ctx is cancelled.
//
// Each session should have its own Run so that a dead session cannot affect
// the others.
func Run(ctx context.Context, session string, src Source, out chan<- Update, opts Options) {
	if opts.Backoff == nil {
		opts.Backoff = defaultBackoff
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &runner{session: session, src: src, out: out, opts: opts, cache: map[string]Agent{}}

	attempt := 0
	for ctx.Err() == nil {
		r.synced = false
		r.gotEvent = false
		err := r.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if r.synced {
			attempt = 0
		}
		if errors.Is(err, errResync) {
			continue // routine topology change: resubscribe and re-snapshot right away
		}
		if errors.Is(err, errEventsLost) {
			// Not an outage: resubscribe and reconcile right away. Only
			// repeated losses with no progress in between back off, so an
			// overloaded server is not hammered with subscribe/snapshot.
			if r.gotEvent {
				r.lostStreak = 1
			} else {
				r.lostStreak++
			}
			if r.lostStreak > 1 && !sleep(ctx, r.opts.Backoff(r.lostStreak-2)) {
				return
			}
			continue
		}
		r.lostStreak = 0
		if r.opts.OnError != nil {
			r.opts.OnError(r.session, err)
		}
		if r.state != stateOffline {
			r.state = stateOffline
			if !r.emit(ctx, Update{Type: UpdateOffline, Err: err}) {
				return
			}
		}
		if !sleep(ctx, r.opts.Backoff(attempt)) {
			return
		}
		attempt++
	}
}

// connect runs one subscribe -> snapshot -> event loop cycle. It returns the
// reason the cycle ended; ctx cancellation returns ctx's error.
func (r *runner) connect(ctx context.Context) error {
	stream, err := r.src.Subscribe(ctx)
	if err != nil {
		return err
	}
	if stream == nil {
		return errNilStream
	}
	defer func() { _ = stream.Close() }()

	// The subscription is acknowledged; only now is a snapshot safe.
	agents, err := r.src.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !r.reconcile(ctx, agents) {
		return ctx.Err()
	}
	r.synced = true

	events := stream.Events()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				if err := stream.Err(); err != nil {
					return err
				}
				return errStreamClosed
			}
			switch ev.Kind {
			case EventsLost:
				return errEventsLost
			case Resync:
				return errResync
			case AgentUpsert:
				r.gotEvent = true
				if !r.apply(ctx, ev.Agent) {
					return ctx.Err()
				}
			case AgentRemoved:
				r.gotEvent = true
				old, known := r.cache[ev.Agent.ID]
				if !known {
					continue
				}
				delete(r.cache, ev.Agent.ID)
				if !r.emit(ctx, Update{Type: UpdateRemoved, Agent: old}) {
					return ctx.Err()
				}
			}
		}
	}
}

// reconcile replaces the cache with a fresh snapshot and emits Online (if the
// session was not already online) followed by the differences. Added and
// Transition updates follow snapshot order, then Removed updates sorted by id.
func (r *runner) reconcile(ctx context.Context, agents []Agent) bool {
	if r.state != stateOnline {
		r.state = stateOnline
		if !r.emit(ctx, Update{Type: UpdateOnline}) {
			return false
		}
	}
	seen := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		seen[a.ID] = struct{}{}
		if !r.apply(ctx, a) {
			return false
		}
	}
	var gone []string
	for id := range r.cache {
		if _, ok := seen[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		old := r.cache[id]
		delete(r.cache, id)
		if !r.emit(ctx, Update{Type: UpdateRemoved, Agent: old}) {
			return false
		}
	}
	return true
}

// apply stores a and emits Added or, if the status changed, Transition.
// Changes to other fields update the cache silently.
func (r *runner) apply(ctx context.Context, a Agent) bool {
	old, known := r.cache[a.ID]
	r.cache[a.ID] = a
	switch {
	case !known:
		return r.emit(ctx, Update{Type: UpdateAdded, Agent: a})
	case old.Status != a.Status:
		return r.emit(ctx, Update{Type: UpdateTransition, Agent: a, From: old.Status, To: a.Status})
	}
	return true
}

// emit sends u on out, stamped with the session and time. It returns false
// if ctx was cancelled first.
func (r *runner) emit(ctx context.Context, u Update) bool {
	u.Session = r.session
	u.At = r.opts.Now()
	select {
	case r.out <- u:
		return true
	case <-ctx.Done():
		return false
	}
}

// sleep waits d or until ctx is done; it returns false if ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
