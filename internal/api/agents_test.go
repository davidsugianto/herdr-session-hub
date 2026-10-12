package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

func TestSessions(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/api/sessions", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty sessions = %q", w.Body)
	}
	e.eng.sessions = []model.SessionInfo{{Name: "labs", Label: "Labs", Online: true, Agents: 2}}
	got := decode[[]model.SessionInfo](t, e.do("GET", "/api/sessions", ""))
	if len(got) != 1 || got[0].Name != "labs" || got[0].Agents != 2 {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestAgentsFilter(t *testing.T) {
	e := newEnv(t)
	e.eng.agents["labs/u"] = agent("labs/u", "labs", model.StateUnknown, true)
	e.eng.agents["work/w"] = agent("work/w", "work", model.StateWorking, true)
	ids := func(path string) []string {
		var out []string
		for _, a := range decode[[]model.Agent](t, e.do("GET", path, "")) {
			out = append(out, a.ID)
		}
		return out
	}
	has := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}
	if l := ids("/api/agents"); has(l, "labs/u") || !has(l, "labs/term_1") || !has(l, "work/w") {
		t.Fatalf("default list = %v", l)
	}
	if l := ids("/api/agents?include_unknown=1"); !has(l, "labs/u") {
		t.Fatalf("include_unknown list = %v", l)
	}
	if l := ids("/api/agents?session=work"); len(l) != 1 || l[0] != "work/w" {
		t.Fatalf("session filter = %v", l)
	}
	e.eng.agents = map[string]model.Agent{}
	if w := e.do("GET", "/api/agents", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty = %q", w.Body)
	}
}

func TestAgentIDForms(t *testing.T) {
	e := newEnv(t)
	e.act.output = "hello"
	for _, id := range []string{"labs/term_1", "labs%2Fterm_1", "labs%2fterm_1"} {
		t.Run(id, func(t *testing.T) {
			if a := decode[model.Agent](t, e.do("GET", "/api/agents/"+id, "")); a.ID != "labs/term_1" {
				t.Fatalf("detail id = %q", a.ID)
			}
			if o := decode[map[string]string](t, e.do("GET", "/api/agents/"+id+"/output", "")); o["text"] != "hello" {
				t.Fatalf("output = %v", o)
			}
			before := len(e.act.callList())
			if w := e.do("POST", "/api/agents/"+id+"/approve", `{"prompt_hash":"h"}`); w.Code != 200 {
				t.Fatalf("approve = %d %s", w.Code, w.Body)
			}
			calls := e.act.callList()
			if len(calls) != before+1 || calls[before] != "approve labs/term_1 h" {
				t.Fatalf("calls = %v", calls)
			}
		})
	}
	for _, p := range []string{"/api/agents/labs/nope", "/api/agents/nope", "/api/agents/labs%2Fnope/output"} {
		if w := e.do("GET", p, ""); w.Code != 404 || errCode(t, w) != "agent_not_found" {
			t.Errorf("GET %s = %d %s", p, w.Code, w.Body)
		}
	}
	if w := e.do("GET", "/api/agents/", ""); w.Code != 404 {
		t.Errorf("GET /api/agents/ = %d", w.Code)
	}
	if w := e.do("POST", "/api/agents/labs/term_1", `{}`); w.Code != 404 {
		t.Errorf("POST without verb = %d", w.Code)
	}
	if w := e.do("POST", "/api/agents/labs/term_1/explode", `{}`); w.Code != 404 {
		t.Errorf("POST unknown verb = %d", w.Code)
	}
}

func TestAgentDetail(t *testing.T) {
	e := newEnv(t)
	e.act.output = "one\n\n  \ntwo\nthree\nfour\nfive\nsix\nseven   \n\n"
	e.act.prompt = actions.PromptView{Profile: "claude", Verified: true, Prompt: map[string]any{"hash": "abc"}}
	type detail struct {
		model.Agent
		LastLines []string `json:"last_lines"`
		Prompt    *struct {
			Profile  string `json:"profile"`
			Verified bool   `json:"verified"`
		} `json:"prompt"`
	}
	d := decode[detail](t, e.do("GET", "/api/agents/labs/term_1", ""))
	if want := []string{"three", "four", "five", "six", "seven"}; !reflect.DeepEqual(d.LastLines, want) {
		t.Fatalf("last_lines = %q, want %q", d.LastLines, want)
	}
	if d.Prompt == nil || d.Prompt.Profile != "claude" || !d.Prompt.Verified {
		t.Fatalf("prompt = %+v", d.Prompt)
	}

	// Non-blocked: no prompt.
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateWorking, true)
	if d := decode[detail](t, e.do("GET", "/api/agents/labs/term_1", "")); d.Prompt != nil || len(d.LastLines) == 0 {
		t.Fatalf("working detail = %+v", d)
	}

	// Failures are non-fatal and omit the fields.
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateBlocked, true)
	e.act.outErr, e.act.prErr = errors.New("boom"), errors.New("boom")
	w := e.do("GET", "/api/agents/labs/term_1", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "last_lines") || strings.Contains(w.Body.String(), `"prompt"`) {
		t.Fatalf("failing detail = %d %s", w.Code, w.Body)
	}

	// Offline agent: detail still served, no output fetched.
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateBlocked, false)
	e.act.outErr, e.act.prErr = nil, nil
	if w := e.do("GET", "/api/agents/labs/term_1", ""); w.Code != 200 || strings.Contains(w.Body.String(), "last_lines") {
		t.Fatalf("offline detail = %d %s", w.Code, w.Body)
	}
}

func TestOutput(t *testing.T) {
	e := newEnv(t)
	e.act.output = "line"
	if w := e.do("GET", "/api/agents/labs/term_1/output", ""); w.Code != 200 || e.act.lastN != 300 {
		t.Fatalf("default = %d lines=%d", w.Code, e.act.lastN)
	}
	if w := e.do("GET", "/api/agents/labs/term_1/output?lines=42", ""); w.Code != 200 || e.act.lastN != 42 {
		t.Fatalf("lines=42 → %d", e.act.lastN)
	}
	for _, v := range []string{"0", "-1", "abc", "999999"} {
		if w := e.do("GET", "/api/agents/labs/term_1/output?lines="+v, ""); w.Code != 400 {
			t.Errorf("lines=%s = %d", v, w.Code)
		}
	}
	e.act.outErr = actions.ErrSessionOffline
	if w := e.do("GET", "/api/agents/labs/term_1/output", ""); w.Code != 503 || errCode(t, w) != "session_offline" {
		t.Fatalf("offline err = %d %s", w.Code, w.Body)
	}
	e.act.outErr = nil
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateBlocked, false)
	if w := e.do("GET", "/api/agents/labs/term_1/output", ""); w.Code != 503 {
		t.Fatalf("offline agent = %d", w.Code)
	}
}

func TestTimeline(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	e.st.events = []store.Event{{ID: 1, AgentID: "labs/term_1", To: "blocked"}}
	cases := []struct {
		q    string
		want time.Duration
		code int
	}{
		{"", 2 * time.Hour, 200},
		{"?since=30m", 30 * time.Minute, 200},
		{"?since=3d", 72 * time.Hour, 200},
		{"?since=14d", 14 * 24 * time.Hour, 200},
		{"?since=90d", 14 * 24 * time.Hour, 200},
		{"?since=5000h", 14 * 24 * time.Hour, 200},
		{"?since=abc", 0, 400},
		{"?since=0s", 0, 400},
		{"?since=-1h", 0, 400},
		{"?since=xd", 0, 400},
	}
	for _, c := range cases {
		w := e.do("GET", "/api/timeline"+c.q, "")
		if w.Code != c.code {
			t.Errorf("%q = %d, want %d", c.q, w.Code, c.code)
			continue
		}
		if c.code == 200 {
			if got := now.Sub(e.st.evSince); got != c.want {
				t.Errorf("%q since = %v ago, want %v", c.q, got, c.want)
			}
		}
	}
	w := e.do("GET", "/api/timeline?session=labs", "")
	if e.st.evSession != "labs" {
		t.Fatalf("session = %q", e.st.evSession)
	}
	if got := decode[[]store.Event](t, w); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("events = %+v", got)
	}
	e.st.events = nil
	if w := e.do("GET", "/api/timeline", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty timeline = %q", w.Body)
	}
}

// ---- action POSTs ----

func TestActionRoutes(t *testing.T) {
	cases := []struct {
		verb, body, wantCall, wantPayload string
	}{
		{"approve", `{"prompt_hash":"h1"}`, "approve labs/term_1 h1", `{"prompt_hash":"h1"}`},
		{"trust", `{"prompt_hash":"h2"}`, "trust labs/term_1 h2", `{"prompt_hash":"h2"}`},
		{"deny", `{"prompt_hash":"h3","note":"too risky"}`, `deny labs/term_1 h3 "too risky"`, `{"prompt_hash":"h3","note":"too risky"}`},
		{"reply", `{"text":"yes please"}`, `reply labs/term_1 "yes please"`, `{"text":"yes please"}`},
		{"send", `{"text":"ls","enter":true}`, `send labs/term_1 "ls" true`, `{"text":"ls","enter":true}`},
		{"send", `{"text":"","enter":true}`, `send labs/term_1 "" true`, `{"text":"","enter":true}`},
		{"send", `{"text":"abc"}`, `send labs/term_1 "abc" false`, `{"text":"abc","enter":false}`},
		{"keys", `{"keys":["esc","ctrl+c"]}`, "keys labs/term_1 esc,ctrl+c", `{"keys":["esc","ctrl+c"]}`},
		{"interrupt", ``, "interrupt labs/term_1 false", `{"force":false}`},
		{"interrupt", `{}`, "interrupt labs/term_1 false", `{"force":false}`},
		{"interrupt", `{"force":true}`, "interrupt labs/term_1 true", `{"force":true}`},
	}
	for _, c := range cases {
		t.Run(c.verb+"/"+c.body, func(t *testing.T) {
			e := newEnv(t)
			w := e.do("POST", "/api/agents/labs/term_1/"+c.verb, c.body)
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
				t.Fatalf("status = %d %s", w.Code, w.Body)
			}
			if calls := e.act.callList(); len(calls) != 1 || calls[0] != c.wantCall {
				t.Fatalf("calls = %q, want [%q]", calls, c.wantCall)
			}
			rows := e.st.auditRows()
			if len(rows) != 1 {
				t.Fatalf("audit rows = %d", len(rows))
			}
			r := rows[0]
			if r.Actor != "dev-1" || r.Surface != "api" || r.AgentID != "labs/term_1" || r.Action != c.verb ||
				r.Result != "ok" || r.Payload != c.wantPayload || !r.At.Equal(e.clock.Now()) {
				t.Fatalf("audit = %+v", r)
			}
		})
	}
}

func TestSeenRoute(t *testing.T) {
	e := newEnv(t)
	if w := e.do("POST", "/api/agents/labs%2Fterm_1/seen", ""); w.Code != 200 {
		t.Fatalf("seen = %d %s", w.Code, w.Body)
	}
	if got := e.eng.seenIDs(); len(got) != 1 || got[0] != "labs/term_1" {
		t.Fatalf("seen = %v", got)
	}
	if len(e.act.callList()) != 0 {
		t.Fatalf("seen must not call actions: %v", e.act.callList())
	}
	rows := e.st.auditRows()
	if len(rows) != 1 || rows[0].Action != "seen" || rows[0].Result != "ok" {
		t.Fatalf("audit = %+v", rows)
	}
	// seen works for an offline agent (engine-local) but not an unknown one.
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateFinished, false)
	if w := e.do("POST", "/api/agents/labs/term_1/seen", ""); w.Code != 200 {
		t.Fatalf("offline seen = %d", w.Code)
	}
	if w := e.do("POST", "/api/agents/labs/ghost/seen", ""); w.Code != 404 {
		t.Fatalf("unknown seen = %d", w.Code)
	}
}

func TestActionFailureIsAudited(t *testing.T) {
	e := newEnv(t)
	e.act.err = actions.ErrPromptChanged
	w := e.do("POST", "/api/agents/labs/term_1/reply", `{"text":"my secret plan"}`)
	if w.Code != 409 || errCode(t, w) != "prompt_changed" {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	rows := e.st.auditRows()
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	r := rows[0]
	if r.Result != actions.ErrPromptChanged.Error() || r.Action != "reply" || r.Actor != "dev-1" || !strings.Contains(r.Payload, "my secret plan") {
		t.Fatalf("audit = %+v", r)
	}
}

func TestActionErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{actions.ErrNotBlocked, 409, "not_blocked"},
		{actions.ErrPromptChanged, 409, "prompt_changed"},
		{actions.ErrNoPrompt, 409, "no_prompt"},
		{actions.ErrActionNotAvail, 422, "action_unavailable"},
		{actions.ErrAgentBlocked, 409, "agent_blocked"},
		{actions.ErrAgentGone, 404, "agent_not_found"},
		{actions.ErrSessionOffline, 503, "session_offline"},
		{fmt.Errorf("%w: invalid key", actions.ErrInvalidInput), 400, "bad_request"},
		{fmt.Errorf("wrapped: %w", actions.ErrNotBlocked), 409, "not_blocked"},
		{context.DeadlineExceeded, 504, "timeout"},
		{errors.New("herdr exploded"), 502, "action_failed"},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			e := newEnv(t)
			e.act.err = c.err
			w := e.do("POST", "/api/agents/labs/term_1/approve", `{"prompt_hash":"h"}`)
			if w.Code != c.status || errCode(t, w) != c.code {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, c.status, c.code)
			}
			if rows := e.st.auditRows(); len(rows) != 1 || rows[0].Result != c.err.Error() {
				t.Fatalf("audit = %+v", rows)
			}
		})
	}
}

func TestOfflineAgentRejectedBeforeActions(t *testing.T) {
	e := newEnv(t)
	e.eng.agents["labs/term_1"] = agent("labs/term_1", "labs", model.StateBlocked, false)
	for _, c := range []struct{ verb, body string }{
		{"approve", `{"prompt_hash":"h"}`}, {"trust", `{"prompt_hash":"h"}`}, {"deny", `{"prompt_hash":"h"}`},
		{"reply", `{"text":"x"}`}, {"send", `{"text":"x"}`}, {"keys", `{"keys":["esc"]}`}, {"interrupt", ``},
	} {
		w := e.do("POST", "/api/agents/labs/term_1/"+c.verb, c.body)
		if w.Code != 503 || errCode(t, w) != "session_offline" {
			t.Errorf("%s = %d %s", c.verb, w.Code, w.Body)
		}
	}
	if calls := e.act.callList(); len(calls) != 0 {
		t.Fatalf("actions called for offline agent: %v", calls)
	}
	if rows := e.st.auditRows(); len(rows) != 7 || rows[0].Result == "ok" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestUnknownAgentAction(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/agents/labs/ghost/approve", `{"prompt_hash":"h"}`)
	if w.Code != 404 || errCode(t, w) != "agent_not_found" {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	if len(e.act.callList()) != 0 {
		t.Fatal("actions called for unknown agent")
	}
}

func TestBodyValidation(t *testing.T) {
	big := `{"text":"` + strings.Repeat("a", 64<<10) + `"}`
	cases := []struct {
		name, verb, body string
		status           int
		code             string
	}{
		{"unknown field", "approve", `{"prompt_hash":"h","extra":1}`, 400, "bad_request"},
		{"note on approve", "approve", `{"prompt_hash":"h","note":"x"}`, 400, "bad_request"},
		{"missing hash", "approve", `{}`, 400, "bad_request"},
		{"empty body", "approve", ``, 400, "bad_request"},
		{"malformed", "reply", `{"text":`, 400, "bad_request"},
		{"not an object", "reply", `[1]`, 400, "bad_request"},
		{"wrong type", "reply", `{"text":5}`, 400, "bad_request"},
		{"trailing data", "reply", `{"text":"a"}{"text":"b"}`, 400, "bad_request"},
		{"blank reply", "reply", `{"text":"  "}`, 400, "bad_request"},
		{"empty send", "send", `{"text":""}`, 400, "bad_request"},
		{"no keys", "keys", `{"keys":[]}`, 400, "bad_request"},
		{"blank key", "keys", `{"keys":["esc",""]}`, 400, "bad_request"},
		{"too many keys", "keys", `{"keys":[` + strings.Repeat(`"a",`, 64) + `"a"]}`, 400, "bad_request"},
		{"unknown field on interrupt", "interrupt", `{"hard":true}`, 400, "bad_request"},
		{"unknown field on seen", "seen", `{"x":1}`, 400, "bad_request"},
		{"too large", "reply", big, 413, "body_too_large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.do("POST", "/api/agents/labs/term_1/"+c.verb, c.body)
			if w.Code != c.status || errCode(t, w) != c.code {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, c.status, c.code)
			}
			if len(e.act.callList()) != 0 || len(e.eng.seenIDs()) != 0 {
				t.Fatal("action performed despite bad body")
			}
		})
	}
	// A body just under the limit is fine.
	e := newEnv(t)
	ok := `{"text":"` + strings.Repeat("a", 64<<10-64) + `"}`
	if w := e.do("POST", "/api/agents/labs/term_1/reply", ok); w.Code != 200 {
		t.Fatalf("near-limit body = %d %s", w.Code, w.Body.String()[:min(200, w.Body.Len())])
	}
}

func TestAuditWriteFailureDoesNotFailAction(t *testing.T) {
	e := newEnv(t)
	e.st.auditErr = errors.New("disk full")
	if w := e.do("POST", "/api/agents/labs/term_1/approve", `{"prompt_hash":"h"}`); w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestActionRateLimit(t *testing.T) {
	e := newEnv(t)
	post := func(token string) *httptest.ResponseRecorder {
		return e.serve(req("POST", "/api/agents/labs/term_1/seen", "", "Authorization", "Bearer "+token))
	}
	for i := 0; i < 30; i++ {
		if w := post("tok-1"); w.Code != 200 {
			t.Fatalf("request %d = %d", i, w.Code)
		}
	}
	w := post("tok-1")
	if w.Code != 429 || errCode(t, w) != "rate_limited" {
		t.Fatalf("31st = %d %s", w.Code, w.Body)
	}
	if ra := w.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("Retry-After = %q", ra)
	}
	if n := len(e.st.auditRows()); n != 30 {
		t.Fatalf("limited request must not be audited, rows = %d", n)
	}
	if w := post("tok-2"); w.Code != 200 {
		t.Fatalf("other device = %d", w.Code)
	}
	// GETs are not limited.
	if w := e.do("GET", "/api/agents", ""); w.Code != 200 {
		t.Fatalf("GET while limited = %d", w.Code)
	}
	e.clock.Advance(500 * time.Millisecond) // 2/s → one token
	if w := post("tok-1"); w.Code != 200 {
		t.Fatalf("after refill = %d", w.Code)
	}
	if w := post("tok-1"); w.Code != 429 {
		t.Fatalf("immediately after = %d", w.Code)
	}
}
