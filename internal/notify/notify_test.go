package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/profiles"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func testAgent() model.Agent {
	return model.Agent{ID: "work/t1", Session: "work", SessionLabel: "work", TerminalID: "t1",
		Name: "upsera", Kind: "claude", State: model.StateBlocked, Online: true}
}

type harness struct {
	n      *Notifier
	eng    *fakeEngine
	st     *fakeStore
	pr     *fakePrompts
	sink   *fakeSink
	log    *syncBuffer
	labels map[string]string
}

func newHarness(t *testing.T, mod func(*Options)) *harness {
	t.Helper()
	h := &harness{
		eng:    newFakeEngine(testAgent()),
		st:     newFakeStore(),
		pr:     &fakePrompts{},
		sink:   newFakeSink(),
		log:    &syncBuffer{},
		labels: map[string]string{"work": "labs"},
	}
	_ = h.st.AddPushSub(context.Background(), store.PushSub{Endpoint: "https://push.example/1", DeviceID: "d1"})
	o := Options{Engine: h.eng, Store: h.st, Prompts: h.pr, Sink: h.sink, Log: h.log,
		Now: func() time.Time { return t0 }, Label: func(s string) string { return h.labels[s] }}
	if mod != nil {
		mod(&o)
	}
	n, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	h.n = n
	return h
}

func stateChange(to model.State, newEpisode bool) model.Change {
	a := testAgent()
	a.State = to
	return model.Change{Type: model.ChangeState, Session: a.Session, Agent: a, From: model.StateWorking,
		To: to, NewEpisode: newEpisode, At: t0}
}

func (h *harness) handle(c model.Change) []sent {
	h.n.handle(context.Background(), c, t0)
	return h.sink.sends()
}

func TestRules(t *testing.T) {
	added := stateChange(model.StateBlocked, true)
	added.Type = model.ChangeAdded
	cases := []struct {
		name string
		c    model.Change
		want Kind // "" = nothing sent
	}{
		{"blocked new episode", stateChange(model.StateBlocked, true), KindBlocked},
		{"blocked bounce", stateChange(model.StateBlocked, false), ""},
		{"finished", stateChange(model.StateFinished, false), KindFinished},
		{"working", stateChange(model.StateWorking, false), ""},
		{"waiting", stateChange(model.StateWaiting, false), ""},
		{"unknown", stateChange(model.StateUnknown, false), ""},
		{"added already blocked", added, ""},
		{"updated", model.Change{Type: model.ChangeUpdated, Agent: testAgent(), At: t0}, ""},
		{"removed", model.Change{Type: model.ChangeRemoved, Agent: testAgent(), At: t0}, ""},
		{"session online", model.Change{Type: model.ChangeSessionOnline, Session: "work", At: t0}, ""},
		{"session offline", model.Change{Type: model.ChangeSessionOffline, Session: "work", At: t0}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			got := h.handle(tc.c)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("sent %d, want none", len(got))
				}
				return
			}
			if len(got) != 1 || got[0].p.Kind != tc.want {
				t.Fatalf("sent %+v, want one %s", got, tc.want)
			}
		})
	}
}

func TestMutes(t *testing.T) {
	cases := []struct {
		name string
		m    store.Mute
		send bool
	}{
		{"agent mute", store.Mute{Scope: store.MuteAgent, Key: "work/t1"}, false},
		{"session mute", store.Mute{Scope: store.MuteSession, Key: "work"}, false},
		{"mute until future", store.Mute{Scope: store.MuteAgent, Key: "work/t1", Until: t0.Add(time.Minute)}, false},
		{"expired mute", store.Mute{Scope: store.MuteAgent, Key: "work/t1", Until: t0.Add(-time.Minute)}, true},
		{"other agent", store.Mute{Scope: store.MuteAgent, Key: "work/t2"}, true},
		{"other session", store.Mute{Scope: store.MuteSession, Key: "labs"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			_ = h.st.SetMute(context.Background(), tc.m)
			if got := h.handle(stateChange(model.StateBlocked, true)); (len(got) == 1) != tc.send {
				t.Fatalf("sent %d, want send=%v", len(got), tc.send)
			}
		})
	}
}

func TestMuteStoreErrorStillSends(t *testing.T) {
	h := newHarness(t, nil)
	h.st.mutesErr = errors.New("disk")
	if got := h.handle(stateChange(model.StateFinished, false)); len(got) != 1 {
		t.Fatalf("sent %d, want 1", len(got))
	}
}

func TestQuietHours(t *testing.T) {
	cases := []struct {
		name        string
		to          model.State
		active      bool
		breaksQuiet bool
		send        bool
	}{
		{"finished quiet", model.StateFinished, true, true, false},
		{"blocked quiet breaks", model.StateBlocked, true, true, true},
		{"blocked quiet no break", model.StateBlocked, true, false, false},
		{"finished not quiet", model.StateFinished, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSession string
			var gotTime time.Time
			h := newHarness(t, func(o *Options) {
				o.Quiet = quietFunc(func(s string, at time.Time) bool { gotSession, gotTime = s, at; return tc.active })
				o.BlockedBreaksQuiet = tc.breaksQuiet
			})
			got := h.handle(stateChange(tc.to, true))
			if (len(got) == 1) != tc.send {
				t.Fatalf("sent %d, want send=%v", len(got), tc.send)
			}
			if gotSession != "work" || !gotTime.Equal(t0) {
				t.Fatalf("quiet asked (%q, %v)", gotSession, gotTime)
			}
		})
	}
}

func TestGoneSubscriptionPrunedAndErrorsLogged(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_ = h.st.AddPushSub(ctx, store.PushSub{Endpoint: "https://push.example/2", DeviceID: "d2"})
	_ = h.st.AddPushSub(ctx, store.PushSub{Endpoint: "https://push.example/3", DeviceID: "d3"})
	h.sink.errs["https://push.example/2"] = ErrGone
	h.sink.errs["https://push.example/3"] = errors.New("boom 500")

	if got := h.handle(stateChange(model.StateBlocked, true)); len(got) != 3 {
		t.Fatalf("sent %d, want 3", len(got))
	}
	subs, _ := h.st.PushSubs(ctx)
	if len(subs) != 2 || len(h.st.removed) != 1 || h.st.removed[0] != "https://push.example/2" {
		t.Fatalf("subs %+v removed %v", subs, h.st.removed)
	}
	log := h.log.String()
	if !strings.Contains(log, "boom 500") || !strings.Contains(log, "notify: blocked work/t1 sent to 1/3 subs in 0ms") {
		t.Fatalf("log = %q", log)
	}
}

func TestPayload(t *testing.T) {
	h := newHarness(t, nil)
	h.eng.agents = []model.Agent{
		testAgent(),
		{ID: "work/t2", State: model.StateFinished, Online: true},
		{ID: "work/t3", State: model.StateBlocked, Online: false}, // offline: not counted
		{ID: "work/t4", State: model.StateWorking, Online: true},
		{ID: "work/t5", State: model.StateWaiting, Online: true},
	}
	h.pr.prompt = actions.PromptView{Prompt: profiles.Prompt{Text: "Allow Bash(go test ./...)?"}}
	got := h.handle(stateChange(model.StateBlocked, true))
	if len(got) != 1 {
		t.Fatalf("sent %d", len(got))
	}
	want := Payload{Title: "[labs] claude · upsera is blocked", Body: "Allow Bash(go test ./...)?",
		URL: "/#/a/work%2Ft1", Tag: "work/t1", Badge: 2, Kind: KindBlocked, AgentID: "work/t1"}
	if got[0].p != want {
		t.Fatalf("payload\n got %+v\nwant %+v", got[0].p, want)
	}
}

func TestTitle(t *testing.T) {
	cases := []struct {
		name, kind, want string
	}{
		{"upsera", "claude", "[labs] claude · upsera is finished"},
		{"claude", "claude", "[labs] claude is finished"},
		{"upsera", "", "[labs] upsera is finished"},
	}
	for _, tc := range cases {
		a := testAgent()
		a.Name, a.Kind = tc.name, tc.kind
		if got := title("labs", KindFinished, a); got != tc.want {
			t.Errorf("title(%q,%q) = %q, want %q", tc.name, tc.kind, got, tc.want)
		}
	}
}

func TestLabelFallsBackToSessionLabel(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Label = nil })
	got := h.handle(stateChange(model.StateFinished, false))
	if len(got) != 1 || !strings.HasPrefix(got[0].p.Title, "[work] ") {
		t.Fatalf("got %+v", got)
	}
}

func TestBody(t *testing.T) {
	long := strings.Repeat("é", 300)
	cases := []struct {
		name   string
		to     model.State
		pr     *fakePrompts
		want   string
		budget time.Duration
	}{
		{"blocked prompt", model.StateBlocked,
			&fakePrompts{prompt: actions.PromptView{Prompt: &profiles.Prompt{Text: "Run it?"}}, output: "last\n"}, "Run it?", 0},
		{"blocked no prompt uses output", model.StateBlocked,
			&fakePrompts{prompt: actions.PromptView{}, output: "one\n\x1b[1mtwo \x1b[0m\n  \n\n"}, "two", 0},
		{"blocked prompt error uses output", model.StateBlocked,
			&fakePrompts{promptErr: errors.New("x"), output: "line"}, "line", 0},
		{"blocked generic", model.StateBlocked,
			&fakePrompts{promptErr: errors.New("x"), outputErr: errors.New("y")}, "Needs your input", 0},
		{"blocked budget exceeded", model.StateBlocked,
			&fakePrompts{prompt: actions.PromptView{Prompt: profiles.Prompt{Text: "Run it?"}}, output: "x", delay: time.Second},
			"Needs your input", 50 * time.Millisecond},
		{"finished output", model.StateFinished, &fakePrompts{output: "\x1b]0;title\x07All tests pass\r\n"}, "All tests pass", 0},
		{"finished generic", model.StateFinished, &fakePrompts{output: " \n \n"}, "Done", 0},
		{"finished budget exceeded", model.StateFinished, &fakePrompts{output: "x", delay: time.Second}, "Done", 50 * time.Millisecond},
		{"capped", model.StateFinished, &fakePrompts{output: long}, strings.Repeat("é", 159) + "…", 0},
		{"single line", model.StateBlocked,
			&fakePrompts{prompt: actions.PromptView{Prompt: profiles.Prompt{Text: "Allow\n  this\tcommand?"}}}, "Allow this command?", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(o *Options) { o.PromptBudget = tc.budget })
			h.pr.prompt, h.pr.promptErr, h.pr.output, h.pr.outputErr, h.pr.delay =
				tc.pr.prompt, tc.pr.promptErr, tc.pr.output, tc.pr.outputErr, tc.pr.delay
			start := time.Now()
			got := h.handle(stateChange(tc.to, true))
			if len(got) != 1 {
				t.Fatalf("sent %d", len(got))
			}
			if got[0].p.Body != tc.want {
				t.Fatalf("body = %q, want %q", got[0].p.Body, tc.want)
			}
			if n := utf8.RuneCountInString(got[0].p.Body); n > maxBodyRunes {
				t.Fatalf("body has %d runes", n)
			}
			if tc.budget > 0 && time.Since(start) > 500*time.Millisecond {
				t.Fatalf("took %v with budget %v", time.Since(start), tc.budget)
			}
			if pc, oc := h.pr.calls(); pc > 1 || oc > 1 {
				t.Fatalf("prompt read %d, output read %d times", pc, oc)
			}
		})
	}
}

func TestFinishedDoesNotReadPrompt(t *testing.T) {
	h := newHarness(t, nil)
	h.pr.output = "ok"
	h.handle(stateChange(model.StateFinished, false))
	if pc, _ := h.pr.calls(); pc != 0 {
		t.Fatalf("CurrentPrompt called %d times", pc)
	}
}

func TestNoSubscriptionsSkipsReads(t *testing.T) {
	h := newHarness(t, nil)
	_ = h.st.RemovePushSub(context.Background(), "https://push.example/1")
	h.handle(stateChange(model.StateBlocked, true))
	if pc, oc := h.pr.calls(); pc+oc != 0 || len(h.sink.sends()) != 0 {
		t.Fatal("work done with no subscriptions")
	}
}

func TestRunSendsAndResubscribes(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.n.Run(ctx); close(done) }()

	first := <-h.eng.subs
	first <- stateChange(model.StateWorking, false)
	first <- stateChange(model.StateBlocked, true)
	expectSend(t, h.sink, KindBlocked)
	close(first) // engine dropped the subscriber

	var second chan model.Change
	select {
	case second = <-h.eng.subs:
	case <-time.After(time.Second):
		t.Fatal("no resubscribe")
	}
	second <- stateChange(model.StateFinished, false)
	expectSend(t, h.sink, KindFinished)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
	if !strings.Contains(h.log.String(), "resubscribing") {
		t.Fatalf("log = %q", h.log.String())
	}
	if n := len(h.sink.sends()); n != 2 {
		t.Fatalf("sent %d, want 2", n)
	}
}

func TestRunStopsWhenClosedAfterCancel(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.n.Run(ctx); close(done) }()
	ch := <-h.eng.subs
	cancel()
	close(ch)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-h.eng.subs:
		t.Fatal("resubscribed after cancel")
	default:
	}
}

func expectSend(t *testing.T, s *fakeSink, k Kind) {
	t.Helper()
	select {
	case got := <-s.ch:
		if got.p.Kind != k {
			t.Fatalf("sent %s, want %s", got.p.Kind, k)
		}
	case <-time.After(time.Second):
		t.Fatalf("no %s send", k)
	}
}

func TestVAPIDGeneratedOnceAndReused(t *testing.T) {
	st := newFakeStore()
	n1, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if n1.PublicKey() == "" || st.settings[store.SettingVAPIDPublic] != n1.PublicKey() || st.settings[store.SettingVAPIDPrivate] == "" {
		t.Fatalf("keys not persisted: %v", st.settings)
	}
	if err := checkVAPID(st.settings[store.SettingVAPIDPublic], st.settings[store.SettingVAPIDPrivate]); err != nil {
		t.Fatal(err)
	}
	calls := st.setCalls
	n2, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if n2.PublicKey() != n1.PublicKey() || st.setCalls != calls {
		t.Fatalf("keys regenerated: %q vs %q, set calls %d -> %d", n1.PublicKey(), n2.PublicKey(), calls, st.setCalls)
	}
}

func TestVAPIDErrors(t *testing.T) {
	st := newFakeStore()
	st.settingEr = errors.New("db locked")
	if _, err := New(Options{Store: st}); err == nil || !strings.Contains(err.Error(), "db locked") {
		t.Fatalf("err = %v", err)
	}

	st = newFakeStore()
	pub, _, _ := generateVAPID()
	_, priv, _ := generateVAPID()
	st.settings[store.SettingVAPIDPublic], st.settings[store.SettingVAPIDPrivate] = pub, priv
	if _, err := New(Options{Store: st}); err == nil {
		t.Fatal("mismatched keys accepted")
	}

	if _, err := New(Options{}); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestSendTest(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_ = h.st.AddPushSub(ctx, store.PushSub{Endpoint: "https://push.example/2", DeviceID: "d2"})

	if err := h.n.SendTest(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := h.n.SendTest(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	got := h.sink.sends()
	if len(got) != 1 || got[0].sub.DeviceID != "d1" {
		t.Fatalf("sends %+v", got)
	}
	p := got[0].p
	if p.Title != "Test notification" || p.URL != "/#/settings" || p.Tag != "test" {
		t.Fatalf("payload %+v", p)
	}

	h.sink.errs["https://push.example/2"] = errors.New("boom")
	if err := h.n.SendTest(ctx, "d2"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want send error", err)
	}
	h.sink.errs["https://push.example/2"] = ErrGone
	if err := h.n.SendTest(ctx, "d2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after gone", err)
	}
	if subs, _ := h.st.PushSubs(ctx); len(subs) != 1 {
		t.Fatalf("gone subscription kept: %+v", subs)
	}
}
