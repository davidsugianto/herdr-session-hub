package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// ---- fake clock ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- fake engine ----

type fakeEngine struct {
	mu       sync.Mutex
	sessions []model.SessionInfo
	agents   map[string]model.Agent
	seen     []string
	subs     []chan model.Change
}

func newFakeEngine(agents ...model.Agent) *fakeEngine {
	e := &fakeEngine{agents: map[string]model.Agent{}}
	for _, a := range agents {
		e.agents[a.ID] = a
	}
	return e
}

func (e *fakeEngine) Sessions() []model.SessionInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.SessionInfo(nil), e.sessions...)
}

func (e *fakeEngine) Agents(session string) []model.Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Agent
	for _, a := range e.agents {
		if session == "" || a.Session == session {
			out = append(out, a)
		}
	}
	return out
}

func (e *fakeEngine) Agent(id string) (model.Agent, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	return a, ok
}

func (e *fakeEngine) MarkSeen(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, id)
	return true
}

func (e *fakeEngine) Subscribe(ctx context.Context) <-chan model.Change {
	ch := make(chan model.Change, 1024)
	e.mu.Lock()
	e.subs = append(e.subs, ch)
	e.mu.Unlock()
	go func() {
		<-ctx.Done()
		e.mu.Lock()
		defer e.mu.Unlock()
		for i, c := range e.subs {
			if c == ch {
				e.subs = append(e.subs[:i], e.subs[i+1:]...)
				close(ch)
				break
			}
		}
	}()
	return ch
}

func (e *fakeEngine) emit(c model.Change) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ch := range e.subs {
		ch <- c
	}
}

func (e *fakeEngine) seenIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

// ---- fake store ----

type fakeStore struct {
	mu        sync.Mutex
	tokens    map[string]store.Device // token -> device
	codes     map[string]bool
	audits    []store.AuditEntry
	events    []store.Event
	lookups   int
	evSince   time.Time
	evSession string
	auditErr  error
	nextDev   int

	settings   map[string]string
	settingErr error
	settingN   int // Setting calls
	pushSubs   []store.PushSub
	mutes      map[[2]string]store.Mute
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tokens:   map[string]store.Device{"tok-1": {ID: "dev-1", Name: "phone"}, "tok-2": {ID: "dev-2", Name: "tablet"}},
		codes:    map[string]bool{"good-code": true},
		settings: map[string]string{},
		mutes:    map[[2]string]store.Mute{},
	}
}

func (s *fakeStore) AddEvent(context.Context, store.Event) error { return nil }
func (s *fakeStore) Events(_ context.Context, since time.Time, session string) ([]store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evSince, s.evSession = since, session
	return s.events, nil
}
func (s *fakeStore) AddAudit(_ context.Context, a store.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditErr != nil {
		return s.auditErr
	}
	s.audits = append(s.audits, a)
	return nil
}
func (s *fakeStore) Audit(context.Context, time.Time) ([]store.AuditEntry, error) { return nil, nil }
func (s *fakeStore) CreatePairCode(context.Context, time.Duration) (string, error) {
	return "", nil
}
func (s *fakeStore) ExchangePairCode(_ context.Context, code, name string) (string, store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.codes[code] {
		return "", store.Device{}, store.ErrInvalidCode
	}
	delete(s.codes, code)
	s.nextDev++
	tok := fmt.Sprintf("paired-token-%d", s.nextDev)
	d := store.Device{ID: fmt.Sprintf("paired-%d", s.nextDev), Name: name}
	s.tokens[tok] = d
	return tok, d, nil
}
func (s *fakeStore) DeviceByToken(_ context.Context, token string) (store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	d, ok := s.tokens[token]
	if !ok {
		return store.Device{}, store.ErrNotFound
	}
	return d, nil
}
func (s *fakeStore) Devices(context.Context) ([]store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []store.Device
	for _, d := range s.tokens {
		if !seen[d.ID] {
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *fakeStore) RevokeDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for t, d := range s.tokens {
		if d.ID == id {
			delete(s.tokens, t)
			found = true
		}
	}
	if !found {
		return store.ErrNotFound
	}
	return nil
}
func (s *fakeStore) Prune(context.Context, time.Time) error { return nil }
func (s *fakeStore) Close() error                           { return nil }

// SettingsStore, PushStore and MuteStore.
var (
	_ store.SettingsStore = (*fakeStore)(nil)
	_ store.PushStore     = (*fakeStore)(nil)
	_ store.MuteStore     = (*fakeStore)(nil)
)

func (s *fakeStore) Setting(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingN++
	if s.settingErr != nil {
		return "", s.settingErr
	}
	v, ok := s.settings[key]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}
func (s *fakeStore) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
	return nil
}
func (s *fakeStore) DeleteSetting(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.settings, key)
	return nil
}

func (s *fakeStore) AddPushSub(_ context.Context, p store.PushSub) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.pushSubs {
		if q.Endpoint == p.Endpoint {
			s.pushSubs[i] = p
			return nil
		}
	}
	s.pushSubs = append(s.pushSubs, p)
	return nil
}
func (s *fakeStore) RemovePushSub(_ context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.pushSubs {
		if q.Endpoint == endpoint {
			s.pushSubs = append(s.pushSubs[:i], s.pushSubs[i+1:]...)
			return nil
		}
	}
	return nil
}
func (s *fakeStore) PushSubs(context.Context) ([]store.PushSub, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.PushSub(nil), s.pushSubs...), nil
}

func (s *fakeStore) SetMute(_ context.Context, m store.Mute) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutes[[2]string{m.Scope, m.Key}] = m
	return nil
}
func (s *fakeStore) DeleteMute(_ context.Context, scope, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mutes, [2]string{scope, key})
	return nil
}
func (s *fakeStore) Mutes(_ context.Context, now time.Time) ([]store.Mute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Mute
	for _, m := range s.mutes {
		if m.Until.IsZero() || m.Until.After(now) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope+"/"+out[i].Key < out[j].Scope+"/"+out[j].Key })
	return out, nil
}

func (s *fakeStore) settingCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingN
}

// ---- fake push notifier ----

type fakeNotifier struct {
	mu    sync.Mutex
	key   string
	err   error
	tests []string // device ids passed to SendTest
}

func (n *fakeNotifier) PublicKey() string { return n.key }
func (n *fakeNotifier) SendTest(_ context.Context, deviceID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tests = append(n.tests, deviceID)
	return n.err
}

func (n *fakeNotifier) testCalls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.tests...)
}

func (s *fakeStore) auditRows() []store.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.AuditEntry(nil), s.audits...)
}

func (s *fakeStore) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

// ---- fake actions ----

type fakeActions struct {
	mu     sync.Mutex
	calls  []string
	err    error // returned by every mutating call
	output string
	outErr error
	prompt actions.PromptView
	prErr  error
	lastN  int
}

func (f *fakeActions) rec(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return f.err
}

func (f *fakeActions) Output(_ context.Context, a model.Agent, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastN = lines
	return f.output, f.outErr
}
func (f *fakeActions) CurrentPrompt(context.Context, model.Agent) (actions.PromptView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompt, f.prErr
}
func (f *fakeActions) Approve(_ context.Context, a model.Agent, h string) error {
	return f.rec(fmt.Sprintf("approve %s %s", a.ID, h))
}
func (f *fakeActions) Trust(_ context.Context, a model.Agent, h string) error {
	return f.rec(fmt.Sprintf("trust %s %s", a.ID, h))
}
func (f *fakeActions) Deny(_ context.Context, a model.Agent, h, note string) error {
	return f.rec(fmt.Sprintf("deny %s %s %q", a.ID, h, note))
}
func (f *fakeActions) Reply(_ context.Context, a model.Agent, text string) error {
	return f.rec(fmt.Sprintf("reply %s %q", a.ID, text))
}
func (f *fakeActions) Send(_ context.Context, a model.Agent, text string, enter bool) error {
	return f.rec(fmt.Sprintf("send %s %q %v", a.ID, text, enter))
}
func (f *fakeActions) Keys(_ context.Context, a model.Agent, keys []string) error {
	return f.rec(fmt.Sprintf("keys %s %s", a.ID, strings.Join(keys, ",")))
}
func (f *fakeActions) Interrupt(_ context.Context, a model.Agent, force bool) error {
	return f.rec(fmt.Sprintf("interrupt %s %v", a.ID, force))
}

func (f *fakeActions) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// ---- test environment ----

const testHost = "127.0.0.1:7878"

type env struct {
	t     *testing.T
	eng   *fakeEngine
	st    *fakeStore
	act   *fakeActions
	clock *fakeClock
	h     http.Handler
}

func agent(id, session string, state model.State, online bool) model.Agent {
	return model.Agent{ID: id, Session: session, TerminalID: strings.TrimPrefix(id, session+"/"), Name: "claude", State: state, Online: online}
}

func newEnv(t *testing.T, mod ...func(*Config)) *env {
	t.Helper()
	e := &env{
		t:     t,
		eng:   newFakeEngine(agent("labs/term_1", "labs", model.StateBlocked, true)),
		st:    newFakeStore(),
		act:   &fakeActions{},
		clock: &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	}
	cfg := Config{Engine: e.eng, Store: e.st, Actions: e.act, Now: e.clock.Now}
	for _, m := range mod {
		m(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.h = New(ctx, cfg)
	return e
}

// req builds a request addressed to the default loopback host.
func req(method, path, body string, hdr ...string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://"+testHost+path, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

func (e *env) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// do sends an authenticated (bearer tok-1) request.
func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	return e.serve(req(method, path, body, "Authorization", "Bearer tok-1"))
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	v := decode[struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, w)
	return v.Error.Code
}

// dropAll closes every subscriber channel, like the real engine does to a
// subscriber whose buffer is full.
func (e *fakeEngine) dropAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ch := range e.subs {
		close(ch)
	}
	e.subs = nil
}

func (e *fakeEngine) subCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subs)
}
