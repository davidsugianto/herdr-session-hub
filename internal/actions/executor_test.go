package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/davidsugianto/herdr-session-hub/internal/herdr"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/profiles"
)

// ---- fakes ---------------------------------------------------------------

type call struct {
	socket, method string
	params         json.RawMessage
}

type fakeCaller struct {
	calls []call
	// handler returns the result (marshalled into result) or an error.
	handler func(method string, params json.RawMessage) (any, error)
}

func (f *fakeCaller) Call(_ context.Context, socket, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	f.calls = append(f.calls, call{socket, method, raw})
	res, err := f.handler(method, raw)
	if err != nil {
		return err
	}
	if result != nil && res != nil {
		b, _ := json.Marshal(res)
		return json.Unmarshal(b, result)
	}
	return nil
}

func (f *fakeCaller) count(method string) int {
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *fakeCaller) sent() bool {
	return f.count(methodPaneSendKeys)+f.count(methodPaneSendText)+f.count(methodAgentPrompt) > 0
}

func (f *fakeCaller) callsTo(method string) []call {
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

type fakeSockets map[string]string

func (s fakeSockets) Socket(name string) (string, bool) { p, ok := s[name]; return p, ok }

type fakeProfile struct {
	name     string
	verified bool
	prompt   profiles.Prompt
	parseOK  bool
	keys     map[profiles.Action][]string
	gotScr   string
}

func (p *fakeProfile) Name() string   { return p.name }
func (p *fakeProfile) Verified() bool { return p.verified }
func (p *fakeProfile) Parse(screen string) (profiles.Prompt, bool) {
	p.gotScr = screen
	return p.prompt, p.parseOK
}
func (p *fakeProfile) Keys(a profiles.Action, _ profiles.Prompt) ([]string, bool) {
	k, ok := p.keys[a]
	return k, ok
}

type fakeProvider struct{ p *fakeProfile }

func (f fakeProvider) For(string) profiles.Profile { return f.p }

// ---- helpers -------------------------------------------------------------

var testAgent = model.Agent{
	ID: "s1/t1", Session: "s1", TerminalID: "t1", PaneID: "p1", Name: "claude",
}

func agentInfo(term, pane string, st herdr.AgentStatus) herdr.AgentInfo {
	return herdr.AgentInfo{TerminalID: term, PaneID: pane, AgentStatus: st}
}

// std returns a handler: agent.get returns info, pane.read returns screen,
// sends succeed.
func std(info herdr.AgentInfo, screen string) func(string, json.RawMessage) (any, error) {
	return func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case herdr.MethodAgentGet:
			return herdr.AgentGetResult{Type: "agent_info", Agent: info}, nil
		case herdr.MethodAgentList:
			return herdr.AgentListResult{Type: "agent_list", Agents: []herdr.AgentInfo{info}}, nil
		case herdr.MethodPaneRead:
			return herdr.PaneReadResult{Type: "pane_read", Read: herdr.PaneReadContent{Text: screen}}, nil
		}
		return map[string]string{"type": "ok"}, nil
	}
}

func newExec(fc *fakeCaller, prof *fakeProfile) *Executor {
	return &Executor{
		sockets:  fakeSockets{"s1": "/sock/s1"},
		profiles: fakeProvider{prof},
		c:        fc,
	}
}

func claudeProfile() *fakeProfile {
	return &fakeProfile{
		name: "claude", verified: true, parseOK: true,
		prompt: profiles.Prompt{Text: "Allow?", Options: []string{"Yes", "No"}, Hash: "h1"},
		keys: map[profiles.Action][]string{
			profiles.ActionApprove: {"1"},
			profiles.ActionTrust:   {"2"},
			profiles.ActionDeny:    {"3"},
		},
	}
}

func keysOf(t *testing.T, c call) []string {
	t.Helper()
	var p paneSendKeysParams
	if err := json.Unmarshal(c.params, &p); err != nil {
		t.Fatal(err)
	}
	return p.Keys
}

// ---- approve / trust / deny ----------------------------------------------

func TestApproveHappyPath(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "screen")}
	prof := claudeProfile()
	e := newExec(fc, prof)
	if err := e.Approve(context.Background(), testAgent, "h1"); err != nil {
		t.Fatal(err)
	}
	sends := fc.callsTo(methodPaneSendKeys)
	if len(sends) != 1 {
		t.Fatalf("want 1 send_keys, got %d", len(sends))
	}
	if got := keysOf(t, sends[0]); len(got) != 1 || got[0] != "1" {
		t.Fatalf("keys = %v", got)
	}
	var p paneSendKeysParams
	_ = json.Unmarshal(sends[0].params, &p)
	if p.PaneID != "p1" || sends[0].socket != "/sock/s1" {
		t.Fatalf("target = %q on %q", p.PaneID, sends[0].socket)
	}
	if prof.gotScr != "screen" {
		t.Fatalf("profile parsed %q", prof.gotScr)
	}
	// pane.read must use recent_unwrapped.
	rd := fc.callsTo(herdr.MethodPaneRead)
	if len(rd) != 1 || !strings.Contains(string(rd[0].params), `"recent_unwrapped"`) {
		t.Fatalf("pane.read params: %v", rd)
	}
}

func TestTrustHappyPath(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
	e := newExec(fc, claudeProfile())
	if err := e.Trust(context.Background(), testAgent, "h1"); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, fc.callsTo(methodPaneSendKeys)[0]); got[0] != "2" {
		t.Fatalf("keys = %v", got)
	}
}

func TestGuards(t *testing.T) {
	tests := []struct {
		name   string
		status herdr.AgentStatus
		mutate func(*fakeProfile)
		hash   string
		want   error
	}{
		{"not blocked", herdr.AgentStatusWorking, nil, "h1", ErrNotBlocked},
		{"no prompt", herdr.AgentStatusBlocked, func(p *fakeProfile) { p.parseOK = false }, "h1", ErrNoPrompt},
		{"hash mismatch", herdr.AgentStatusBlocked, nil, "stale", ErrPromptChanged},
		{"no trust key", herdr.AgentStatusBlocked, func(p *fakeProfile) { delete(p.keys, profiles.ActionTrust) }, "h1", ErrActionNotAvail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{handler: std(agentInfo("t1", "p1", tc.status), "s")}
			prof := claudeProfile()
			if tc.mutate != nil {
				tc.mutate(prof)
			}
			e := newExec(fc, prof)
			var err error
			if tc.name == "no trust key" {
				err = e.Trust(context.Background(), testAgent, tc.hash)
			} else {
				err = e.Approve(context.Background(), testAgent, tc.hash)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if fc.sent() {
				t.Fatal("something was sent despite the guard")
			}
		})
	}
}

func TestNotBlockedDoesNotReadScreen(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "s")}
	e := newExec(fc, claudeProfile())
	_ = e.Deny(context.Background(), testAgent, "h1", "")
	if fc.count(herdr.MethodPaneRead) != 0 {
		t.Fatal("screen read for a non-blocked agent")
	}
}

func TestDenyWithNote(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
	e := newExec(fc, claudeProfile())
	if err := e.Deny(context.Background(), testAgent, "h1", "use go vet instead"); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, c := range fc.calls {
		switch c.method {
		case methodPaneSendKeys:
			seq = append(seq, "keys:"+strings.Join(keysOf(t, c), ","))
		case methodPaneSendText:
			var p paneSendTextParams
			_ = json.Unmarshal(c.params, &p)
			seq = append(seq, "text:"+p.Text)
		}
	}
	want := []string{"keys:3", "text:use go vet instead", "keys:enter"}
	if fmt.Sprint(seq) != fmt.Sprint(want) {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
}

func TestDenyWithoutNote(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
	e := newExec(fc, claudeProfile())
	if err := e.Deny(context.Background(), testAgent, "h1", ""); err != nil {
		t.Fatal(err)
	}
	if fc.count(methodPaneSendKeys) != 1 || fc.count(methodPaneSendText) != 0 {
		t.Fatalf("calls: %+v", fc.calls)
	}
}

func TestDenyNoteCancelledDuringDelay(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
	e := newExec(fc, claudeProfile())
	e.denyDelay = 10 * 1e9
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.Deny(ctx, testAgent, "h1", "note")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if fc.count(methodPaneSendText) != 0 {
		t.Fatal("note sent after cancel")
	}
}

// ---- reply ----------------------------------------------------------------

func TestReply(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
	e := newExec(fc, claudeProfile())
	if err := e.Reply(context.Background(), testAgent, "hello"); err != nil {
		t.Fatal(err)
	}
	c := fc.callsTo(methodAgentPrompt)
	if len(c) != 1 || string(c[0].params) != `{"target":"p1","text":"hello"}` {
		t.Fatalf("calls: %+v", c)
	}
}

func TestReplyErrors(t *testing.T) {
	tests := []struct {
		name string
		code string
		want error
	}{
		{"blocked", "agent_blocked", ErrAgentBlocked},
		{"gone", "agent_not_found", ErrAgentGone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "")
			fc := &fakeCaller{handler: func(m string, p json.RawMessage) (any, error) {
				if m == methodAgentPrompt {
					return nil, &herdr.Error{Code: tc.code, Message: "x"}
				}
				return base(m, p)
			}}
			err := newExec(fc, claudeProfile()).Reply(context.Background(), testAgent, "hi")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v", err)
			}
			var he *herdr.Error
			if !errors.As(err, &he) || he.Code != tc.code {
				t.Fatalf("herdr error not wrapped: %v", err)
			}
		})
	}
}

func TestReplyOtherErrorWrapped(t *testing.T) {
	base := std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")
	fc := &fakeCaller{handler: func(m string, p json.RawMessage) (any, error) {
		if m == methodAgentPrompt {
			return nil, &herdr.Error{Code: "weird", Message: "m"}
		}
		return base(m, p)
	}}
	err := newExec(fc, claudeProfile()).Reply(context.Background(), testAgent, "hi")
	var he *herdr.Error
	if !errors.As(err, &he) || he.Code != "weird" {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyEmpty(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
	for _, s := range []string{"", "  \n"} {
		if err := newExec(fc, claudeProfile()).Reply(context.Background(), testAgent, s); err == nil {
			t.Fatalf("empty reply %q accepted", s)
		}
	}
	if len(fc.calls) != 0 {
		t.Fatal("herdr contacted for empty reply")
	}
}

// ---- send / keys / interrupt ---------------------------------------------

func TestSend(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
	e := newExec(fc, claudeProfile())
	if err := e.Send(context.Background(), testAgent, "ls", true); err != nil {
		t.Fatal(err)
	}
	if fc.count(methodPaneSendText) != 1 {
		t.Fatalf("calls: %+v", fc.calls)
	}
	ks := fc.callsTo(methodPaneSendKeys)
	if len(ks) != 1 || keysOf(t, ks[0])[0] != "enter" {
		t.Fatalf("keys: %+v", ks)
	}

	fc2 := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
	if err := newExec(fc2, claudeProfile()).Send(context.Background(), testAgent, "ls", false); err != nil {
		t.Fatal(err)
	}
	if fc2.count(methodPaneSendKeys) != 0 {
		t.Fatal("enter sent when enter=false")
	}
	if err := newExec(fc2, claudeProfile()).Send(context.Background(), testAgent, "", false); err == nil {
		t.Fatal("empty send accepted")
	}
}

func TestKeysValidation(t *testing.T) {
	many := make([]string, 21)
	for i := range many {
		many[i] = "a"
	}
	ok20 := many[:20]
	tests := []struct {
		name string
		keys []string
		ok   bool
	}{
		{"nil", nil, false},
		{"empty slice", []string{}, false},
		{"single", []string{"enter"}, true},
		{"combo", []string{"ctrl+c", "esc", "shift-tab", "f_1"}, true},
		{"20 keys", ok20, true},
		{"21 keys", many, false},
		{"empty key", []string{"a", ""}, false},
		{"space", []string{"a b"}, false},
		{"newline", []string{"a\n"}, false},
		{"quote", []string{`a"`}, false},
		{"unicode", []string{"é"}, false},
		{"32 chars", []string{strings.Repeat("a", 32)}, true},
		{"33 chars", []string{strings.Repeat("a", 33)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
			err := newExec(fc, claudeProfile()).Keys(context.Background(), testAgent, tc.keys)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if !tc.ok && len(fc.calls) != 0 {
				t.Fatal("herdr contacted for invalid keys")
			}
			if tc.ok && fc.count(methodPaneSendKeys) != 1 {
				t.Fatal("keys not sent")
			}
		})
	}
}

func TestInterrupt(t *testing.T) {
	prof := claudeProfile()
	prof.keys[profiles.ActionInterrupt] = []string{"escape"}
	// force falls back to ctrl+c as the profile lacks force_interrupt.
	for _, tc := range []struct {
		force bool
		want  string
	}{{false, "escape"}, {true, "ctrl+c"}} {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusWorking), "")}
		if err := newExec(fc, prof).Interrupt(context.Background(), testAgent, tc.force); err != nil {
			t.Fatal(err)
		}
		if got := keysOf(t, fc.callsTo(methodPaneSendKeys)[0]); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("force=%v keys=%v want %s", tc.force, got, tc.want)
		}
	}
	// no profile keys at all -> esc.
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusWorking), "")}
	bare := &fakeProfile{name: "generic"}
	if err := newExec(fc, bare).Interrupt(context.Background(), testAgent, false); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, fc.callsTo(methodPaneSendKeys)[0]); got[0] != "esc" {
		t.Fatalf("keys = %v", got)
	}
}

// ---- resolution ------------------------------------------------------------

func TestMovedPaneReResolved(t *testing.T) {
	moved := agentInfo("t1", "p9", herdr.AgentStatusBlocked)
	fc := &fakeCaller{handler: func(m string, p json.RawMessage) (any, error) {
		if m == herdr.MethodAgentGet {
			// p1 is now another terminal.
			return herdr.AgentGetResult{Agent: agentInfo("other", "p1", herdr.AgentStatusIdle)}, nil
		}
		return std(moved, "s")(m, p)
	}}
	if err := newExec(fc, claudeProfile()).Approve(context.Background(), testAgent, "h1"); err != nil {
		t.Fatal(err)
	}
	if fc.count(herdr.MethodAgentList) != 1 {
		t.Fatal("agent.list not consulted")
	}
	var p paneSendKeysParams
	_ = json.Unmarshal(fc.callsTo(methodPaneSendKeys)[0].params, &p)
	if p.PaneID != "p9" {
		t.Fatalf("keys sent to %q, want p9", p.PaneID)
	}
	var rp herdr.PaneReadParams
	_ = json.Unmarshal(fc.callsTo(herdr.MethodPaneRead)[0].params, &rp)
	if rp.PaneID != "p9" {
		t.Fatalf("read from %q, want p9", rp.PaneID)
	}
}

func TestMovedPaneGetNotFound(t *testing.T) {
	moved := agentInfo("t1", "p9", herdr.AgentStatusBlocked)
	fc := &fakeCaller{handler: func(m string, p json.RawMessage) (any, error) {
		if m == herdr.MethodAgentGet {
			return nil, &herdr.Error{Code: "agent_not_found", Message: "no"}
		}
		return std(moved, "s")(m, p)
	}}
	if err := newExec(fc, claudeProfile()).Approve(context.Background(), testAgent, "h1"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalMismatchIsAgentGone(t *testing.T) {
	fc := &fakeCaller{handler: func(m string, _ json.RawMessage) (any, error) {
		switch m {
		case herdr.MethodAgentGet:
			return herdr.AgentGetResult{Agent: agentInfo("other", "p1", herdr.AgentStatusBlocked)}, nil
		case herdr.MethodAgentList:
			return herdr.AgentListResult{Agents: []herdr.AgentInfo{agentInfo("other", "p1", herdr.AgentStatusBlocked)}}, nil
		}
		return nil, nil
	}}
	for name, fn := range map[string]func(*Executor) error{
		"approve": func(e *Executor) error { return e.Approve(context.Background(), testAgent, "h1") },
		"reply":   func(e *Executor) error { return e.Reply(context.Background(), testAgent, "hi") },
		"keys":    func(e *Executor) error { return e.Keys(context.Background(), testAgent, []string{"esc"}) },
		"send":    func(e *Executor) error { return e.Send(context.Background(), testAgent, "x", true) },
		"int":     func(e *Executor) error { return e.Interrupt(context.Background(), testAgent, false) },
	} {
		fc.calls = nil
		if err := fn(newExec(fc, claudeProfile())); !errors.Is(err, ErrAgentGone) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if fc.sent() {
			t.Fatalf("%s: sent to a mismatched terminal", name)
		}
	}
}

func TestOfflineSession(t *testing.T) {
	t.Run("unknown session", func(t *testing.T) {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "")}
		e := newExec(fc, claudeProfile())
		a := testAgent
		a.Session = "nope"
		if err := e.Approve(context.Background(), a, "h1"); !errors.Is(err, ErrSessionOffline) {
			t.Fatalf("err = %v", err)
		}
		if len(fc.calls) != 0 {
			t.Fatal("dialled an unknown session")
		}
	})
	t.Run("dial fails", func(t *testing.T) {
		fc := &fakeCaller{handler: func(string, json.RawMessage) (any, error) {
			return nil, fmt.Errorf("herdr: dial /x: %w", &net.OpError{Op: "dial", Net: "unix", Err: errors.New("connection refused")})
		}}
		e := newExec(fc, claudeProfile())
		if err := e.Reply(context.Background(), testAgent, "hi"); !errors.Is(err, ErrSessionOffline) {
			t.Fatalf("err = %v", err)
		}
		if _, err := e.Output(context.Background(), testAgent, 10); !errors.Is(err, ErrSessionOffline) {
			t.Fatalf("output err = %v", err)
		}
	})
}

// ---- output / prompt ------------------------------------------------------

func TestOutputClamping(t *testing.T) {
	tests := []struct {
		in   int
		want uint32
	}{{0, 300}, {-5, 300}, {1, 1}, {500, 500}, {2000, 2000}, {99999, 2000}}
	for _, tc := range tests {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "x")}
		if _, err := newExec(fc, claudeProfile()).Output(context.Background(), testAgent, tc.in); err != nil {
			t.Fatal(err)
		}
		var rp herdr.PaneReadParams
		_ = json.Unmarshal(fc.callsTo(herdr.MethodPaneRead)[0].params, &rp)
		if rp.Lines == nil || *rp.Lines != tc.want {
			t.Fatalf("lines(%d) = %v, want %d", tc.in, rp.Lines, tc.want)
		}
		if rp.Source != herdr.ReadSourceRecentUnwrapped {
			t.Fatalf("source = %q", rp.Source)
		}
	}
}

func TestOutputStripsANSI(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "\x1b[31mred\x1b[0m \x1b]0;title\x07plain")}
	out, err := newExec(fc, claudeProfile()).Output(context.Background(), testAgent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if out != "red plain" {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentPrompt(t *testing.T) {
	t.Run("not blocked", func(t *testing.T) {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusWorking), "")}
		v, err := newExec(fc, claudeProfile()).CurrentPrompt(context.Background(), testAgent)
		if err != nil {
			t.Fatal(err)
		}
		if v.Prompt != nil || v.Profile != "claude" || !v.Verified {
			t.Fatalf("view = %+v", v)
		}
		if fc.count(herdr.MethodPaneRead) != 0 {
			t.Fatal("screen read while not blocked")
		}
	})
	t.Run("blocked", func(t *testing.T) {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
		v, err := newExec(fc, claudeProfile()).CurrentPrompt(context.Background(), testAgent)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := v.Prompt.(profiles.Prompt)
		if !ok || p.Hash != "h1" {
			t.Fatalf("view = %+v", v)
		}
	})
	t.Run("blocked unparsed", func(t *testing.T) {
		fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "s")}
		prof := claudeProfile()
		prof.parseOK = false
		v, err := newExec(fc, prof).CurrentPrompt(context.Background(), testAgent)
		if err != nil || v.Prompt != nil {
			t.Fatalf("view=%+v err=%v", v, err)
		}
	})
}

// recordingProvider remembers which agent key the executor asked a profile for.
type recordingProvider struct {
	p    *fakeProfile
	keys []string
}

func (r *recordingProvider) For(agent string) profiles.Profile {
	r.keys = append(r.keys, agent)
	return r.p
}

// The profile must be chosen by agent kind (claude, pi, omp), not by the
// user-assigned agent name.
func TestProfileLookupUsesKindNotName(t *testing.T) {
	prof := &fakeProfile{name: "claude", prompt: profiles.Prompt{Hash: "h"}, parseOK: true,
		keys: map[profiles.Action][]string{profiles.ActionApprove: {"1", "enter"}}}
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusBlocked), "screen")}
	e := newExec(fc, prof)
	rec := &recordingProvider{p: prof}
	e.profiles = rec

	a := testAgent
	a.Name, a.Kind = "named-worker", "claude"
	if err := e.Approve(context.Background(), a, "h"); err != nil {
		t.Fatal(err)
	}
	if len(rec.keys) == 0 || rec.keys[0] != "claude" {
		t.Fatalf("profile requested for %v, want [claude]", rec.keys)
	}

	// No kind known: fall back to the name.
	rec.keys = nil
	a.Kind = ""
	a.Name = "pi"
	if _, err := e.CurrentPrompt(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(rec.keys) == 0 || rec.keys[0] != "pi" {
		t.Fatalf("fallback profile requested for %v, want [pi]", rec.keys)
	}
}

func TestEmptyReplyAndSendAreInvalidInput(t *testing.T) {
	fc := &fakeCaller{handler: std(agentInfo("t1", "p1", herdr.AgentStatusIdle), "")}
	e := newExec(fc, claudeProfile())
	if err := e.Reply(context.Background(), testAgent, "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Reply err = %v, want ErrInvalidInput", err)
	}
	if err := e.Send(context.Background(), testAgent, "", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Send err = %v, want ErrInvalidInput", err)
	}
}
