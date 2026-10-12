// Package profiles describes, per agent, how to read a permission prompt off
// the screen and which keys answer it. Profiles are TOML data files embedded
// in the binary; a profile that cannot parse the screen leaves the raw key grid
// as the only way to answer.
package profiles

// Action is something a profile can map to keys.
type Action string

const (
	ActionApprove        Action = "approve"
	ActionTrust          Action = "trust"
	ActionDeny           Action = "deny"
	ActionInterrupt      Action = "interrupt"       // esc for all three agents
	ActionForceInterrupt Action = "force_interrupt" // ctrl+c
)

// Prompt is a permission prompt parsed from a screen.
type Prompt struct {
	Text    string   `json:"text"`    // the question, e.g. "Allow Bash(go test ./...)?"
	Options []string `json:"options"` // option labels in screen order
	// Hash identifies this exact prompt (sha256 hex of normalised Text+Options),
	// so an approve is rejected if the screen changed since the user looked.
	Hash string `json:"hash"`
	// TrustRule is the exact rule the Trust action would add, when the profile
	// can tell (e.g. "Bash(go test:*)"); empty otherwise.
	TrustRule string `json:"trust_rule,omitempty"`
}

// Profile is one agent's prompt reader. Implementations are immutable.
type Profile interface {
	Name() string
	// Verified is false until the profile has been checked against real
	// captured screens of that agent.
	Verified() bool
	// Parse reads a screen (pane.read text). ok is false if no permission
	// prompt is recognised, in which case only the raw key grid applies.
	Parse(screen string) (p Prompt, ok bool)
	// Keys maps an action to herdr key combos for the given prompt. ok is false
	// when the profile has no such action (e.g. pi has no Trust).
	Keys(a Action, p Prompt) (keys []string, ok bool)
}

// Provider resolves the profile for an agent name ("claude", "pi", "omp", ...);
// unknown agents get the generic profile, never nil.
type Provider interface {
	For(agent string) Profile
}
