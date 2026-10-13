package macos

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
)

type fakeEngine struct {
	mu     sync.Mutex
	agents []model.Agent
	subs   chan chan model.Change // every Subscribe hands its channel here
	nsub   atomic.Int32
}

func newFakeEngine(agents ...model.Agent) *fakeEngine {
	return &fakeEngine{agents: agents, subs: make(chan chan model.Change, 8)}
}

func (e *fakeEngine) Subscribe(ctx context.Context) <-chan model.Change {
	ch := make(chan model.Change, 8)
	e.nsub.Add(1)
	e.subs <- ch
	return ch
}

func (e *fakeEngine) Agents(string) []model.Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.Agent(nil), e.agents...)
}

func (e *fakeEngine) set(agents ...model.Agent) {
	e.mu.Lock()
	e.agents = agents
	e.mu.Unlock()
}

func working(id string) model.Agent {
	return model.Agent{ID: id, State: model.StateWorking, Online: true}
}

// fakeStart counts live assertions.
type fakeStart struct {
	starts, stops atomic.Int32
	err           error
}

func (f *fakeStart) start(ctx context.Context, pid int) (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.starts.Add(1)
	var once sync.Once
	return func() { once.Do(func() { f.stops.Add(1) }) }, nil
}

func (f *fakeStart) held() int32 { return f.starts.Load() - f.stops.Load() }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, o Options) (cancel func(), done <-chan struct{}) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() { Run(ctx, o); close(d) }()
	t.Cleanup(func() { c(); <-d })
	return c, d
}

func nextSub(t *testing.T, e *fakeEngine) chan model.Change {
	t.Helper()
	select {
	case ch := <-e.subs:
		return ch
	case <-time.After(2 * time.Second):
		t.Fatal("no subscription")
		return nil
	}
}

func TestOffReturnsImmediately(t *testing.T) {
	f := &fakeStart{}
	done := make(chan struct{})
	go func() {
		Run(context.Background(), Options{Mode: "off", Engine: newFakeEngine(), Start: f.start})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("off mode must return immediately")
	}
	if f.starts.Load() != 0 {
		t.Fatal("must not start")
	}
}

func TestAlwaysHoldsFromStartAndStopsOnExit(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine()
	cancel, done := run(t, Options{Mode: "always", Engine: e, Start: f.start, Grace: 10 * time.Millisecond})
	eventually(t, "held", func() bool { return f.held() == 1 })
	// Never released while idle, even after the grace period.
	time.Sleep(60 * time.Millisecond)
	if f.held() != 1 {
		t.Fatal("always must keep holding")
	}
	cancel()
	<-done
	if f.held() != 0 {
		t.Fatal("must stop on exit")
	}
}

func TestWhileWorkingHoldsAfterInitialCount(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine(working("w/1"), model.Agent{ID: "w/2", State: model.StateBlocked, Online: true})
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond})
	eventually(t, "held", func() bool { return f.held() == 1 })
}

func TestWhileWorkingIdleDoesNotHold(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine(model.Agent{ID: "w/1", State: model.StateWorking, Online: false})
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond})
	nextSub(t, e)
	time.Sleep(40 * time.Millisecond)
	if f.starts.Load() != 0 {
		t.Fatal("offline agents do not count")
	}
}

func TestWhileWorkingFollowsChangesWithGrace(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine()
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 80 * time.Millisecond})
	ch := nextSub(t, e)

	e.set(working("w/1"))
	ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	eventually(t, "held", func() bool { return f.held() == 1 })

	// Back to idle: still held during grace, released after.
	e.set()
	ch <- model.Change{Type: model.ChangeState, To: model.StateWaiting}
	time.Sleep(20 * time.Millisecond)
	if f.held() != 1 {
		t.Fatal("must hold during grace")
	}
	eventually(t, "released", func() bool { return f.held() == 0 })

	// Work resumes: a single new assertion.
	e.set(working("w/1"))
	ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	eventually(t, "held again", func() bool { return f.held() == 1 })
	if f.starts.Load() != 2 {
		t.Fatalf("starts = %d", f.starts.Load())
	}
}

func TestGraceIsCancelledWhenWorkResumes(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine(working("w/1"))
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 100 * time.Millisecond})
	ch := nextSub(t, e)
	eventually(t, "held", func() bool { return f.held() == 1 })

	e.set()
	ch <- model.Change{Type: model.ChangeRemoved}
	time.Sleep(30 * time.Millisecond)
	e.set(working("w/9"))
	ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	time.Sleep(200 * time.Millisecond)
	if f.held() != 1 || f.starts.Load() != 1 {
		t.Fatalf("held=%d starts=%d, want the original assertion kept", f.held(), f.starts.Load())
	}
}

func TestSessionOfflineReconciles(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine(working("w/1"))
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond})
	ch := nextSub(t, e)
	eventually(t, "held", func() bool { return f.held() == 1 })
	e.set(model.Agent{ID: "w/1", State: model.StateWorking, Online: false})
	ch <- model.Change{Type: model.ChangeSessionOffline, Session: "w"}
	eventually(t, "released", func() bool { return f.held() == 0 })
}

func TestResubscribesWhenChannelCloses(t *testing.T) {
	f := &fakeStart{}
	e := newFakeEngine()
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond, resubscribeDelay: time.Millisecond})
	ch := nextSub(t, e)
	close(ch)
	// Work that began while we were unsubscribed is picked up by the reconcile.
	e.set(working("w/1"))
	ch2 := nextSub(t, e)
	_ = ch2
	eventually(t, "held", func() bool { return f.held() == 1 })
}

func TestStartErrorIsLoggedNotFatal(t *testing.T) {
	f := &fakeStart{err: context.DeadlineExceeded}
	var log bytes.Buffer
	e := newFakeEngine(working("w/1"))
	var mu sync.Mutex
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond, Log: lockedWriter{&mu, &log}})
	nextSub(t, e)
	eventually(t, "log", func() bool { mu.Lock(); defer mu.Unlock(); return log.Len() > 0 })
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// flakyStart fails while fail is true, otherwise holds like fakeStart.
type flakyStart struct {
	fakeStart
	fail     atomic.Bool
	attempts atomic.Int32
}

func (f *flakyStart) start(ctx context.Context, pid int) (func(), error) {
	f.attempts.Add(1)
	if f.fail.Load() {
		return nil, errors.New("boom")
	}
	return f.fakeStart.start(ctx, pid)
}

func countLines(mu *sync.Mutex, b *bytes.Buffer, sub string) int {
	mu.Lock()
	defer mu.Unlock()
	return strings.Count(b.String(), sub)
}

func TestStartFailureLoggedOncePerFailureStreak(t *testing.T) {
	f := &flakyStart{}
	f.fail.Store(true)
	var log bytes.Buffer
	var mu sync.Mutex
	e := newFakeEngine(working("w/1"))
	run(t, Options{Mode: "while-working", Engine: e, Start: f.start, Grace: 10 * time.Millisecond, Log: lockedWriter{&mu, &log}})
	ch := nextSub(t, e)

	// Many changes while failing: retried every time, logged once.
	for i := 0; i < 10; i++ {
		ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	}
	eventually(t, "retries", func() bool { return f.attempts.Load() >= 10 })
	if n := countLines(&mu, &log, "start failed"); n != 1 {
		t.Fatalf("start failed logged %d times, want 1", n)
	}

	// Recovers quietly.
	f.fail.Store(false)
	ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	eventually(t, "held", func() bool { return f.held() == 1 })

	// Release, then fail again: one more line.
	e.set()
	ch <- model.Change{Type: model.ChangeState, To: model.StateWaiting}
	eventually(t, "released", func() bool { return f.held() == 0 })
	f.fail.Store(true)
	e.set(working("w/1"))
	before := f.attempts.Load()
	for i := 0; i < 5; i++ {
		ch <- model.Change{Type: model.ChangeState, To: model.StateWorking}
	}
	eventually(t, "retries", func() bool { return f.attempts.Load() >= before+5 })
	if n := countLines(&mu, &log, "start failed"); n != 2 {
		t.Fatalf("start failed logged %d times, want 2", n)
	}
}
