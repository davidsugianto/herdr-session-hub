// Package actions performs guarded operations on a herdr agent: read output,
// approve/trust/deny a prompt, reply, send text, send keys, interrupt.
package actions

import (
	"context"
	"errors"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
)

var (
	ErrSessionOffline = errors.New("actions: session offline or unknown")
	ErrNotBlocked     = errors.New("actions: agent is not blocked")
	ErrNoPrompt       = errors.New("actions: no parseable prompt on screen")
	ErrPromptChanged  = errors.New("actions: prompt changed since it was shown")
	ErrActionNotAvail = errors.New("actions: action not available for this prompt")
	// ErrAgentBlocked is herdr's agent_blocked on agent.prompt: the agent is at
	// a permission prompt, so reply was refused rather than typed into it.
	ErrAgentBlocked = errors.New("actions: agent is blocked; reply refused")
	ErrAgentGone    = errors.New("actions: agent not found")
	// ErrInvalidInput marks a caller mistake (empty text, malformed keys); no
	// request reached herdr.
	ErrInvalidInput = errors.New("actions: invalid input")
)

// PromptView is the current prompt of a blocked agent, for the API.
type PromptView struct {
	Prompt   any    `json:"prompt"` // profiles.Prompt, or nil when none parsed
	Profile  string `json:"profile"`
	Verified bool   `json:"verified"`
}

// Service is implemented by *Executor (see New). a is the agent as the API last
// saw it; implementations must re-read the live state before sending anything.
type Service interface {
	// Output returns the last `lines` lines of the pane (pane.read, recent_unwrapped).
	Output(ctx context.Context, a model.Agent, lines int) (string, error)
	// CurrentPrompt parses the agent's screen with its profile.
	CurrentPrompt(ctx context.Context, a model.Agent) (PromptView, error)
	Approve(ctx context.Context, a model.Agent, promptHash string) error
	Trust(ctx context.Context, a model.Agent, promptHash string) error
	Deny(ctx context.Context, a model.Agent, promptHash, note string) error
	// Reply submits text via agent.prompt (refused with ErrAgentBlocked when blocked).
	Reply(ctx context.Context, a model.Agent, text string) error
	// Send types raw text via pane.send_text; enter appends Enter.
	Send(ctx context.Context, a model.Agent, text string, enter bool) error
	// Keys sends herdr key combos via pane.send_keys, e.g. "enter","esc","ctrl+c".
	Keys(ctx context.Context, a model.Agent, keys []string) error
	// Interrupt sends esc, or ctrl+c when force.
	Interrupt(ctx context.Context, a model.Agent, force bool) error
}
