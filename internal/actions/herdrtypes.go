package actions

// Method names used by the executor that the herdr package does not define.
const (
	methodAgentPrompt  = "agent.prompt"
	methodPaneSendText = "pane.send_text"
	methodPaneSendKeys = "pane.send_keys"
	herdrCodeBlocked   = "agent_blocked"
	herdrCodeAgentGone = "agent_not_found"
	herdrCodePaneGone  = "pane_not_found"
)

// agentPromptParams is the params object of agent.prompt (schema:
// AgentPromptParams). The optional wait option is never set.
type agentPromptParams struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

// paneSendTextParams is the params object of pane.send_text.
type paneSendTextParams struct {
	PaneID string `json:"pane_id"`
	Text   string `json:"text"`
}

// paneSendKeysParams is the params object of pane.send_keys.
type paneSendKeysParams struct {
	PaneID string   `json:"pane_id"`
	Keys   []string `json:"keys"`
}

// ackResult decodes the loosely-typed results of the send/prompt methods; the
// executor only needs to know the call succeeded.
type ackResult struct {
	Type string `json:"type"`
}
