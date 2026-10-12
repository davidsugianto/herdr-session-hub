// Package herdr holds the herdr API schema (schema.json, protocol 22) and the
// hand-written domain types the hub spike needs. Field names and method names
// come from the schema; schema_test.go guards against drift.
//
// Decode with plain encoding/json: herdr adds fields over time, so unknown
// fields must be ignored (never use DisallowUnknownFields).
package herdr

// ProtocolVersion is the herdr API protocol the committed schema describes.
const ProtocolVersion = 22

// Method names (request "method" values).
const (
	MethodSessionSnapshot = "session.snapshot"
	MethodEventsSubscribe = "events.subscribe"
	MethodAgentList       = "agent.list"
	MethodAgentGet        = "agent.get"
	MethodPaneRead        = "pane.read"
)

// Subscription types: the "type" of an events.subscribe subscription
// (request schema, Subscription). Dotted form.
const (
	SubPaneCreated            = "pane.created"
	SubPaneClosed             = "pane.closed"
	SubPaneUpdated            = "pane.updated"
	SubPaneMoved              = "pane.moved"
	SubPaneExited             = "pane.exited"
	SubPaneAgentDetected      = "pane.agent_detected"
	SubPaneAgentStatusChanged = "pane.agent_status_changed"
)

// Event kinds: the "event" name of an EventEnvelope and the "type" tag inside
// its data (event schema, EventKind). Underscore form.
const (
	EventPaneCreated            = "pane_created"
	EventPaneClosed             = "pane_closed"
	EventPaneUpdated            = "pane_updated"
	EventPaneMoved              = "pane_moved"
	EventPaneExited             = "pane_exited"
	EventPaneAgentDetected      = "pane_agent_detected"
	EventPaneAgentStatusChanged = "pane_agent_status_changed"
)

// SubEventPaneAgentStatusChanged is the "event" name of a
// SubscriptionEventEnvelope for agent status changes (subscription_event
// schema, SubscriptionEventKind). Dotted form.
const SubEventPaneAgentStatusChanged = "pane.agent_status_changed"

// Result type tags: the "type" of a success response's result.
const (
	ResultSessionSnapshot     = "session_snapshot"
	ResultAgentList           = "agent_list"
	ResultAgentInfo           = "agent_info"
	ResultPaneRead            = "pane_read"
	ResultSubscriptionStarted = "subscription_started"
)

// The schema defines no "events_lost" signal in protocol 22, so none is
// modelled here.

// AgentStatus is herdr's agent status enum.
type AgentStatus string

const (
	AgentStatusIdle    AgentStatus = "idle"
	AgentStatusWorking AgentStatus = "working"
	AgentStatusBlocked AgentStatus = "blocked"
	AgentStatusDone    AgentStatus = "done"
	AgentStatusUnknown AgentStatus = "unknown"
)

// ReadSource selects which text pane.read returns.
type ReadSource string

const (
	ReadSourceVisible         ReadSource = "visible"
	ReadSourceRecent          ReadSource = "recent"
	ReadSourceRecentUnwrapped ReadSource = "recent_unwrapped"
	ReadSourceDetection       ReadSource = "detection"
)

// ReadFormat selects text or ANSI output for pane.read.
type ReadFormat string

const (
	ReadFormatText ReadFormat = "text"
	ReadFormatANSI ReadFormat = "ansi"
)

// ---- Snapshot types -------------------------------------------------------

// WorkspaceInfo describes one workspace.
type WorkspaceInfo struct {
	WorkspaceID string            `json:"workspace_id"`
	Number      uint              `json:"number"`
	Label       string            `json:"label"`
	Focused     bool              `json:"focused"`
	PaneCount   uint              `json:"pane_count"`
	TabCount    uint              `json:"tab_count"`
	ActiveTabID string            `json:"active_tab_id"`
	AgentStatus AgentStatus       `json:"agent_status"`
	Tokens      map[string]string `json:"tokens,omitempty"`
}

// TabInfo describes one tab.
type TabInfo struct {
	TabID       string      `json:"tab_id"`
	WorkspaceID string      `json:"workspace_id"`
	Number      uint        `json:"number"`
	Label       string      `json:"label"`
	Focused     bool        `json:"focused"`
	PaneCount   uint        `json:"pane_count"`
	AgentStatus AgentStatus `json:"agent_status"`
}

// AgentSessionInfo identifies an agent's resumable session.
type AgentSessionInfo struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"` // "id" or "path"
	Value  string `json:"value"`
}

// PaneInfo describes one pane. TerminalID is the stable terminal id; PaneID
// can change when a pane moves.
type PaneInfo struct {
	PaneID                string            `json:"pane_id"`
	TerminalID            string            `json:"terminal_id"`
	WorkspaceID           string            `json:"workspace_id"`
	TabID                 string            `json:"tab_id"`
	Focused               bool              `json:"focused"`
	AgentStatus           AgentStatus       `json:"agent_status"`
	Revision              uint64            `json:"revision"`
	Agent                 string            `json:"agent,omitempty"`
	DisplayAgent          string            `json:"display_agent,omitempty"`
	AgentSession          *AgentSessionInfo `json:"agent_session,omitempty"`
	Cwd                   string            `json:"cwd,omitempty"`
	ForegroundCwd         string            `json:"foreground_cwd,omitempty"`
	Label                 string            `json:"label,omitempty"`
	Title                 string            `json:"title,omitempty"`
	TerminalTitle         string            `json:"terminal_title,omitempty"`
	TerminalTitleStripped string            `json:"terminal_title_stripped,omitempty"`
	StateLabels           map[string]string `json:"state_labels,omitempty"`
	Tokens                map[string]string `json:"tokens,omitempty"`
}

// AgentInfo is the agent view of a pane (agent.list, agent.get, snapshot).
type AgentInfo struct {
	TerminalID            string            `json:"terminal_id"`
	PaneID                string            `json:"pane_id"`
	WorkspaceID           string            `json:"workspace_id"`
	TabID                 string            `json:"tab_id"`
	Focused               bool              `json:"focused"`
	AgentStatus           AgentStatus       `json:"agent_status"`
	Revision              uint64            `json:"revision"`
	Name                  string            `json:"name,omitempty"`
	Agent                 string            `json:"agent,omitempty"`
	DisplayAgent          string            `json:"display_agent,omitempty"`
	AgentSession          *AgentSessionInfo `json:"agent_session,omitempty"`
	Cwd                   string            `json:"cwd,omitempty"`
	ForegroundCwd         string            `json:"foreground_cwd,omitempty"`
	Title                 string            `json:"title,omitempty"`
	TerminalTitle         string            `json:"terminal_title,omitempty"`
	TerminalTitleStripped string            `json:"terminal_title_stripped,omitempty"`
	StateLabels           map[string]string `json:"state_labels,omitempty"`
	Tokens                map[string]string `json:"tokens,omitempty"`
	InteractiveReady      bool              `json:"interactive_ready,omitempty"`
	LaunchPending         bool              `json:"launch_pending,omitempty"`
	StateChangeSeq        uint64            `json:"state_change_seq,omitempty"`
	CompletionSeq         *uint64           `json:"completion_seq,omitempty"`
}

// SessionSnapshot is the session.snapshot payload.
type SessionSnapshot struct {
	Version            string          `json:"version"`
	Protocol           uint32          `json:"protocol"`
	Workspaces         []WorkspaceInfo `json:"workspaces"`
	Tabs               []TabInfo       `json:"tabs"`
	Panes              []PaneInfo      `json:"panes"`
	Agents             []AgentInfo     `json:"agents"`
	FocusedWorkspaceID string          `json:"focused_workspace_id,omitempty"`
	FocusedTabID       string          `json:"focused_tab_id,omitempty"`
	FocusedPaneID      string          `json:"focused_pane_id,omitempty"`
}

// ---- Method params and results --------------------------------------------

// EmptyParams is the (empty) params object for session.snapshot and
// agent.list.
type EmptyParams struct{}

// AgentGetParams is the params object for agent.get.
type AgentGetParams struct {
	Target string `json:"target"`
}

// Subscription is one entry of events.subscribe params. Type is one of the Sub*
// constants. PaneID and AgentStatus only apply to pane-scoped subscription
// types and are omitted when empty.
type SubscriptionSpec struct {
	Type        string      `json:"type"`
	PaneID      string      `json:"pane_id,omitempty"`
	AgentStatus AgentStatus `json:"agent_status,omitempty"`
}

// EventsSubscribeParams is the params object for events.subscribe.
type EventsSubscribeParams struct {
	Subscriptions []SubscriptionSpec `json:"subscriptions"`
}

// PaneReadParams is the params object for pane.read.
type PaneReadParams struct {
	PaneID    string     `json:"pane_id"`
	Source    ReadSource `json:"source"`
	Format    ReadFormat `json:"format,omitempty"`
	Lines     *uint32    `json:"lines,omitempty"`
	StripANSI *bool      `json:"strip_ansi,omitempty"`
}

// SessionSnapshotResult is the result of session.snapshot.
type SessionSnapshotResult struct {
	Type     string          `json:"type"`
	Snapshot SessionSnapshot `json:"snapshot"`
}

// AgentListResult is the result of agent.list.
type AgentListResult struct {
	Type   string      `json:"type"`
	Agents []AgentInfo `json:"agents"`
}

// AgentGetResult is the result of agent.get.
type AgentGetResult struct {
	Type  string    `json:"type"`
	Agent AgentInfo `json:"agent"`
}

// PaneReadContent is the text read from a pane.
type PaneReadContent struct {
	PaneID      string     `json:"pane_id"`
	WorkspaceID string     `json:"workspace_id"`
	TabID       string     `json:"tab_id"`
	Source      ReadSource `json:"source"`
	Format      ReadFormat `json:"format"`
	Text        string     `json:"text"`
	Revision    uint64     `json:"revision"`
	Truncated   bool       `json:"truncated"`
}

// PaneReadResult is the result of pane.read.
type PaneReadResult struct {
	Type string          `json:"type"`
	Read PaneReadContent `json:"read"`
}

// SubscriptionStartedResult is the result of events.subscribe; events follow.
type SubscriptionStartedResult struct {
	Type string `json:"type"`
}

// ---- Event payloads --------------------------------------------------------
//
// These are the "data" objects of event envelopes. Lifecycle events carry a
// "type" tag equal to the event kind (Event* constants).

// PaneCreatedEvent is the data of EventPaneCreated.
type PaneCreatedEvent struct {
	Type string   `json:"type"`
	Pane PaneInfo `json:"pane"`
}

// PaneUpdatedEvent is the data of EventPaneUpdated.
type PaneUpdatedEvent struct {
	Type string   `json:"type"`
	Pane PaneInfo `json:"pane"`
}

// PaneClosedEvent is the data of EventPaneClosed.
type PaneClosedEvent struct {
	Type        string `json:"type"`
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
}

// PaneExitedEvent is the data of EventPaneExited.
type PaneExitedEvent struct {
	Type        string `json:"type"`
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
}

// PaneMovedEvent is the data of EventPaneMoved. Pane carries the new location;
// the Previous* fields the old one.
type PaneMovedEvent struct {
	Type                string         `json:"type"`
	Pane                PaneInfo       `json:"pane"`
	PreviousPaneID      string         `json:"previous_pane_id"`
	PreviousWorkspaceID string         `json:"previous_workspace_id"`
	PreviousTabID       string         `json:"previous_tab_id"`
	CreatedWorkspace    *WorkspaceInfo `json:"created_workspace,omitempty"`
	CreatedTab          *TabInfo       `json:"created_tab,omitempty"`
	ClosedWorkspaceID   string         `json:"closed_workspace_id,omitempty"`
	ClosedTabID         string         `json:"closed_tab_id,omitempty"`
}

// PaneAgentDetectedEvent is the data of EventPaneAgentDetected.
type PaneAgentDetectedEvent struct {
	Type        string      `json:"type"`
	PaneID      string      `json:"pane_id"`
	WorkspaceID string      `json:"workspace_id"`
	Agent       string      `json:"agent,omitempty"`
	FinalStatus AgentStatus `json:"final_status,omitempty"`
	Released    bool        `json:"released,omitempty"`
}

// PaneAgentStatusChangedEvent is the data of EventPaneAgentStatusChanged
// (Type set) and of the subscription event SubEventPaneAgentStatusChanged
// (Type absent).
type PaneAgentStatusChangedEvent struct {
	Type         string            `json:"type,omitempty"`
	PaneID       string            `json:"pane_id"`
	WorkspaceID  string            `json:"workspace_id"`
	AgentStatus  AgentStatus       `json:"agent_status"`
	Agent        string            `json:"agent,omitempty"`
	DisplayAgent string            `json:"display_agent,omitempty"`
	Title        string            `json:"title,omitempty"`
	StateLabels  map[string]string `json:"state_labels,omitempty"`
}
