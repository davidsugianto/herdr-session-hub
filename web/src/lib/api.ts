import type { AgentDetail, Device, Mute, MuteScope, NotifyInfo, TimelineEvent } from "./types";
import { auth } from "./auth";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

// Slightly above the server's own timeouts (10 s reads, 15 s actions) so its 504 wins the race.
const READ_TIMEOUT_MS = 15_000;
const ACTION_TIMEOUT_MS = 20_000;

interface Opts {
  method?: string;
  body?: unknown;
  /** Do not flip auth to "unpaired" on 401 (used by POST /api/pair). */
  keepAuth?: boolean;
}

export async function request<T>(path: string, opts: Opts = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const init: RequestInit = { method, credentials: "same-origin" };
  if (method !== "GET") {
    init.headers = { "X-Requested-With": "hub", "Content-Type": "application/json" };
    init.body = JSON.stringify(opts.body ?? {});
  }
  // A hung mobile connection must not wedge callers that wait on a request (retries, polling).
  const ctl = new AbortController();
  init.signal = ctl.signal;
  const timer = setTimeout(() => ctl.abort(), method === "GET" ? READ_TIMEOUT_MS : ACTION_TIMEOUT_MS);
  try {
    return await send<T>(path, init, opts);
  } catch (e) {
    if (ctl.signal.aborted && !(e instanceof ApiError)) {
      // Our own timer fired: replace the browser's "operation was aborted" with something readable.
      throw new ApiError(0, "timeout", method === "GET"
        ? "Request timed out"
        : "Request timed out; it may have been applied, check the board before retrying");
    }
    throw e;
  } finally {
    clearTimeout(timer);
  }
}

async function send<T>(path: string, init: RequestInit, opts: Opts): Promise<T> {
  const res = await fetch(path, init);
  if (!res.ok) {
    let code = "internal";
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const j = await res.json();
      if (j?.error) {
        code = j.error.code ?? code;
        message = j.error.message ?? message;
      }
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && !opts.keepAuth) auth.value = "unpaired";
    throw new ApiError(res.status, code, message);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

const agentPath = (id: string, suffix = "") => `/api/agents/${encodeURIComponent(id)}${suffix}`;
const post = async (path: string, body: unknown = {}): Promise<void> => {
  await request<unknown>(path, { method: "POST", body });
};

export const getMe = (): Promise<Device> => request<Device>("/api/me");
export const getAgentDetail = (id: string): Promise<AgentDetail> => request<AgentDetail>(agentPath(id));
/** Raw pane text; default 300 lines. */
export const getOutput = async (id: string, lines = 300): Promise<string> =>
  (await request<{ text: string }>(agentPath(id, `/output?lines=${lines}`))).text;
export const getTimeline = (since = "2h", session?: string): Promise<TimelineEvent[]> => {
  const q = new URLSearchParams({ since });
  if (session) q.set("session", session);
  return request<TimelineEvent[]>(`/api/timeline?${q}`);
};
export const getDevices = (): Promise<Device[]> => request<Device[]>("/api/devices");
export const revokeDevice = (id: string): Promise<{ ok: boolean; warning?: string }> =>
  request<{ ok: boolean; warning?: string }>(`/api/devices/${encodeURIComponent(id)}`, { method: "DELETE" });

export const approve = (id: string, hash: string): Promise<void> =>
  post(agentPath(id, "/approve"), { prompt_hash: hash });
export const trust = (id: string, hash: string): Promise<void> =>
  post(agentPath(id, "/trust"), { prompt_hash: hash });
export const deny = (id: string, hash: string, note = ""): Promise<void> =>
  post(agentPath(id, "/deny"), { prompt_hash: hash, note });
export const reply = (id: string, text: string): Promise<void> => post(agentPath(id, "/reply"), { text });
export const sendText = (id: string, text: string, enter: boolean): Promise<void> =>
  post(agentPath(id, "/send"), { text, enter });
export const sendKeys = (id: string, keys: string[]): Promise<void> => post(agentPath(id, "/keys"), { keys });
export const interrupt = (id: string, force = false): Promise<void> => post(agentPath(id, "/interrupt"), { force });
export const markSeen = (id: string): Promise<void> => post(agentPath(id, "/seen"), {});

export const getPushKey = async (): Promise<string> => (await request<{ public_key: string }>("/api/push/key")).public_key;
export const pushSubscribe = (subscription: unknown): Promise<void> => post("/api/push/subscribe", { subscription });
export const pushUnsubscribe = async (endpoint: string): Promise<void> => {
  await request<unknown>("/api/push/subscribe", { method: "DELETE", body: { endpoint } });
};
export const pushTest = (): Promise<void> => post("/api/push/test");
export const getMutes = async (): Promise<Mute[]> => (await request<{ mutes: Mute[] }>("/api/mutes")).mutes ?? [];
export const putMute = async (scope: MuteScope, key: string, until: string | null = null): Promise<void> => {
  await request<unknown>("/api/mutes", { method: "PUT", body: { scope, key, until } });
};
export const deleteMute = async (scope: MuteScope, key: string): Promise<void> => {
  await request<unknown>("/api/mutes", { method: "DELETE", body: { scope, key } });
};
export const getNotify = (): Promise<NotifyInfo> => request<NotifyInfo>("/api/notify");
