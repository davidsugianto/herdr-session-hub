// Package notify turns engine state changes into Web Push notifications: an
// agent became blocked (once per flap-guarded episode) or finished. Mutes and
// per-session quiet hours are checked at send time.
package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/profiles"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// Kind is what a notification is about.
type Kind string

const (
	KindBlocked  Kind = "blocked"
	KindFinished Kind = "finished"
	KindTest     Kind = "test" // SendTest
)

// ErrGone is returned by a Sink when the push service says the subscription no
// longer exists (404/410); the notifier then deletes it.
var ErrGone = errors.New("notify: subscription gone")

const (
	defaultPromptBudget = 500 * time.Millisecond
	defaultContact      = "mailto:hub@example.com"
	sendTimeout         = 5 * time.Second
	outputLines         = 40
	maxBodyRunes        = 160
)

// Payload is the JSON the service worker receives.
type Payload struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	Tag     string `json:"tag"`
	Badge   int    `json:"badge"`
	Kind    Kind   `json:"kind"`
	AgentID string `json:"agent_id"`
}

// Sink delivers one payload to one subscription.
type Sink interface {
	Send(ctx context.Context, sub store.PushSub, p Payload) error
}

// Engine is the part of *model.Engine the notifier uses.
type Engine interface {
	Subscribe(ctx context.Context) <-chan model.Change
	Agents(session string) []model.Agent
}

// Prompts is the part of actions.Service used to build the body.
type Prompts interface {
	CurrentPrompt(ctx context.Context, a model.Agent) (actions.PromptView, error)
	Output(ctx context.Context, a model.Agent, lines int) (string, error)
}

// QuietPolicy reports whether a session's quiet hours are active at t.
type QuietPolicy interface {
	Active(session string, t time.Time) bool
}

// Store is the persistence the notifier needs.
type Store interface {
	store.SettingsStore
	store.PushStore
	store.MuteStore
}

// Options configures New. Store is required; Engine and Prompts are needed by
// Run (Prompts may be nil, then bodies fall back to generic text).
type Options struct {
	Engine             Engine
	Store              Store
	Prompts            Prompts
	Quiet              QuietPolicy // may be nil
	BlockedBreaksQuiet bool
	Label              func(session string) string // default: agent's SessionLabel, else session
	Contact            string                      // VAPID subscriber; default mailto:hub@example.com
	Sink               Sink                        // default: Web Push with the stored VAPID keys
	Now                func() time.Time
	Log                io.Writer
	PromptBudget       time.Duration // total time to read the body; default 500ms
}

// Notifier sends notifications. Its methods are goroutine-safe.
type Notifier struct {
	o      Options
	public string

	logMu sync.Mutex
}

// New loads the VAPID keys from the store, generating and saving them on first
// use, and returns a notifier.
func New(o Options) (*Notifier, error) {
	if o.Store == nil {
		return nil, errors.New("notify: store is required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.PromptBudget <= 0 {
		o.PromptBudget = defaultPromptBudget
	}
	if o.Contact == "" {
		o.Contact = defaultContact
	}
	pub, priv, err := loadVAPID(context.Background(), o.Store)
	if err != nil {
		return nil, err
	}
	if o.Sink == nil {
		o.Sink = newWebPushSink(pub, priv, o.Contact, nil)
	}
	return &Notifier{o: o, public: pub}, nil
}

// PublicKey is the VAPID public key (base64url) for PushManager.subscribe.
func (n *Notifier) PublicKey() string { return n.public }

func (n *Notifier) logf(format string, args ...any) {
	n.logMu.Lock()
	defer n.logMu.Unlock()
	_, _ = fmt.Fprintf(n.o.Log, format+"\n", args...)
}

// SendTest sends a test notification to every subscription of deviceID. It
// returns store.ErrNotFound when the device has no (live) subscription.
func (n *Notifier) SendTest(ctx context.Context, deviceID string) error {
	all, err := n.o.Store.PushSubs(ctx)
	if err != nil {
		return fmt.Errorf("notify: push subscriptions: %w", err)
	}
	var subs []store.PushSub
	for _, s := range all {
		if s.DeviceID == deviceID {
			subs = append(subs, s)
		}
	}
	if len(subs) == 0 {
		return store.ErrNotFound
	}
	p := Payload{Title: "Test notification", Body: "Notifications from the hub are working.",
		URL: "/#/settings", Tag: "test", Kind: KindTest, Badge: n.badge()}
	sent, gone, firstErr := n.deliver(ctx, subs, p)
	switch {
	case sent > 0:
		return nil
	case gone == len(subs):
		return store.ErrNotFound
	default:
		return fmt.Errorf("notify: test push: %w", firstErr)
	}
}

// Run sends notifications for engine changes until ctx is done. If the engine
// drops the subscription (buffer overflow) it resubscribes; changes published
// in between are lost.
func (n *Notifier) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	ch := n.o.Engine.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return
				}
				n.logf("notify: engine dropped the subscription; resubscribing, some notifications may be lost")
				ch = n.o.Engine.Subscribe(ctx)
				continue
			}
			if _, ok := kindOf(c); !ok {
				continue
			}
			received := n.o.Now()
			wg.Add(1)
			go func() {
				defer wg.Done()
				n.handle(ctx, c, received)
			}()
		}
	}
}

// kindOf reports whether c should notify, and as what.
func kindOf(c model.Change) (Kind, bool) {
	if c.Type != model.ChangeState {
		return "", false
	}
	switch {
	case c.To == model.StateBlocked && c.NewEpisode:
		return KindBlocked, true
	case c.To == model.StateFinished:
		return KindFinished, true
	}
	return "", false
}

// handle builds and sends the notification for c, if the rules allow it.
func (n *Notifier) handle(ctx context.Context, c model.Change, received time.Time) {
	kind, ok := kindOf(c)
	if !ok {
		return
	}
	a := c.Agent
	now := n.o.Now()
	start := c.At
	if start.IsZero() {
		start = received
	}

	if n.muted(ctx, a, now) {
		return
	}
	if n.o.Quiet != nil && n.o.Quiet.Active(a.Session, now) && !(kind == KindBlocked && n.o.BlockedBreaksQuiet) {
		return
	}

	subs, err := n.o.Store.PushSubs(ctx)
	if err != nil {
		n.logf("notify: push subscriptions: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	// The body read (herdr round trips) and the badge count run concurrently.
	bodyc := make(chan string, 1)
	go func() { bodyc <- n.body(ctx, kind, a) }()
	badge := n.badge()
	p := Payload{
		Title:   title(n.label(a), kind, a),
		Body:    <-bodyc,
		URL:     "/#/a/" + url.PathEscape(a.ID),
		Tag:     a.ID,
		Badge:   badge,
		Kind:    kind,
		AgentID: a.ID,
	}
	sent, _, _ := n.deliver(ctx, subs, p)
	n.logf("notify: %s %s sent to %d/%d subs in %dms", kind, a.ID, sent, len(subs), n.o.Now().Sub(start).Milliseconds())
}

// muted reports whether a or its session has an active mute. A store error is
// logged and treated as not muted: a spurious push beats a missed prompt.
func (n *Notifier) muted(ctx context.Context, a model.Agent, now time.Time) bool {
	mutes, err := n.o.Store.Mutes(ctx, now)
	if err != nil {
		n.logf("notify: mutes: %v", err)
		return false
	}
	for _, m := range mutes {
		if !m.Until.IsZero() && !m.Until.After(now) {
			continue
		}
		if (m.Scope == store.MuteAgent && m.Key == a.ID) || (m.Scope == store.MuteSession && m.Key == a.Session) {
			return true
		}
	}
	return false
}

func (n *Notifier) label(a model.Agent) string {
	if n.o.Label != nil {
		if l := n.o.Label(a.Session); l != "" {
			return l
		}
	}
	if a.SessionLabel != "" {
		return a.SessionLabel
	}
	return a.Session
}

// title is "[label] kind · name is blocked", or "[label] name is blocked" when
// the kind is unknown or equal to the name.
func title(label string, kind Kind, a model.Agent) string {
	who := a.Name
	if a.Kind != "" && a.Kind != a.Name {
		if who == "" {
			who = a.Kind
		} else {
			who = a.Kind + " · " + a.Name
		}
	}
	if who == "" {
		who = a.TerminalID
	}
	return "[" + label + "] " + who + " is " + string(kind)
}

// badge counts online agents that need attention (blocked or finished).
func (n *Notifier) badge() int {
	if n.o.Engine == nil {
		return 0
	}
	count := 0
	for _, a := range n.o.Engine.Agents("") {
		if a.Online && (a.State == model.StateBlocked || a.State == model.StateFinished) {
			count++
		}
	}
	return count
}

// body reads the prompt question or last output line within PromptBudget.
// For blocked agents the prompt and the output are read concurrently and the
// prompt wins when it parses.
func (n *Notifier) body(ctx context.Context, kind Kind, a model.Agent) string {
	fallback := "Done"
	if kind == KindBlocked {
		fallback = "Needs your input"
	}
	if n.o.Prompts == nil {
		return fallback
	}
	bctx, cancel := context.WithTimeout(ctx, n.o.PromptBudget)
	defer cancel()

	outc := make(chan string, 1)
	go func() {
		out, err := n.o.Prompts.Output(bctx, a, outputLines)
		if err != nil {
			outc <- ""
			return
		}
		outc <- lastLine(out)
	}()

	if kind == KindBlocked {
		promptc := make(chan string, 1)
		go func() {
			v, err := n.o.Prompts.CurrentPrompt(bctx, a)
			if err != nil {
				promptc <- ""
				return
			}
			promptc <- promptText(v)
		}()
		select {
		case q := <-promptc:
			if q = clean(q); q != "" {
				return q
			}
		case <-bctx.Done():
			return fallback
		}
	}
	select {
	case line := <-outc:
		if line != "" {
			return line
		}
	case <-bctx.Done():
	}
	return fallback
}

// promptText extracts the question from a PromptView.
func promptText(v actions.PromptView) string {
	switch p := v.Prompt.(type) {
	case profiles.Prompt:
		return p.Text
	case *profiles.Prompt:
		if p != nil {
			return p.Text
		}
	}
	return ""
}

// deliver sends p to every sub concurrently (5s each), pruning gone ones.
func (n *Notifier) deliver(ctx context.Context, subs []store.PushSub, p Payload) (sent, gone int, firstErr error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, sendTimeout)
			err := n.o.Sink.Send(sctx, s, p)
			cancel()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				sent++
			case errors.Is(err, ErrGone):
				gone++
				if rerr := n.o.Store.RemovePushSub(ctx, s.Endpoint); rerr != nil {
					n.logf("notify: remove gone subscription: %v", rerr)
				} else {
					n.logf("notify: removed gone subscription of device %s", s.DeviceID)
				}
			default:
				if firstErr == nil {
					firstErr = err
				}
				n.logf("notify: send to device %s: %v", s.DeviceID, err)
			}
		}()
	}
	wg.Wait()
	return sent, gone, firstErr
}

// loadVAPID returns the stored key pair, generating and saving one when either
// half is missing.
func loadVAPID(ctx context.Context, st store.SettingsStore) (pub, priv string, err error) {
	pub, perr := st.Setting(ctx, store.SettingVAPIDPublic)
	priv, kerr := st.Setting(ctx, store.SettingVAPIDPrivate)
	for _, e := range []error{perr, kerr} {
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return "", "", fmt.Errorf("notify: load VAPID keys: %w", e)
		}
	}
	if perr == nil && kerr == nil {
		if err := checkVAPID(pub, priv); err != nil {
			return "", "", err
		}
		return pub, priv, nil
	}
	pub, priv, err = generateVAPID()
	if err != nil {
		return "", "", fmt.Errorf("notify: generate VAPID keys: %w", err)
	}
	if err := st.SetSetting(ctx, store.SettingVAPIDPrivate, priv); err != nil {
		return "", "", fmt.Errorf("notify: save VAPID keys: %w", err)
	}
	if err := st.SetSetting(ctx, store.SettingVAPIDPublic, pub); err != nil {
		return "", "", fmt.Errorf("notify: save VAPID keys: %w", err)
	}
	return pub, priv, nil
}

// subscriber is the VAPID "sub" value without the mailto: scheme, which
// webpush-go adds itself (it would otherwise send "mailto:mailto:...").
func subscriber(contact string) string {
	return strings.TrimPrefix(contact, "mailto:")
}
