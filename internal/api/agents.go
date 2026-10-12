package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	defaultOutputLines = 300
	maxOutputLines     = 5000
	detailReadLines    = 60
	detailLastLines    = 5
	defaultSince       = 2 * time.Hour
	maxSince           = 14 * 24 * time.Hour
	maxKeys            = 64
	readTimeout        = 10 * time.Second
)

func filterAgents(in []model.Agent, includeUnknown bool) []model.Agent {
	out := make([]model.Agent, 0, len(in))
	for _, a := range in {
		if a.State == model.StateUnknown && !includeUnknown {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (s *server) handleSessions(w http.ResponseWriter, r *http.Request) {
	out := s.eng.Sessions()
	if out == nil {
		out = []model.SessionInfo{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleAgents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, filterAgents(s.eng.Agents(q.Get("session")), q.Get("include_unknown") == "1"))
}

// agentRoute splits the raw path after /api/agents/ into an agent id (which
// contains a slash, sent either raw or as %2F) and an optional trailing verb.
func agentRoute(r *http.Request, verbs map[string]bool) (id, verb string, e *apiErr) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/api/agents/")
	if i := strings.LastIndex(rest, "/"); i >= 0 && verbs[rest[i+1:]] {
		verb, rest = rest[i+1:], rest[:i]
	}
	id, err := url.PathUnescape(rest)
	if err != nil {
		return "", "", newErr(http.StatusBadRequest, "bad_request", "invalid agent id encoding")
	}
	if id == "" {
		return "", "", newErr(http.StatusNotFound, "not_found", "no such route")
	}
	return id, verb, nil
}

var getVerbs = map[string]bool{"output": true}

var postVerbs = map[string]bool{
	"approve": true, "trust": true, "deny": true, "reply": true,
	"send": true, "keys": true, "interrupt": true, "seen": true,
}

func (s *server) handleAgentGet(w http.ResponseWriter, r *http.Request) {
	id, verb, e := agentRoute(r, getVerbs)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	a, ok := s.eng.Agent(id)
	if !ok {
		writeAPIErr(w, newErr(http.StatusNotFound, "agent_not_found", "unknown agent"))
		return
	}
	if verb == "output" {
		s.handleOutput(w, r, a)
		return
	}
	s.handleAgentDetail(w, r, a)
}

type agentDetail struct {
	model.Agent
	LastLines []string            `json:"last_lines,omitempty"`
	Prompt    *actions.PromptView `json:"prompt,omitempty"`
}

func (s *server) handleAgentDetail(w http.ResponseWriter, r *http.Request, a model.Agent) {
	d := agentDetail{Agent: a}
	if a.Online {
		ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
		defer cancel()
		if text, err := s.act.Output(ctx, a, detailReadLines); err == nil {
			d.LastLines = lastNonEmpty(text, detailLastLines)
		} else {
			slog.Debug("api: detail output failed", "agent", a.ID, "err", err)
		}
		if a.State == model.StateBlocked {
			if pv, err := s.act.CurrentPrompt(ctx, a); err == nil {
				d.Prompt = &pv
			} else {
				slog.Debug("api: detail prompt failed", "agent", a.ID, "err", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, d)
}

func lastNonEmpty(text string, n int) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func (s *server) handleOutput(w http.ResponseWriter, r *http.Request, a model.Agent) {
	lines := defaultOutputLines
	if v := r.URL.Query().Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxOutputLines {
			writeAPIErr(w, newErr(http.StatusBadRequest, "bad_request", "lines must be an integer between 1 and "+strconv.Itoa(maxOutputLines)))
			return
		}
		lines = n
	}
	if !a.Online {
		writeAPIErr(w, newErr(http.StatusServiceUnavailable, "session_offline", "session is offline"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	text, err := s.act.Output(ctx, a, lines)
	if err != nil {
		writeAPIErr(w, fromActionErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func parseSince(v string) (time.Duration, error) {
	if v == "" {
		return defaultSince, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		if n > 14 {
			n = 14
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, err
		}
	}
	if d <= 0 {
		return 0, errors.New("since must be positive")
	}
	return min(d, maxSince), nil
}

func (s *server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, err := parseSince(q.Get("since"))
	if err != nil {
		writeAPIErr(w, newErr(http.StatusBadRequest, "bad_request", "invalid since duration (e.g. 90m, 2h, 3d; max 14d)"))
		return
	}
	events, err := s.st.Events(r.Context(), s.now().Add(-d), q.Get("session"))
	if err != nil {
		slog.Error("api: timeline failed", "err", err)
		writeAPIErr(w, newErr(http.StatusInternalServerError, "internal", "timeline unavailable"))
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

// ---- action POSTs ----

type (
	hashBody struct {
		PromptHash string `json:"prompt_hash"`
	}
	denyBody struct {
		PromptHash string `json:"prompt_hash"`
		Note       string `json:"note"`
	}
	textBody struct {
		Text string `json:"text"`
	}
	sendBody struct {
		Text  string `json:"text"`
		Enter bool   `json:"enter"`
	}
	keysBody struct {
		Keys []string `json:"keys"`
	}
	interruptBody struct {
		Force bool `json:"force"`
	}
	emptyBody struct{}
)

// decodeBody reads a size-limited JSON object, rejecting unknown fields and
// trailing data. With optional set, an empty body is accepted.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, optional bool) *apiErr {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, err2 := dec.Token(); err2 != io.EOF {
			if err2 == nil {
				err2 = errors.New("unexpected data after JSON body")
			}
			err = err2
			if isTooLarge(err) {
				return newErr(http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 64 KiB")
			}
			return newErr(http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		}
		return nil
	}
	switch {
	case isTooLarge(err):
		return newErr(http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 64 KiB")
	case errors.Is(err, io.EOF) && optional:
		return nil
	case errors.Is(err, io.EOF):
		return newErr(http.StatusBadRequest, "bad_request", "request body is required")
	}
	return newErr(http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
}

func isTooLarge(err error) bool {
	var mb *http.MaxBytesError
	return errors.As(err, &mb)
}

func badRequest(msg string) *apiErr { return newErr(http.StatusBadRequest, "bad_request", msg) }

func (s *server) handleAgentPost(w http.ResponseWriter, r *http.Request) {
	id, verb, e := agentRoute(r, postVerbs)
	if e == nil && verb == "" {
		e = newErr(http.StatusNotFound, "not_found", "no such route")
	}
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	dev := deviceFrom(r)
	if ok, wait := s.actionLimit.allow(dev.ID); !ok {
		writeTooMany(w, wait)
		return
	}
	payload, aerr := s.runAction(w, r, id, verb)
	s.audit(r, dev, id, verb, payload, aerr)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// runAction decodes the body and calls the matching Actions method. payload is
// the JSON of the decoded body ("" when it could not be decoded).
func (s *server) runAction(w http.ResponseWriter, r *http.Request, id, verb string) (payload string, aerr *apiErr) {
	a, ok := s.eng.Agent(id)
	if !ok {
		return "", newErr(http.StatusNotFound, "agent_not_found", "unknown agent")
	}
	if verb != "seen" && !a.Online {
		return "", newErr(http.StatusServiceUnavailable, "session_offline", "session is offline")
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()

	// run decodes into body, validates it, then performs call.
	run := func(body any, optional bool, validate func() string, call func() error) (string, *apiErr) {
		if e := decodeBody(w, r, body, optional); e != nil {
			return "", e
		}
		b, _ := json.Marshal(body)
		if validate != nil {
			if msg := validate(); msg != "" {
				return string(b), badRequest(msg)
			}
		}
		if err := call(); err != nil {
			return string(b), fromActionErr(err)
		}
		return string(b), nil
	}

	switch verb {
	case "approve":
		var b hashBody
		return run(&b, false, needHash(&b.PromptHash), func() error { return s.act.Approve(ctx, a, b.PromptHash) })
	case "trust":
		var b hashBody
		return run(&b, false, needHash(&b.PromptHash), func() error { return s.act.Trust(ctx, a, b.PromptHash) })
	case "deny":
		var b denyBody
		return run(&b, false, needHash(&b.PromptHash), func() error { return s.act.Deny(ctx, a, b.PromptHash, b.Note) })
	case "reply":
		var b textBody
		return run(&b, false, func() string {
			if strings.TrimSpace(b.Text) == "" {
				return "text is required"
			}
			return ""
		}, func() error { return s.act.Reply(ctx, a, b.Text) })
	case "send":
		var b sendBody
		return run(&b, false, func() string {
			if b.Text == "" && !b.Enter {
				return "text or enter is required"
			}
			return ""
		}, func() error { return s.act.Send(ctx, a, b.Text, b.Enter) })
	case "keys":
		var b keysBody
		return run(&b, false, func() string {
			if len(b.Keys) == 0 || len(b.Keys) > maxKeys {
				return "keys must contain between 1 and " + strconv.Itoa(maxKeys) + " entries"
			}
			for _, k := range b.Keys {
				if strings.TrimSpace(k) == "" {
					return "keys must not contain empty entries"
				}
			}
			return ""
		}, func() error { return s.act.Keys(ctx, a, b.Keys) })
	case "interrupt":
		var b interruptBody
		return run(&b, true, nil, func() error { return s.act.Interrupt(ctx, a, b.Force) })
	default: // seen
		var b emptyBody
		return run(&b, true, nil, func() error { s.eng.MarkSeen(id); return nil })
	}
}

func needHash(h *string) func() string {
	return func() string {
		if strings.TrimSpace(*h) == "" {
			return "prompt_hash is required"
		}
		return ""
	}
}

// audit records the attempt after it finished, so failures are logged too.
func (s *server) audit(r *http.Request, dev store.Device, agentID, action, payload string, aerr *apiErr) {
	result := "ok"
	if aerr != nil {
		result = aerr.Error()
	}
	// Detached from the request so a client that hangs up cannot lose the row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	err := s.st.AddAudit(ctx, store.AuditEntry{
		At:      s.now(),
		Actor:   dev.ID,
		Surface: "api",
		AgentID: agentID,
		Action:  action,
		Payload: payload,
		Result:  result,
	})
	if err != nil {
		slog.Error("api: audit write failed", "agent", agentID, "action", action, "err", err)
	}
}
