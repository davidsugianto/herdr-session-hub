import { signal, computed, effect } from "@preact/signals";
import type { Agent, Change, Connection, SessionInfo, Snapshot, State } from "./types";
import { auth } from "./auth";
import { getMe, ApiError } from "./api";
import { openStream } from "./sse";

export const agents = signal<Record<string, Agent>>({});
export const sessions = signal<SessionInfo[]>([]);
export const connection = signal<Connection>("connecting");
export { auth };
/** "" means all sessions. */
export const sessionFilter = signal<string>("");
/** Agent last opened (keys tab target). */
export const lastAgentId = signal<string | null>(null);

const RANK: Record<State, number> = { blocked: 0, finished: 1, working: 2, waiting: 3, unknown: 4 };
const ts = (a: Agent): number => {
  const t = Date.parse(a.since);
  return Number.isNaN(t) ? 0 : t;
};

const filtered = computed<Agent[]>(() => {
  const f = sessionFilter.value;
  const all = Object.values(agents.value);
  return f ? all.filter((a) => a.session === f) : all;
});

/** Attention order: blocked, finished, working, waiting; then newest `since` first. Respects sessionFilter. Hides unknown. */
export const sortedAgents = computed<Agent[]>(() =>
  filtered.value
    .filter((a) => a.state !== "unknown")
    .sort((a, b) => RANK[a.state] - RANK[b.state] || ts(b) - ts(a)),
);

export const counts = computed<Record<State, number>>(() => {
  const c: Record<State, number> = { blocked: 0, finished: 0, working: 0, waiting: 0, unknown: 0 };
  for (const a of filtered.value) c[a.state] = (c[a.state] ?? 0) + 1;
  return c;
});

export function applySnapshot(s: Snapshot): void {
  const map: Record<string, Agent> = {};
  for (const a of s.agents) map[a.id] = a;
  agents.value = map;
  sessions.value = s.sessions;
}

const setSessionOnline = (name: string, online: boolean) => {
  sessions.value = sessions.value.map((x) => (x.name === name ? { ...x, online } : x));
  const next: Record<string, Agent> = {};
  for (const [id, a] of Object.entries(agents.value)) next[id] = a.session === name ? { ...a, online } : a;
  agents.value = next;
};

const bumpSessionCount = (name: string, delta: number) => {
  sessions.value = sessions.value.map((x) =>
    x.name === name ? { ...x, agents: Math.max(0, x.agents + delta) } : x,
  );
};

export function applyChange(c: Change): void {
  switch (c.type) {
    case "added":
    case "state":
    case "updated": {
      if (!c.agent?.id) return;
      if (!(c.agent.id in agents.value)) bumpSessionCount(c.agent.session, 1);
      agents.value = { ...agents.value, [c.agent.id]: c.agent };
      return;
    }
    case "removed": {
      if (!c.agent?.id || !(c.agent.id in agents.value)) return;
      const { [c.agent.id]: _gone, ...rest } = agents.value;
      agents.value = rest;
      bumpSessionCount(c.agent.session, -1);
      return;
    }
    case "session_online":
      setSessionOnline(c.session, true);
      return;
    case "session_offline":
      setSessionOnline(c.session, false);
      return;
    default:
      return;
  }
}

let stopStream: (() => void) | null = null;
let started = false;
let generation = 0;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let backoff = 1000;
let busy = false; // a /api/me check is in flight
const MAX_BACKOFF = 30000;

/** Checks auth (GET /api/me), then opens the SSE stream. Retries with backoff when the hub or the stream is unavailable. */
function attempt(gen: number): void {
  busy = true;
  getMe().then(
    () => {
      if (gen !== generation) return; // stopSync() already reset the flag; a newer check owns it now
      busy = false;
      auth.value = "paired";
      stopStream?.();
      stopStream = openStream({
        onSnapshot: applySnapshot,
        onChange: applyChange,
        onStatus: (s) => {
          connection.value = s;
          if (s === "online") backoff = 1000; // only a working stream resets the delay
        },
        // The browser will not reconnect a stream that got a non-200 reply: wait, re-check auth, then reopen.
        onDead: () => {
          if (gen !== generation) return;
          stopStream?.();
          stopStream = null;
          scheduleRetry(gen);
        },
      });
    },
    (e: unknown) => {
      if (gen !== generation) return;
      busy = false;
      if (e instanceof ApiError && e.status === 401) {
        auth.value = "unpaired"; // the effect below stops the sync
        return;
      }
      connection.value = "offline";
      scheduleRetry(gen);
    },
  );
}

function scheduleRetry(gen: number): void {
  clearTimeout(retryTimer);
  retryTimer = setTimeout(() => attempt(gen), backoff);
  backoff = Math.min(backoff * 2, MAX_BACKOFF);
}

// Back in the foreground or online again: do not wait out a long backoff.
const kick = () => {
  if (!started || busy || stopStream || document.visibilityState !== "visible") return;
  clearTimeout(retryTimer);
  attempt(generation);
};
document.addEventListener("visibilitychange", kick);
window.addEventListener("online", kick);

/** Idempotent. */
export function startSync(): void {
  if (started) return;
  started = true;
  backoff = 1000;
  attempt(++generation);
}

export function stopSync(): void {
  generation++;
  started = false;
  busy = false;
  clearTimeout(retryTimer);
  stopStream?.();
  stopStream = null;
}

// Whatever flips auth to "unpaired" (401, self-revoke) must also stop the stream.
effect(() => {
  if (auth.value === "unpaired") stopSync();
});

/** Test helper: back to initial state. */
export function resetStore(): void {
  stopSync();
  agents.value = {};
  sessions.value = [];
  connection.value = "connecting";
  auth.value = "unknown";
  sessionFilter.value = "";
  lastAgentId.value = null;
}
