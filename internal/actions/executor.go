package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/herdr"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/profiles"
)

const (
	promptScreenLines = 60
	defaultOutputLns  = 300
	maxOutputLns      = 2000
	maxKeys           = 20
	maxKeyLen         = 32
	defaultDenyDelay  = 300 * time.Millisecond
)

var (
	keyRe = regexp.MustCompile(`^[A-Za-z0-9+_-]+$`)
	// ansiRe matches CSI, OSC and simple two-byte escape sequences.
	ansiRe = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
)

// SocketResolver maps a session directory name to its socket path.
type SocketResolver interface {
	Socket(session string) (string, bool)
}

// caller is the herdr transport; herdr answers one request per connection, so
// every call dials fresh.
type caller interface {
	Call(ctx context.Context, socketPath, method string, params, result any) error
}

type onceCaller struct{}

func (onceCaller) Call(ctx context.Context, socketPath, method string, params, result any) error {
	return herdr.CallOnce(ctx, socketPath, method, params, result)
}

// Executor implements Service against live herdr sockets.
type Executor struct {
	sockets  SocketResolver
	profiles profiles.Provider
	c        caller
	// denyDelay is the pause between the deny keys and the typed note, giving
	// the agent time to open its free-text field.
	denyDelay time.Duration
}

var _ Service = (*Executor)(nil)

// New returns an Executor that talks to the real herdr sockets.
func New(sockets SocketResolver, profiles profiles.Provider) *Executor {
	return &Executor{sockets: sockets, profiles: profiles, c: onceCaller{}, denyDelay: defaultDenyDelay}
}

// profileFor picks the prompt profile by agent type. herdr's agent name is
// user-assigned ("sa-1", "reviewer"), so it only serves as a fallback.
func (e *Executor) profileFor(a model.Agent) profiles.Profile {
	if a.Kind != "" {
		return e.profiles.For(a.Kind)
	}
	return e.profiles.For(a.Name)
}

// target is an agent re-resolved against live herdr state.
type target struct {
	socket string
	paneID string
	info   herdr.AgentInfo
}

// mapErr translates herdr errors into the package sentinels; anything else is
// returned unchanged (wrapped by callers with %w).
func mapErr(err error) error {
	var he *herdr.Error
	if errors.As(err, &he) {
		switch he.Code {
		case herdrCodeAgentGone, herdrCodePaneGone:
			return fmt.Errorf("%w: %w", ErrAgentGone, err)
		}
	}
	return err
}

func isDialFailure(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return true
	}
	return errors.Is(err, herdr.ErrClosed)
}

func isHerdrErr(err error) bool {
	var he *herdr.Error
	return errors.As(err, &he)
}

// resolve finds the live pane of a.TerminalID: first by a.PaneID via
// agent.get, then (pane moved or gone) by scanning agent.list.
func (e *Executor) resolve(ctx context.Context, a model.Agent) (target, error) {
	sock, ok := e.sockets.Socket(a.Session)
	if !ok || sock == "" {
		return target{}, ErrSessionOffline
	}
	if a.TerminalID == "" {
		return target{}, ErrAgentGone
	}
	if a.PaneID != "" {
		var res herdr.AgentGetResult
		err := e.c.Call(ctx, sock, herdr.MethodAgentGet, herdr.AgentGetParams{Target: a.PaneID}, &res)
		switch {
		case err == nil:
			if res.Agent.TerminalID == a.TerminalID {
				return target{socket: sock, paneID: a.PaneID, info: res.Agent}, nil
			}
			// The pane id now belongs to another terminal; look the terminal up.
		case isDialFailure(err):
			return target{}, fmt.Errorf("%w: %w", ErrSessionOffline, err)
		case ctx.Err() != nil:
			return target{}, ctx.Err()
		case isHerdrErr(err):
			// Pane id unknown to herdr; fall through to the list scan.
		default:
			return target{}, fmt.Errorf("actions: agent.get: %w", err)
		}
	}
	var list herdr.AgentListResult
	if err := e.c.Call(ctx, sock, herdr.MethodAgentList, herdr.EmptyParams{}, &list); err != nil {
		if isDialFailure(err) {
			return target{}, fmt.Errorf("%w: %w", ErrSessionOffline, err)
		}
		return target{}, fmt.Errorf("actions: agent.list: %w", err)
	}
	for _, ag := range list.Agents {
		if ag.TerminalID == a.TerminalID {
			return target{socket: sock, paneID: ag.PaneID, info: ag}, nil
		}
	}
	return target{}, ErrAgentGone
}

func (e *Executor) readScreen(ctx context.Context, t target, lines uint32, source herdr.ReadSource) (string, error) {
	strip := true
	var res herdr.PaneReadResult
	err := e.c.Call(ctx, t.socket, herdr.MethodPaneRead, herdr.PaneReadParams{
		PaneID:    t.paneID,
		Source:    source,
		Format:    herdr.ReadFormatText,
		Lines:     &lines,
		StripANSI: &strip,
	}, &res)
	if err != nil {
		return "", fmt.Errorf("actions: pane.read: %w", mapErr(err))
	}
	return ansiRe.ReplaceAllString(res.Read.Text, ""), nil
}

func (e *Executor) sendKeys(ctx context.Context, t target, keys []string) error {
	var res ackResult
	err := e.c.Call(ctx, t.socket, methodPaneSendKeys, paneSendKeysParams{PaneID: t.paneID, Keys: keys}, &res)
	if err != nil {
		return fmt.Errorf("actions: pane.send_keys: %w", mapErr(err))
	}
	return nil
}

func (e *Executor) sendText(ctx context.Context, t target, text string) error {
	var res ackResult
	err := e.c.Call(ctx, t.socket, methodPaneSendText, paneSendTextParams{PaneID: t.paneID, Text: text}, &res)
	if err != nil {
		return fmt.Errorf("actions: pane.send_text: %w", mapErr(err))
	}
	return nil
}

// Output implements Service.
func (e *Executor) Output(ctx context.Context, a model.Agent, lines int) (string, error) {
	t, err := e.resolve(ctx, a)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = defaultOutputLns
	}
	if lines > maxOutputLns {
		lines = maxOutputLns
	}
	return e.readScreen(ctx, t, uint32(lines), herdr.ReadSourceRecentUnwrapped)
}

// CurrentPrompt implements Service.
func (e *Executor) CurrentPrompt(ctx context.Context, a model.Agent) (PromptView, error) {
	t, err := e.resolve(ctx, a)
	if err != nil {
		return PromptView{}, err
	}
	prof := e.profileFor(a)
	view := PromptView{Profile: prof.Name(), Verified: prof.Verified()}
	if t.info.AgentStatus != herdr.AgentStatusBlocked {
		return view, nil
	}
	screen, err := e.readScreen(ctx, t, promptScreenLines, herdr.ReadSourceRecentUnwrapped)
	if err != nil {
		return PromptView{}, err
	}
	if p, ok := prof.Parse(screen); ok {
		view.Prompt = p
	}
	return view, nil
}

// guarded runs the shared approve/trust/deny checks and returns the keys to
// send. Nothing is sent by this function.
func (e *Executor) guarded(ctx context.Context, a model.Agent, act profiles.Action, promptHash string) (target, []string, error) {
	t, err := e.resolve(ctx, a)
	if err != nil {
		return target{}, nil, err
	}
	if t.info.AgentStatus != herdr.AgentStatusBlocked {
		return target{}, nil, ErrNotBlocked
	}
	screen, err := e.readScreen(ctx, t, promptScreenLines, herdr.ReadSourceRecentUnwrapped)
	if err != nil {
		return target{}, nil, err
	}
	prof := e.profileFor(a)
	p, ok := prof.Parse(screen)
	if !ok {
		return target{}, nil, ErrNoPrompt
	}
	if p.Hash != promptHash {
		return target{}, nil, ErrPromptChanged
	}
	keys, ok := prof.Keys(act, p)
	if !ok || len(keys) == 0 {
		return target{}, nil, ErrActionNotAvail
	}
	return t, keys, nil
}

// Approve implements Service.
func (e *Executor) Approve(ctx context.Context, a model.Agent, promptHash string) error {
	t, keys, err := e.guarded(ctx, a, profiles.ActionApprove, promptHash)
	if err != nil {
		return err
	}
	return e.sendKeys(ctx, t, keys)
}

// Trust implements Service.
func (e *Executor) Trust(ctx context.Context, a model.Agent, promptHash string) error {
	t, keys, err := e.guarded(ctx, a, profiles.ActionTrust, promptHash)
	if err != nil {
		return err
	}
	return e.sendKeys(ctx, t, keys)
}

// Deny implements Service. A non-empty note is typed after the deny keys.
func (e *Executor) Deny(ctx context.Context, a model.Agent, promptHash, note string) error {
	t, keys, err := e.guarded(ctx, a, profiles.ActionDeny, promptHash)
	if err != nil {
		return err
	}
	if err := e.sendKeys(ctx, t, keys); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return nil
	}
	if e.denyDelay > 0 {
		timer := time.NewTimer(e.denyDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	if err := e.sendText(ctx, t, note); err != nil {
		return err
	}
	return e.sendKeys(ctx, t, []string{"enter"})
}

// Reply implements Service.
func (e *Executor) Reply(ctx context.Context, a model.Agent, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: reply text is empty", ErrInvalidInput)
	}
	t, err := e.resolve(ctx, a)
	if err != nil {
		return err
	}
	var res ackResult
	err = e.c.Call(ctx, t.socket, methodAgentPrompt, agentPromptParams{Target: t.paneID, Text: text}, &res)
	if err != nil {
		var he *herdr.Error
		if errors.As(err, &he) && he.Code == herdrCodeBlocked {
			return fmt.Errorf("%w: %w", ErrAgentBlocked, err)
		}
		return fmt.Errorf("actions: agent.prompt: %w", mapErr(err))
	}
	return nil
}

// Send implements Service.
func (e *Executor) Send(ctx context.Context, a model.Agent, text string, enter bool) error {
	if text == "" && !enter {
		return fmt.Errorf("%w: nothing to send", ErrInvalidInput)
	}
	t, err := e.resolve(ctx, a)
	if err != nil {
		return err
	}
	if text != "" {
		if err := e.sendText(ctx, t, text); err != nil {
			return err
		}
	}
	if enter {
		return e.sendKeys(ctx, t, []string{"enter"})
	}
	return nil
}

func validateKeys(keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: no keys given", ErrInvalidInput)
	}
	if len(keys) > maxKeys {
		return fmt.Errorf("%w: too many keys (%d, max %d)", ErrInvalidInput, len(keys), maxKeys)
	}
	for _, k := range keys {
		if k == "" {
			return fmt.Errorf("%w: empty key", ErrInvalidInput)
		}
		if len(k) > maxKeyLen {
			return fmt.Errorf("%w: key %q longer than %d chars", ErrInvalidInput, k, maxKeyLen)
		}
		if !keyRe.MatchString(k) {
			return fmt.Errorf("%w: invalid key %q", ErrInvalidInput, k)
		}
	}
	return nil
}

// Keys implements Service.
func (e *Executor) Keys(ctx context.Context, a model.Agent, keys []string) error {
	if err := validateKeys(keys); err != nil {
		return err
	}
	t, err := e.resolve(ctx, a)
	if err != nil {
		return err
	}
	return e.sendKeys(ctx, t, keys)
}

// Interrupt implements Service.
func (e *Executor) Interrupt(ctx context.Context, a model.Agent, force bool) error {
	t, err := e.resolve(ctx, a)
	if err != nil {
		return err
	}
	act, fallback := profiles.ActionInterrupt, "esc"
	if force {
		act, fallback = profiles.ActionForceInterrupt, "ctrl+c"
	}
	keys := []string{fallback}
	if k, ok := e.profileFor(a).Keys(act, profiles.Prompt{}); ok && len(k) > 0 {
		keys = k
	}
	return e.sendKeys(ctx, t, keys)
}
