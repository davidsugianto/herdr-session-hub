package notify

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// fakeEngine hands out channels the test controls.
type fakeEngine struct {
	mu     sync.Mutex
	agents []model.Agent
	subs   chan chan model.Change // every Subscribe call delivers its channel here
}

func newFakeEngine(agents ...model.Agent) *fakeEngine {
	return &fakeEngine{agents: agents, subs: make(chan chan model.Change, 16)}
}

func (e *fakeEngine) Subscribe(ctx context.Context) <-chan model.Change {
	ch := make(chan model.Change, 16)
	e.subs <- ch
	return ch
}

func (e *fakeEngine) Agents(session string) []model.Agent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.Agent(nil), e.agents...)
}

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu        sync.Mutex
	settings  map[string]string
	subs      map[string]store.PushSub
	mutes     []store.Mute
	removed   []string
	setCalls  int
	mutesErr  error
	settingEr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{settings: map[string]string{}, subs: map[string]store.PushSub{}}
}

func (s *fakeStore) Setting(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settingEr != nil {
		return "", s.settingEr
	}
	v, ok := s.settings[key]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}

func (s *fakeStore) SetSetting(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls++
	s.settings[key] = value
	return nil
}

func (s *fakeStore) DeleteSetting(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.settings, key)
	return nil
}

func (s *fakeStore) AddPushSub(ctx context.Context, p store.PushSub) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[p.Endpoint] = p
	return nil
}

func (s *fakeStore) RemovePushSub(ctx context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, endpoint)
	s.removed = append(s.removed, endpoint)
	return nil
}

func (s *fakeStore) PushSubs(ctx context.Context) ([]store.PushSub, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.PushSub, 0, len(s.subs))
	for _, p := range s.subs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out, nil
}

func (s *fakeStore) SetMute(ctx context.Context, m store.Mute) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutes = append(s.mutes, m)
	return nil
}

func (s *fakeStore) DeleteMute(ctx context.Context, scope, key string) error { return nil }

// Mutes returns every row, expired included, so the notifier's own expiry
// check is exercised too.
func (s *fakeStore) Mutes(ctx context.Context, now time.Time) ([]store.Mute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutesErr != nil {
		return nil, s.mutesErr
	}
	return append([]store.Mute(nil), s.mutes...), nil
}

// fakePrompts returns canned values after an optional delay.
type fakePrompts struct {
	prompt    actions.PromptView
	promptErr error
	output    string
	outputErr error
	delay     time.Duration

	mu          sync.Mutex
	promptCalls int
	outputCalls int
}

func (p *fakePrompts) wait(ctx context.Context) error {
	if p.delay == 0 {
		return nil
	}
	select {
	case <-time.After(p.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *fakePrompts) CurrentPrompt(ctx context.Context, a model.Agent) (actions.PromptView, error) {
	p.mu.Lock()
	p.promptCalls++
	p.mu.Unlock()
	if err := p.wait(ctx); err != nil {
		return actions.PromptView{}, err
	}
	return p.prompt, p.promptErr
}

func (p *fakePrompts) Output(ctx context.Context, a model.Agent, lines int) (string, error) {
	p.mu.Lock()
	p.outputCalls++
	p.mu.Unlock()
	if err := p.wait(ctx); err != nil {
		return "", err
	}
	return p.output, p.outputErr
}

type sent struct {
	sub store.PushSub
	p   Payload
}

// fakeSink records sends; errs maps endpoint -> error to return.
type fakeSink struct {
	mu   sync.Mutex
	errs map[string]error
	got  []sent
	ch   chan sent
}

func newFakeSink() *fakeSink {
	return &fakeSink{errs: map[string]error{}, ch: make(chan sent, 64)}
}

func (s *fakeSink) Send(ctx context.Context, sub store.PushSub, p Payload) error {
	s.mu.Lock()
	err := s.errs[sub.Endpoint]
	s.got = append(s.got, sent{sub, p})
	s.mu.Unlock()
	s.ch <- sent{sub, p}
	return err
}

func (s *fakeSink) sends() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sent(nil), s.got...)
}

type quietFunc func(session string, t time.Time) bool

func (f quietFunc) Active(session string, t time.Time) bool { return f(session, t) }

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (p *fakePrompts) calls() (prompt, output int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.promptCalls, p.outputCalls
}
