// Package model holds the hub's domain types and the state engine that maps
// raw herdr statuses to hub states.
package model

import "time"

// State is the hub's four-word state (plus unknown).
type State string

const (
	StateBlocked  State = "blocked"  // herdr: blocked
	StateFinished State = "finished" // herdr: done (turn ended, not yet seen)
	StateWorking  State = "working"  // herdr: working
	StateWaiting  State = "waiting"  // herdr: idle, or done after MarkSeen
	StateUnknown  State = "unknown"  // herdr: unknown or unrecognised
)

// Agent is the hub's view of one agent. ID is "<session>/<terminal_id>" and is
// the only stable key; PaneID is an address that can change.
type Agent struct {
	ID           string    `json:"id"`
	Session      string    `json:"session"`       // herdr session directory name
	SessionLabel string    `json:"session_label"` // display label from config
	TerminalID   string    `json:"terminal_id"`
	PaneID       string    `json:"pane_id"`
	Name         string    `json:"name"` // herdr agent name (user-assigned); falls back to Kind
	Kind         string    `json:"kind"` // agent type, e.g. "claude", "pi", "omp"; selects the prompt profile
	Cwd          string    `json:"cwd"`
	Title        string    `json:"title"`
	State        State     `json:"state"`
	RawStatus    string    `json:"raw_status"`
	Since        time.Time `json:"since"`  // when State last changed
	Online       bool      `json:"online"` // false while its session is offline
}

// SessionInfo summarises one herdr session.
type SessionInfo struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Online bool   `json:"online"`
	Agents int    `json:"agents"`
}

// ChangeType is the kind of a Change.
type ChangeType string

const (
	ChangeAdded          ChangeType = "added"
	ChangeState          ChangeType = "state"   // State changed (after the flap guard)
	ChangeUpdated        ChangeType = "updated" // pane id, title, cwd changed; State did not
	ChangeRemoved        ChangeType = "removed"
	ChangeSessionOnline  ChangeType = "session_online"
	ChangeSessionOffline ChangeType = "session_offline"
)

// Change is what the engine publishes to subscribers.
type Change struct {
	Type    ChangeType `json:"type"`
	Session string     `json:"session"`
	Agent   Agent      `json:"agent"` // zero for session_* changes
	From    State      `json:"from,omitempty"`
	To      State      `json:"to,omitempty"`
	// NewEpisode is true on a ChangeState into blocked that is not a bounce
	// (blocked->working->blocked) inside the flap guard window.
	NewEpisode bool      `json:"new_episode,omitempty"`
	At         time.Time `json:"at"`
}
