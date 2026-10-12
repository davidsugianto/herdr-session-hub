package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTest(t *testing.T) (*Engine, *clock) {
	t.Helper()
	c := &clock{t: t0}
	return NewEngine(Options{Now: c.Now, FlapGuard: 3 * time.Second, Label: func(s string) string { return "L-" + s }}), c
}

func sag(id, status string) sessions.Agent {
	return sessions.Agent{ID: id, PaneID: "w1:p" + id, Name: "claude", Kind: "claude", Status: status, Cwd: "/c/" + id, Title: "t" + id}
}

func add(e *Engine, session string, a sessions.Agent) {
	e.Apply(sessions.Update{Session: session, Type: sessions.UpdateAdded, Agent: a})
}

func trans(e *Engine, session string, a sessions.Agent, from string) {
	e.Apply(sessions.Update{Session: session, Type: sessions.UpdateTransition, Agent: a, From: from, To: a.Status})
}

func drain(ch <-chan Change) []Change {
	var out []Change
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		default:
			return out
		}
	}
}

func mustAgent(t *testing.T, e *Engine, id string) Agent {
	t.Helper()
	a, ok := e.Agent(id)
	if !ok {
		t.Fatalf("agent %q not found", id)
	}
	return a
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		raw  string
		want State
	}{
		{"blocked", StateBlocked},
		{"done", StateFinished},
		{"working", StateWorking},
		{"idle", StateWaiting},
		{"unknown", StateUnknown},
		{"weird", StateUnknown},
		{"", StateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			e, _ := newTest(t)
			add(e, "S", sag("a1", tc.raw))
			a := mustAgent(t, e, "S/a1")
			if a.State != tc.want || a.RawStatus != tc.raw {
				t.Fatalf("state=%q raw=%q, want %q/%q", a.State, a.RawStatus, tc.want, tc.raw)
			}
		})
	}
}

func TestAddedFields(t *testing.T) {
	e, _ := newTest(t)
	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateOnline})
	add(e, "S", sag("a1", "working"))
	got := mustAgent(t, e, "S/a1")
	want := Agent{ID: "S/a1", Session: "S", SessionLabel: "L-S", TerminalID: "a1", PaneID: "w1:pa1", Name: "claude", Kind: "claude",
		Cwd: "/c/a1", Title: "ta1", State: StateWorking, RawStatus: "working", Since: t0, Online: true}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if _, ok := e.Agent("S/nope"); ok {
		t.Fatal("unexpected agent")
	}
}

func TestTransitionChangesAndSince(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
		wantChg  bool
		wantFrom State
		wantTo   State
	}{
		{"working to blocked", "working", "blocked", true, StateWorking, StateBlocked},
		{"working to done", "working", "done", true, StateWorking, StateFinished},
		{"done to idle", "done", "idle", true, StateFinished, StateWaiting},
		{"idle to working", "idle", "working", true, StateWaiting, StateWorking},
		{"unknown to weird is silent", "unknown", "weird", false, "", ""},
		{"weird to unknown is silent", "weird", "unknown", false, "", ""},
		{"working to unknown", "working", "unknown", true, StateWorking, StateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newTest(t)
			add(e, "S", sag("a1", tc.from))
			ch := e.Subscribe(context.Background())
			c.Advance(10 * time.Second)
			trans(e, "S", sag("a1", tc.to), tc.from)
			got := drain(ch)
			a := mustAgent(t, e, "S/a1")
			if a.RawStatus != tc.to {
				t.Fatalf("raw = %q, want %q", a.RawStatus, tc.to)
			}
			if !tc.wantChg {
				if len(got) != 0 {
					t.Fatalf("unexpected changes %+v", got)
				}
				if !a.Since.Equal(t0) {
					t.Fatalf("Since moved to %v", a.Since)
				}
				return
			}
			if len(got) != 1 || got[0].Type != ChangeState || got[0].From != tc.wantFrom || got[0].To != tc.wantTo {
				t.Fatalf("changes = %+v", got)
			}
			if !got[0].At.Equal(t0.Add(10*time.Second)) || !a.Since.Equal(t0.Add(10*time.Second)) {
				t.Fatalf("At/Since = %v/%v", got[0].At, a.Since)
			}
			if got[0].Agent.State != tc.wantTo || got[0].Session != "S" {
				t.Fatalf("change agent = %+v", got[0])
			}
		})
	}
}

func TestFlapGuard(t *testing.T) {
	type step struct {
		after  time.Duration
		status string
		// wantEpisode is checked only for transitions into blocked
		wantEpisode bool
	}
	cases := []struct {
		name  string
		first string // initial status
		steps []step
	}{
		{"first block is a new episode", "working", []step{{time.Second, "blocked", true}}},
		{"bounce inside window", "working", []step{
			{time.Second, "blocked", true},
			{time.Second, "working", false},
			{time.Second, "blocked", false},
		}},
		{"bounce outside window", "working", []step{
			{time.Second, "blocked", true},
			{time.Second, "working", false},
			{5 * time.Second, "blocked", true},
		}},
		{"window runs from leaving blocked", "working", []step{
			{time.Second, "blocked", true},
			{10 * time.Second, "working", false},
			{2 * time.Second, "blocked", false},
		}},
		{"added blocked then bounce", "blocked", []step{
			{time.Second, "working", false},
			{time.Second, "blocked", false},
		}},
		{"added blocked, long gap", "blocked", []step{
			{time.Second, "working", false},
			{4 * time.Second, "blocked", true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newTest(t)
			add(e, "S", sag("a1", tc.first))
			ch := e.Subscribe(context.Background())
			prev := tc.first
			for i, s := range tc.steps {
				c.Advance(s.after)
				trans(e, "S", sag("a1", s.status), prev)
				prev = s.status
				got := drain(ch)
				if len(got) != 1 || got[0].Type != ChangeState {
					t.Fatalf("step %d: changes = %+v (every state change must publish)", i, got)
				}
				if s.status == "blocked" && got[0].NewEpisode != s.wantEpisode {
					t.Fatalf("step %d: NewEpisode = %v, want %v", i, got[0].NewEpisode, s.wantEpisode)
				}
				if s.status != "blocked" && got[0].NewEpisode {
					t.Fatalf("step %d: NewEpisode set on non-blocked change", i)
				}
			}
		})
	}
}

func TestMarkSeen(t *testing.T) {
	t.Run("finished becomes waiting", func(t *testing.T) {
		e, c := newTest(t)
		add(e, "S", sag("a1", "done"))
		ch := e.Subscribe(context.Background())
		c.Advance(time.Minute)
		if !e.MarkSeen("S/a1") {
			t.Fatal("MarkSeen = false")
		}
		got := drain(ch)
		if len(got) != 1 || got[0].Type != ChangeState || got[0].From != StateFinished || got[0].To != StateWaiting {
			t.Fatalf("changes = %+v", got)
		}
		a := mustAgent(t, e, "S/a1")
		if a.State != StateWaiting || a.RawStatus != "done" || !a.Since.Equal(t0.Add(time.Minute)) {
			t.Fatalf("agent = %+v", a)
		}
		// Second call is a no-op but the agent exists.
		if !e.MarkSeen("S/a1") || len(drain(ch)) != 0 {
			t.Fatal("second MarkSeen should be silent and true")
		}
	})
	t.Run("missing agent", func(t *testing.T) {
		e, _ := newTest(t)
		if e.MarkSeen("S/none") {
			t.Fatal("MarkSeen on missing agent = true")
		}
	})
	t.Run("other states untouched", func(t *testing.T) {
		for _, st := range []string{"working", "blocked", "idle", "unknown"} {
			e, _ := newTest(t)
			add(e, "S", sag("a1", st))
			ch := e.Subscribe(context.Background())
			if !e.MarkSeen("S/a1") || len(drain(ch)) != 0 {
				t.Fatalf("%s: MarkSeen changed something", st)
			}
			if got := mustAgent(t, e, "S/a1").State; got != mapStatus(st) {
				t.Fatalf("%s: state = %s", st, got)
			}
		}
	})
	t.Run("stays waiting on repeated done, finished again after working", func(t *testing.T) {
		e, _ := newTest(t)
		add(e, "S", sag("a1", "done"))
		e.MarkSeen("S/a1")
		ch := e.Subscribe(context.Background())
		// A re-report of the same raw status (e.g. Changed) must not revive Finished.
		e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateChanged, Agent: sag("a1", "done")})
		if s := mustAgent(t, e, "S/a1").State; s != StateWaiting {
			t.Fatalf("state after repeat done = %s", s)
		}
		drain(ch)
		trans(e, "S", sag("a1", "working"), "done")
		trans(e, "S", sag("a1", "done"), "working")
		got := drain(ch)
		if len(got) != 2 || got[1].From != StateWorking || got[1].To != StateFinished {
			t.Fatalf("changes = %+v", got)
		}
	})
	t.Run("done to idle after seen is silent", func(t *testing.T) {
		e, _ := newTest(t)
		add(e, "S", sag("a1", "done"))
		e.MarkSeen("S/a1")
		ch := e.Subscribe(context.Background())
		trans(e, "S", sag("a1", "idle"), "done")
		if got := drain(ch); len(got) != 0 {
			t.Fatalf("changes = %+v", got)
		}
		if a := mustAgent(t, e, "S/a1"); a.State != StateWaiting || a.RawStatus != "idle" {
			t.Fatalf("agent = %+v", a)
		}
	})
}

func TestOfflineOnline(t *testing.T) {
	e, _ := newTest(t)
	e.Apply(sessions.Update{Session: "A", Type: sessions.UpdateOnline})
	e.Apply(sessions.Update{Session: "B", Type: sessions.UpdateOnline})
	add(e, "A", sag("a1", "working"))
	add(e, "B", sag("b1", "idle"))
	ch := e.Subscribe(context.Background())

	e.Apply(sessions.Update{Session: "A", Type: sessions.UpdateOffline, Err: fmt.Errorf("boom")})
	got := drain(ch)
	if len(got) != 1 || got[0].Type != ChangeSessionOffline || got[0].Session != "A" || got[0].Agent.ID != "" {
		t.Fatalf("changes = %+v", got)
	}
	if a := mustAgent(t, e, "A/a1"); a.Online || a.State != StateWorking {
		t.Fatalf("A/a1 = %+v (must be kept, offline, state unchanged)", a)
	}
	if !mustAgent(t, e, "B/b1").Online {
		t.Fatal("B/b1 went offline")
	}
	wantSessions := []SessionInfo{{"A", "L-A", false, 1}, {"B", "L-B", true, 1}}
	gotS := e.Sessions()
	if len(gotS) != 2 || gotS[0] != wantSessions[0] || gotS[1] != wantSessions[1] {
		t.Fatalf("Sessions = %+v, want %+v", gotS, wantSessions)
	}

	// An agent added while offline is offline.
	add(e, "A", sag("a2", "idle"))
	if mustAgent(t, e, "A/a2").Online {
		t.Fatal("agent added to offline session is online")
	}
	drain(ch)

	e.Apply(sessions.Update{Session: "A", Type: sessions.UpdateOnline})
	got = drain(ch)
	if len(got) != 1 || got[0].Type != ChangeSessionOnline {
		t.Fatalf("changes = %+v", got)
	}
	if !mustAgent(t, e, "A/a1").Online || !mustAgent(t, e, "A/a2").Online {
		t.Fatal("agents not back online")
	}
	if !e.Sessions()[0].Online || e.Sessions()[0].Agents != 2 {
		t.Fatalf("Sessions = %+v", e.Sessions())
	}
}

func TestPaneMoveIsUpdatedNotState(t *testing.T) {
	e, c := newTest(t)
	add(e, "S", sag("a1", "working"))
	ch := e.Subscribe(context.Background())
	c.Advance(time.Hour)
	moved := sag("a1", "working")
	moved.PaneID = "w9:p9"
	moved.Title = "new title"
	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateChanged, Agent: moved})
	got := drain(ch)
	if len(got) != 1 || got[0].Type != ChangeUpdated || got[0].Agent.PaneID != "w9:p9" {
		t.Fatalf("changes = %+v", got)
	}
	a := mustAgent(t, e, "S/a1")
	if a.PaneID != "w9:p9" || a.Title != "new title" || !a.Since.Equal(t0) || a.State != StateWorking {
		t.Fatalf("agent = %+v", a)
	}
	// ID is stable.
	if a.ID != "S/a1" {
		t.Fatalf("id = %s", a.ID)
	}
}

func TestRemoved(t *testing.T) {
	e, _ := newTest(t)
	add(e, "S", sag("a1", "blocked"))
	ch := e.Subscribe(context.Background())
	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateRemoved, Agent: sag("a1", "blocked")})
	got := drain(ch)
	if len(got) != 1 || got[0].Type != ChangeRemoved || got[0].Agent.ID != "S/a1" {
		t.Fatalf("changes = %+v", got)
	}
	if _, ok := e.Agent("S/a1"); ok {
		t.Fatal("agent still present")
	}
	// Removing an unknown agent is silent.
	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateRemoved, Agent: sag("zz", "idle")})
	if len(drain(ch)) != 0 {
		t.Fatal("unexpected change")
	}
}

func TestAgentsOrderingAndFilter(t *testing.T) {
	e, _ := newTest(t)
	add(e, "B", sag("w1", "working"))
	add(e, "A", sag("u1", "unknown"))
	add(e, "A", sag("i2", "idle"))
	add(e, "A", sag("i1", "idle"))
	add(e, "B", sag("d1", "done"))
	add(e, "A", sag("b1", "blocked"))
	add(e, "A", sag("w0", "working"))

	ids := func(as []Agent) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.ID)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	all := ids(e.Agents(""))
	wantAll := []string{"A/b1", "B/d1", "A/w0", "B/w1", "A/i1", "A/i2", "A/u1"}
	if !eq(all, wantAll) {
		t.Fatalf("all = %v, want %v", all, wantAll)
	}
	a := ids(e.Agents("A"))
	wantA := []string{"A/b1", "A/w0", "A/i1", "A/i2", "A/u1"}
	if !eq(a, wantA) {
		t.Fatalf("A = %v, want %v", a, wantA)
	}
	if len(e.Agents("nope")) != 0 {
		t.Fatal("expected no agents")
	}
}

func TestSessionsSortedAndLabelDefault(t *testing.T) {
	e := NewEngine(Options{})
	e.Apply(sessions.Update{Session: "zeta", Type: sessions.UpdateOnline})
	e.Apply(sessions.Update{Session: "alpha", Type: sessions.UpdateOffline})
	got := e.Sessions()
	if len(got) != 2 || got[0].Name != "alpha" || got[0].Label != "alpha" || got[1].Name != "zeta" || !got[1].Online {
		t.Fatalf("sessions = %+v", got)
	}
	add(e, "zeta", sag("a1", "idle"))
	if a := mustAgent(t, e, "zeta/a1"); a.SessionLabel != "zeta" || a.Since.IsZero() {
		t.Fatalf("agent = %+v (default clock/label)", a)
	}
}

func TestSubscribeDropsSlowSubscriber(t *testing.T) {
	e, _ := newTest(t)
	slow := e.Subscribe(context.Background())
	fast := e.Subscribe(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		// More changes than the buffer holds; nobody reads `slow`.
		for i := 0; i < subscriberBuffer+50; i++ {
			add(e, "S", sag(fmt.Sprintf("a%d", i), "idle"))
			drain(fast) // keep fast subscribed
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Apply blocked on a full subscriber")
	}

	n := 0
	closed := false
	for !closed {
		select {
		case _, ok := <-slow:
			if !ok {
				closed = true
				break
			}
			n++
		case <-time.After(time.Second):
			t.Fatal("slow subscriber channel was not closed")
		}
	}
	if n != subscriberBuffer {
		t.Fatalf("slow subscriber received %d buffered changes, want %d", n, subscriberBuffer)
	}
	// The fast one is still attached.
	add(e, "S", sag("last", "idle"))
	got := drain(fast)
	if len(got) != 1 || got[0].Agent.ID != "S/last" {
		t.Fatalf("fast subscriber got %+v", got)
	}
}

func TestSubscribeClosedOnContextDone(t *testing.T) {
	e, _ := newTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := e.Subscribe(ctx)
	add(e, "S", sag("a1", "idle"))
	if c := <-ch; c.Type != ChangeAdded {
		t.Fatalf("change = %+v", c)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("got a value, want closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
	e.mu.Lock()
	n := len(e.subs)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d subscribers left", n)
	}
	add(e, "S", sag("a2", "idle")) // must not panic on a closed channel

	// Already-cancelled context yields a closed channel.
	if _, ok := <-e.Subscribe(ctx); ok {
		t.Fatal("expected closed channel for cancelled ctx")
	}
}

func TestConcurrentApplyAndReads(t *testing.T) {
	e := NewEngine(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var readers sync.WaitGroup
	for i := 0; i < 3; i++ {
		ch := e.Subscribe(ctx)
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range ch {
			}
		}()
	}

	var wg sync.WaitGroup
	statuses := []string{"working", "blocked", "done", "idle", "unknown"}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			sess := fmt.Sprintf("S%d", w%2)
			e.Apply(sessions.Update{Session: sess, Type: sessions.UpdateOnline})
			for i := 0; i < 300; i++ {
				id := fmt.Sprintf("a%d", i%5)
				a := sag(id, statuses[i%len(statuses)])
				if i%7 == 0 {
					add(e, sess, a)
				} else if i%11 == 0 {
					e.Apply(sessions.Update{Session: sess, Type: sessions.UpdateRemoved, Agent: a})
				} else {
					trans(e, sess, a, "x")
				}
				e.MarkSeen(sess + "/" + id)
				_ = e.Agents("")
				_ = e.Sessions()
				_, _ = e.Agent(sess + "/" + id)
				if i%50 == 0 {
					e.Apply(sessions.Update{Session: sess, Type: sessions.UpdateOffline})
					e.Apply(sessions.Update{Session: sess, Type: sessions.UpdateOnline})
				}
			}
		}(w)
	}
	wg.Wait()
	cancel()
	readers.Wait()
}

// herdr's agent name is user-assigned; the agent type (Kind) is what selects a
// prompt profile, so the engine must expose it separately.
func TestAgentKindSeparateFromName(t *testing.T) {
	e, _ := newTest(t)
	a := sag("a1", "idle")
	a.Name, a.Kind = "my-worker", "claude"
	add(e, "S", a)
	got := mustAgent(t, e, "S/a1")
	if got.Name != "my-worker" || got.Kind != "claude" {
		t.Fatalf("Name/Kind = %q/%q, want my-worker/claude", got.Name, got.Kind)
	}

	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateChanged, Agent: func() sessions.Agent { a.Kind = "pi"; return a }(), At: t0})
	if k := mustAgent(t, e, "S/a1").Kind; k != "pi" {
		t.Fatalf("Kind after UpdateChanged = %q, want pi", k)
	}
}
