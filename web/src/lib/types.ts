// Contract: mirrors the Go JSON in internal/model, internal/api, internal/actions,
// internal/profiles and internal/store. Frozen for milestone 2; change only with the orchestrator.

export type State = "blocked" | "finished" | "working" | "waiting" | "unknown";

export interface Agent {
  id: string; // "<session>/<terminal_id>"
  session: string;
  session_label: string;
  terminal_id: string;
  pane_id: string;
  name: string;
  kind: string; // "claude" | "pi" | "omp" | ...
  cwd: string;
  title: string;
  state: State;
  raw_status: string;
  since: string; // RFC3339
  online: boolean;
}

export interface SessionInfo {
  name: string;
  label: string;
  online: boolean;
  agents: number;
}

export type ChangeType = "added" | "state" | "updated" | "removed" | "session_online" | "session_offline";

export interface Change {
  type: ChangeType;
  session: string;
  agent: Agent; // zero-valued (id "") for session_* changes
  from?: State;
  to?: State;
  new_episode?: boolean;
  at: string;
}

export interface Snapshot {
  sessions: SessionInfo[];
  agents: Agent[];
}

export interface Prompt {
  text: string;
  options: string[];
  hash: string;
  trust_rule?: string;
}

export interface PromptView {
  prompt: Prompt | null;
  profile: string;
  verified: boolean;
}

export interface AgentDetail extends Agent {
  last_lines?: string[];
  prompt?: PromptView;
}

export interface TimelineEvent {
  id: number;
  session: string;
  agent_id: string;
  name: string;
  from: string; // "" for first sighting
  to: string; // a State or "removed"
  at: string;
  source: string; // "herdr" | "hub"
}

export interface Device {
  id: string;
  name: string;
  created_at: string;
  last_seen: string;
}

export type MuteScope = "agent" | "session";

export interface Mute {
  scope: MuteScope;
  key: string;
  until: string | null; // RFC3339; null = until unmuted
  created_at: string;
}

export interface NotifyInfo {
  quiet_hours: { session: string; rule: string }[];
  blocked_breaks_quiet: boolean;
  push_available: boolean;
}

export type Connection = "connecting" | "online" | "offline";
export type AuthState = "unknown" | "paired" | "unpaired";

/** Error codes the API returns that the UI treats specially. */
export type ApiErrorCode =
  | "unauthorized" | "csrf" | "invalid_code" | "prompt_changed" | "not_blocked" | "no_prompt"
  | "agent_blocked" | "action_unavailable" | "session_offline" | "agent_not_found"
  | "rate_limited" | "bad_request" | "timeout" | "internal" | string;
