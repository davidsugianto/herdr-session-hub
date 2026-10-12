package sessions

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

// ---- fakes ----------------------------------------------------------------

type loopStream struct {
	ch   chan SourceEvent
	once sync.Once
	mu   sync.Mutex
	err  error
	shut bool
}

func newLoopStream() *loopStream { return &loopStream{ch: make(chan SourceEvent, 16)} }

func (s *loopStream) Events() <-chan SourceEvent { return s.ch }
func (s *loopStream) Err() error                 { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *loopStream) Close() error {
	s.mu.Lock()
	s.shut = true
	s.mu.Unlock()
	s.once.Do(func() { close(s.ch) })
	return nil
}
func (s *loopStream) closed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.shut }

// end simulates the server dropping the connection.
func (s *loopStream) end(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.once.Do(func() { close(s.ch) })
}

func (s *loopStream) push(ev SourceEvent) { s.ch <- ev }

type subStep struct {
	stream *loopStream
	err    error
}
type snapStep struct {
	agents []Agent
	err    error
}

// loopSource replays scripted steps in order. When a script is exhausted the
// call blocks until ctx is done, so a finished test never spins.
type loopSource struct {
	mu    sync.Mutex
	calls []string
	subs  []subStep
	snaps []snapStep
	gate  chan struct{} // if set, the first Subscribe waits for it before acking
}

func (f *loopSource) record(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }

func (f *loopSource) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *loopSource) Subscribe(ctx context.Context) (Stream, error) {
	f.record("subscribe")
	f.mu.Lock()
	gate := f.gate
	f.gate = nil
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	if len(f.subs) == 0 {
		f.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	st := f.subs[0]
	f.subs = f.subs[1:]
	f.mu.Unlock()
	if st.err != nil {
		return nil, st.err
	}
	f.record("subscribed")
	return st.stream, nil
}

func (f *loopSource) Snapshot(ctx context.Context) ([]Agent, error) {
	f.record("snapshot")
	f.mu.Lock()
	if len(f.snaps) == 0 {
		f.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	st := f.snaps[0]
	f.snaps = f.snaps[1:]
	f.mu.Unlock()
	return st.agents, st.err
}

// ---- helpers --------------------------------------------------------------

var loopT0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func loopOpts() Options {
	return Options{
		Backoff: func(int) time.Duration { return 0 },
		Now:     func() time.Time { return loopT0 },
	}
}

func ag(id, status string) Agent {
	return Agent{ID: id, PaneID: "w1:p" + id, Name: "claude", Status: status}
}

// ev is a compact, comparable view of an Update.
type ev struct {
	Type     UpdateType
	ID       string
	From, To string
}

func up(typ UpdateType, id, from, to string) ev { return ev{typ, id, from, to} }

func loopStart(t *testing.T, name string, src Source, out chan Update, opts Options) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() {
		defer close(d)
		Run(ctx, name, src, out, opts)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return cancel, d
}

func loopNext(t *testing.T, out <-chan Update) Update {
	t.Helper()
	select {
	case u := <-out:
		return u
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for update")
		return Update{}
	}
}

func loopExpect(t *testing.T, out <-chan Update, session string, want ...ev) {
	t.Helper()
	for i, w := range want {
		u := loopNext(t, out)
		if u.Session != session {
			t.Fatalf("update %d: session = %q, want %q", i, u.Session, session)
		}
		if !u.At.Equal(loopT0) {
			t.Fatalf("update %d: At = %v, want %v", i, u.At, loopT0)
		}
		got := ev{u.Type, u.Agent.ID, u.From, u.To}
		if got != w {
			t.Fatalf("update %d = %+v, want %+v", i, got, w)
		}
	}
}

func loopNone(t *testing.T, out <-chan Update) {
	t.Helper()
	select {
	case u := <-out:
		t.Fatalf("unexpected update %+v", u)
	case <-time.After(30 * time.Millisecond):
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---- tests ----------------------------------------------------------------

func TestLoopSubscribeBeforeSnapshot(t *testing.T) {
	gate := make(chan struct{})
	src := &loopSource{
		gate:  gate,
		subs:  []subStep{{stream: newLoopStream()}},
		snaps: []snapStep{{agents: []Agent{ag("a1", "idle")}}},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())

	// While Subscribe has not been acknowledged, Snapshot must not run.
	time.Sleep(50 * time.Millisecond)
	if got := src.callList(); !reflect.DeepEqual(got, []string{"subscribe"}) {
		t.Fatalf("before ack calls = %v, want [subscribe]", got)
	}
	close(gate)
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""))
	want := []string{"subscribe", "subscribed", "snapshot"}
	if got := src.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestLoopInitialSync(t *testing.T) {
	src := &loopSource{
		subs:  []subStep{{stream: newLoopStream()}},
		snaps: []snapStep{{agents: []Agent{ag("a1", "working"), ag("a2", "idle")}}},
	}
	out := make(chan Update, 16)
	loopStart(t, "labs", src, out, loopOpts())
	loopExpect(t, out, "labs",
		up(UpdateOnline, "", "", ""),
		up(UpdateAdded, "a1", "", ""),
		up(UpdateAdded, "a2", "", ""),
	)
	loopNone(t, out)
}

func TestLoopTransitionAndNoDuplicate(t *testing.T) {
	st := newLoopStream()
	src := &loopSource{
		subs:  []subStep{{stream: st}},
		snaps: []snapStep{{agents: []Agent{ag("a1", "working")}}},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""))

	// Same status again (even with other fields changed): no update.
	same := ag("a1", "working")
	same.Title = "renamed"
	st.push(SourceEvent{Kind: AgentUpsert, Agent: same})
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "working")})
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "blocked")})
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "blocked")})
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "unknown")}) // passed through unchanged
	loopExpect(t, out, "s",
		up(UpdateTransition, "a1", "working", "blocked"),
		up(UpdateTransition, "a1", "blocked", "unknown"),
	)
	loopNone(t, out)
}

func TestLoopUpsertAndRemovedEvents(t *testing.T) {
	st := newLoopStream()
	src := &loopSource{
		subs:  []subStep{{stream: st}},
		snaps: []snapStep{{agents: nil}},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""))

	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "idle")})
	st.push(SourceEvent{Kind: AgentRemoved, Agent: Agent{ID: "ghost"}}) // unknown: ignored
	st.push(SourceEvent{Kind: AgentRemoved, Agent: Agent{ID: "a1"}})
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "idle")}) // re-added
	loopExpect(t, out, "s",
		up(UpdateAdded, "a1", "", ""),
		up(UpdateRemoved, "a1", "", ""),
		up(UpdateAdded, "a1", "", ""),
	)
	loopNone(t, out)
}

func TestLoopRemovedCarriesLastKnownAgent(t *testing.T) {
	st := newLoopStream()
	a := ag("a1", "working")
	a.Cwd = "/code/x"
	src := &loopSource{subs: []subStep{{stream: st}}, snaps: []snapStep{{agents: []Agent{a}}}}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""))
	st.push(SourceEvent{Kind: AgentRemoved, Agent: Agent{ID: "a1"}})
	if u := loopNext(t, out); u.Type != UpdateRemoved || u.Agent != a {
		t.Fatalf("got %+v, want Removed with last known agent %+v", u, a)
	}
}

func TestLoopEventsLostResyncs(t *testing.T) {
	st1, st2 := newLoopStream(), newLoopStream()
	src := &loopSource{
		subs: []subStep{{stream: st1}, {stream: st2}},
		snaps: []snapStep{
			{agents: []Agent{ag("a1", "working"), ag("a2", "idle"), ag("a3", "working")}},
			// While events were lost: a1 changed status, a2 vanished, a4 appeared.
			{agents: []Agent{ag("a1", "blocked"), ag("a3", "working"), ag("a4", "idle")}},
		},
	}
	out := make(chan Update, 32)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s",
		up(UpdateOnline, "", "", ""),
		up(UpdateAdded, "a1", "", ""),
		up(UpdateAdded, "a2", "", ""),
		up(UpdateAdded, "a3", "", ""),
	)

	st1.push(SourceEvent{Kind: EventsLost})
	loopExpect(t, out, "s",
		up(UpdateTransition, "a1", "working", "blocked"),
		up(UpdateAdded, "a4", "", ""),
		up(UpdateRemoved, "a2", "", ""),
	)
	// Not an outage, so no Offline and no second Online.
	loopNone(t, out)

	want := []string{"subscribe", "subscribed", "snapshot", "subscribe", "subscribed", "snapshot"}
	if got := src.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !st1.closed() {
		t.Fatal("old stream was not closed after EventsLost")
	}

	// The new stream keeps working against the reconciled cache.
	st2.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a4", "working")})
	st2.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a2", "idle")}) // gone earlier: Added again
	loopExpect(t, out, "s",
		up(UpdateTransition, "a4", "idle", "working"),
		up(UpdateAdded, "a2", "", ""),
	)
}

func TestLoopStreamCloseOfflineThenOnline(t *testing.T) {
	st1, st2 := newLoopStream(), newLoopStream()
	src := &loopSource{
		subs: []subStep{{stream: st1}, {stream: st2}},
		snaps: []snapStep{
			{agents: []Agent{ag("a1", "working"), ag("a2", "idle")}},
			{agents: []Agent{ag("a1", "idle")}}, // a1 finished, a2 gone during the outage
		},
	}
	out := make(chan Update, 32)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s",
		up(UpdateOnline, "", "", ""),
		up(UpdateAdded, "a1", "", ""),
		up(UpdateAdded, "a2", "", ""),
	)

	cause := errors.New("connection reset")
	st1.end(cause)
	u := loopNext(t, out)
	if u.Type != UpdateOffline || u.Session != "s" || !errors.Is(u.Err, cause) {
		t.Fatalf("got %+v, want Offline with cause %v", u, cause)
	}
	loopExpect(t, out, "s",
		up(UpdateOnline, "", "", ""),
		up(UpdateTransition, "a1", "working", "idle"),
		up(UpdateRemoved, "a2", "", ""),
	)
	loopNone(t, out)
}

func TestLoopStreamCloseWithoutErrorStillOffline(t *testing.T) {
	st1 := newLoopStream()
	src := &loopSource{
		subs:  []subStep{{stream: st1}, {stream: newLoopStream()}},
		snaps: []snapStep{{agents: nil}, {agents: nil}},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""))
	st1.end(nil)
	loopExpect(t, out, "s", up(UpdateOffline, "", "", ""), up(UpdateOnline, "", "", ""))
}

func TestLoopOfflineEmittedOnce(t *testing.T) {
	boom := errors.New("dial: no such file")
	src := &loopSource{
		subs: []subStep{
			{err: boom}, {err: boom}, {err: boom}, {err: boom},
			{stream: newLoopStream()},
		},
		snaps: []snapStep{{agents: []Agent{ag("a1", "idle")}}},
	}
	var mu sync.Mutex
	var attempts []int
	var errs int
	opts := loopOpts()
	opts.Backoff = func(n int) time.Duration {
		mu.Lock()
		attempts = append(attempts, n)
		mu.Unlock()
		return 0
	}
	opts.OnError = func(string, error) { mu.Lock(); errs++; mu.Unlock() }
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, opts)

	// A session that never came up still reports Offline, exactly once.
	loopExpect(t, out, "s",
		up(UpdateOffline, "", "", ""),
		up(UpdateOnline, "", "", ""),
		up(UpdateAdded, "a1", "", ""),
	)
	loopNone(t, out)

	mu.Lock()
	defer mu.Unlock()
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("backoff attempts = %v, want %v", attempts, want)
	}
	if errs != 4 {
		t.Fatalf("OnError calls = %d, want 4 (every retry is reported, only Offline is deduplicated)", errs)
	}
}

func TestLoopSnapshotError(t *testing.T) {
	st1, st2 := newLoopStream(), newLoopStream()
	boom := errors.New("snapshot failed")
	src := &loopSource{
		subs: []subStep{{stream: st1}, {stream: st2}},
		snaps: []snapStep{
			{err: boom},
			{agents: []Agent{ag("a1", "idle")}},
		},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())

	u := loopNext(t, out)
	if u.Type != UpdateOffline || !errors.Is(u.Err, boom) {
		t.Fatalf("got %+v, want Offline(snapshot failed)", u)
	}
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""))
	if !st1.closed() {
		t.Fatal("stream from the failed attempt was not closed")
	}
	want := []string{"subscribe", "subscribed", "snapshot", "subscribe", "subscribed", "snapshot"}
	if got := src.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestLoopSnapshotErrorWhileOnlineDoesNotLoseCache(t *testing.T) {
	st1, st2 := newLoopStream(), newLoopStream()
	src := &loopSource{
		subs: []subStep{{stream: st1}, {stream: st2}, {stream: newLoopStream()}},
		snaps: []snapStep{
			{agents: []Agent{ag("a1", "working")}},
			{err: errors.New("nope")},
			{agents: []Agent{ag("a1", "working")}}, // unchanged: nothing but Online
		},
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, loopOpts())
	loopExpect(t, out, "s", up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""))
	st1.end(errors.New("eof"))
	loopExpect(t, out, "s", up(UpdateOffline, "", "", ""), up(UpdateOnline, "", "", ""))
	loopNone(t, out) // no spurious Added: the cache survived the outage
}

func TestLoopBackoffResetsAfterSuccessfulSync(t *testing.T) {
	boom := errors.New("down")
	st1 := newLoopStream()
	src := &loopSource{
		subs:  []subStep{{err: boom}, {err: boom}, {stream: st1}, {err: boom}, {stream: newLoopStream()}},
		snaps: []snapStep{{agents: nil}, {agents: nil}},
	}
	var mu sync.Mutex
	var attempts []int
	opts := loopOpts()
	opts.Backoff = func(n int) time.Duration {
		mu.Lock()
		attempts = append(attempts, n)
		mu.Unlock()
		return 0
	}
	out := make(chan Update, 16)
	loopStart(t, "s", src, out, opts)
	loopExpect(t, out, "s", up(UpdateOffline, "", "", ""), up(UpdateOnline, "", "", ""))
	st1.end(errors.New("eof"))
	loopExpect(t, out, "s", up(UpdateOffline, "", "", ""), up(UpdateOnline, "", "", ""))
	// Failures before the first sync count 0,1; after the good sync the
	// counter restarts, so the stream end is attempt 0 and the following
	// subscribe failure attempt 1.
	mu.Lock()
	defer mu.Unlock()
	if want := []int{0, 1, 0, 1}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("backoff attempts = %v, want %v", attempts, want)
	}
}

func TestLoopDefaultBackoff(t *testing.T) {
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, w := range want {
		if got := defaultBackoff(i); got != w {
			t.Errorf("defaultBackoff(%d) = %v, want %v", i, got, w)
		}
	}
	for _, n := range []int{64, 1000, 1 << 30} {
		if got := defaultBackoff(n); got != 5*time.Second {
			t.Errorf("defaultBackoff(%d) = %v, want 5s", n, got)
		}
	}
}

func TestLoopSessionsAreIsolated(t *testing.T) {
	// "work" can never subscribe: it fails forever.
	deadSrc := &failingSource{err: errors.New("socket gone")}

	st := newLoopStream()
	live := &loopSource{
		subs:  []subStep{{stream: st}},
		snaps: []snapStep{{agents: []Agent{ag("a1", "working")}}},
	}

	out := make(chan Update, 64)
	fast := loopOpts()
	fast.Backoff = func(int) time.Duration { return time.Millisecond }
	loopStart(t, "work", deadSrc, out, fast)
	loopStart(t, "labs", live, out, fast)

	seen := map[string][]ev{}
	collect := func(n int) {
		for i := 0; i < n; i++ {
			u := loopNext(t, out)
			seen[u.Session] = append(seen[u.Session], ev{u.Type, u.Agent.ID, u.From, u.To})
		}
	}
	collect(3) // work: Offline; labs: Online, Added (any interleaving)
	if got := seen["work"]; !reflect.DeepEqual(got, []ev{up(UpdateOffline, "", "", "")}) {
		t.Fatalf("work = %v", got)
	}

	// The dead session keeps retrying while the live one keeps emitting.
	waitFor(t, "dead session retries", func() bool { return deadSrc.attempts() >= 5 })
	st.push(SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "blocked")})
	collect(1)
	if got := seen["labs"]; !reflect.DeepEqual(got, []ev{
		up(UpdateOnline, "", "", ""), up(UpdateAdded, "a1", "", ""), up(UpdateTransition, "a1", "working", "blocked"),
	}) {
		t.Fatalf("labs = %v", got)
	}
	loopNone(t, out) // dead session emitted no further Offline spam
}

type failingSource struct {
	mu  sync.Mutex
	n   int
	err error
}

func (f *failingSource) attempts() int { f.mu.Lock(); defer f.mu.Unlock(); return f.n }
func (f *failingSource) Subscribe(context.Context) (Stream, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return nil, f.err
}
func (f *failingSource) Snapshot(context.Context) ([]Agent, error) { return nil, f.err }

type nilStreamSource struct{}

func (nilStreamSource) Subscribe(context.Context) (Stream, error) { return nil, nil }
func (nilStreamSource) Snapshot(context.Context) ([]Agent, error) { return nil, nil }

func TestLoopNilStreamIsAnError(t *testing.T) {
	out := make(chan Update, 4)
	opts := loopOpts()
	opts.Backoff = func(int) time.Duration { return time.Millisecond }
	loopStart(t, "s", nilStreamSource{}, out, opts)
	if u := loopNext(t, out); u.Type != UpdateOffline || u.Err == nil {
		t.Fatalf("got %+v, want Offline with error", u)
	}
}

func TestLoopCancelUnblocksSendAndLeaksNothing(t *testing.T) {
	// Let the runtime settle, then record the baseline.
	time.Sleep(20 * time.Millisecond)
	base := runtime.NumGoroutine()

	st := newLoopStream()
	src := &loopSource{
		subs:  []subStep{{stream: st}},
		snaps: []snapStep{{agents: []Agent{ag("a1", "idle"), ag("a2", "idle")}}},
	}
	out := make(chan Update) // unbuffered and never read: the first emit blocks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Run(ctx, "s", src, out, loopOpts()) }()

	waitFor(t, "snapshot taken", func() bool { return len(src.callList()) >= 3 })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run stayed blocked sending on out after cancel")
	}
	if !st.closed() {
		t.Error("stream not closed on exit")
	}
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= base })
}

func TestLoopCancelDuringBackoffAndEventWait(t *testing.T) {
	time.Sleep(20 * time.Millisecond)
	base := runtime.NumGoroutine()

	for _, tc := range []struct {
		name string
		src  Source
		opts Options
	}{
		{"backoff", &failingSource{err: errors.New("x")}, Options{Backoff: func(int) time.Duration { return time.Hour }}},
		{"waiting-for-events", &loopSource{
			subs:  []subStep{{stream: newLoopStream()}},
			snaps: []snapStep{{agents: nil}},
		}, loopOpts()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan Update, 8)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); Run(ctx, "s", tc.src, out, tc.opts) }()
			loopNext(t, out) // Offline or Online: loop is now parked
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not exit on cancel")
			}
		})
	}
	waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= base })
}

// runLossScript runs a loop against one stream per entry in streams (each ends
// with a queued EventsLost unless it is the final, idle one), serves the same
// one-agent snapshot every time, waits for len(streams) snapshots and returns
// the attempt numbers passed to Backoff.
func runLossScript(t *testing.T, streams []*loopStream) []int {
	t.Helper()
	src := &loopSource{}
	for _, s := range streams {
		src.subs = append(src.subs, subStep{stream: s})
		src.snaps = append(src.snaps, snapStep{agents: []Agent{ag("a1", "idle")}})
	}
	var mu sync.Mutex
	var attempts []int
	opts := loopOpts()
	opts.Backoff = func(n int) time.Duration {
		mu.Lock()
		attempts = append(attempts, n)
		mu.Unlock()
		return 0
	}
	out := make(chan Update, 64)
	loopStart(t, "s", src, out, opts)
	waitFor(t, "all snapshots", func() bool {
		n := 0
		for _, c := range src.callList() {
			if c == "snapshot" {
				n++
			}
		}
		return n == len(streams)
	})
	mu.Lock()
	defer mu.Unlock()
	return append([]int(nil), attempts...)
}

// lostStream is a stream with pre queued events followed by EventsLost.
func lostStream(pre ...SourceEvent) *loopStream {
	s := newLoopStream()
	for _, e := range pre {
		s.push(e)
	}
	s.push(SourceEvent{Kind: EventsLost})
	return s
}

// Repeated EventsLost with no progress in between must back off instead of
// hammering subscribe/snapshot; a single loss still resyncs immediately, and a
// cycle that applied an event (upsert or removal) restarts the streak.
func TestLoopEventsLostBackoff(t *testing.T) {
	upsert := SourceEvent{Kind: AgentUpsert, Agent: ag("a1", "working")}
	removal := SourceEvent{Kind: AgentRemoved, Agent: Agent{ID: "a1"}}
	cases := []struct {
		name    string
		streams func() []*loopStream
		want    []int
	}{
		{
			name: "consecutive losses back off",
			// 1st loss immediate; 2nd and 3rd: Backoff(0), Backoff(1).
			streams: func() []*loopStream {
				return []*loopStream{lostStream(), lostStream(), lostStream(), newLoopStream()}
			},
			want: []int{0, 1},
		},
		{
			name: "upsert progress resets the streak",
			streams: func() []*loopStream {
				return []*loopStream{lostStream(), lostStream(), lostStream(upsert), lostStream(), newLoopStream()}
			},
			want: []int{0, 0},
		},
		{
			name: "removal progress resets the streak",
			streams: func() []*loopStream {
				return []*loopStream{lostStream(), lostStream(), lostStream(removal), lostStream(), newLoopStream()}
			},
			want: []int{0, 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runLossScript(t, tc.streams()); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("backoff attempts = %v, want %v", got, tc.want)
			}
		})
	}
}

// Resync means "topology changed, resubscribe and re-snapshot": it is routine,
// not a failure, so it must never back off even when it repeats.
func TestLoopResyncIsImmediateAndNotCounted(t *testing.T) {
	resync := func() *loopStream {
		s := newLoopStream()
		s.push(SourceEvent{Kind: Resync})
		return s
	}
	got := runLossScript(t, []*loopStream{resync(), resync(), resync(), newLoopStream()})
	if len(got) != 0 {
		t.Fatalf("Backoff called for Resync: %v", got)
	}
}
