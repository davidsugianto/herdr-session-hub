// Pure timeline model: events -> per-agent state segments, and feed text. No DOM, no signals.
import type { TimelineEvent } from "../lib/types";

export type RangeKey = "1h" | "2h" | "6h" | "24h";
export const RANGES: readonly RangeKey[] = ["1h", "2h", "6h", "24h"];
export const RANGE_MS: Record<RangeKey, number> = {
  "1h": 3_600_000,
  "2h": 7_200_000,
  "6h": 21_600_000,
  "24h": 86_400_000,
};

const STATES = new Set(["blocked", "finished", "working", "waiting", "unknown"]);

/** One interval of a single state, in epoch ms, always inside [windowStart, now]. */
export interface Segment {
  state: string;
  start: number;
  end: number;
}

export interface AgentTrack {
  agentId: string;
  name: string;
  session: string;
  segments: Segment[];
}

/** The slice of an Agent the model needs, so tests do not build full agents. */
export interface CurrentAgent {
  id: string;
  name: string;
  session: string;
  state: string;
  since: string;
}

const ts = (iso: string): number => {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? NaN : t;
};

/**
 * Builds one track per agent for the window [now - rangeMs, now].
 *
 * - Only herdr-sourced events change state; hub (user action) events are ignored here.
 * - The first in-window event's `from` gives the state before it (when known).
 * - The last segment extends to `now`; a `removed` event ends the segment and leaves a gap.
 * - `current` (optional) adds a full-width track for agents with no events in the window.
 */
export function buildTracks(
  events: readonly TimelineEvent[],
  now: number,
  rangeMs: number,
  current: readonly CurrentAgent[] = [],
): AgentTrack[] {
  const winStart = now - rangeMs;
  const sorted = events
    .filter((e) => e.source !== "hub" && !Number.isNaN(ts(e.at)))
    .slice()
    .sort((a, b) => ts(a.at) - ts(b.at) || a.id - b.id);

  interface Cursor {
    track: AgentTrack;
    state: string | null;
    start: number;
  }
  const cursors = new Map<string, Cursor>();
  const order: Cursor[] = [];

  const close = (c: Cursor, at: number): void => {
    if (c.state === null) return;
    const start = Math.max(c.start, winStart);
    const end = Math.min(at, now);
    if (end > start) c.track.segments.push({ state: c.state, start, end });
  };

  for (const e of sorted) {
    const at = ts(e.at);
    let c = cursors.get(e.agent_id);
    if (!c) {
      c = {
        track: { agentId: e.agent_id, name: e.name, session: e.session, segments: [] },
        // The agent existed before its first in-window event: its previous state fills the gap.
        state: STATES.has(e.from) ? e.from : null,
        start: winStart,
      };
      cursors.set(e.agent_id, c);
      order.push(c);
    }
    c.track.name = e.name || c.track.name;
    close(c, at);
    c.state = e.to === "removed" || !STATES.has(e.to) ? null : e.to;
    c.start = at;
  }
  for (const c of order) close(c, now);

  const tracks = order.map((c) => c.track);

  for (const a of current) {
    if (cursors.has(a.id)) continue;
    const since = ts(a.since);
    const start = Number.isNaN(since) ? winStart : Math.max(since, winStart);
    if (now > start) {
      tracks.push({
        agentId: a.id,
        name: a.name,
        session: a.session,
        segments: [{ state: STATES.has(a.state) ? a.state : "unknown", start, end: now }],
      });
    }
  }

  return tracks
    .filter((t) => t.segments.length > 0)
    .sort((a, b) => a.session.localeCompare(b.session) || a.name.localeCompare(b.name) || a.agentId.localeCompare(b.agentId));
}

export interface FeedLine {
  /** "You" for hub-sourced events; empty otherwise. */
  actor: string;
  text: string;
}

const pad = (n: number): string => (n < 10 ? "0" : "") + n;

/** Local HH:MM. */
export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Feed wording for one event. */
export function describeEvent(e: TimelineEvent): FeedLine {
  const name = e.name || e.agent_id;
  if (e.source === "hub") {
    // Hub events record user actions; `to` is the action (approved, replied, interrupted, ...).
    const what = e.to && e.to !== "removed" ? e.to : "acted on";
    return { actor: "You", text: `${what} ${name}` };
  }
  switch (e.to) {
    case "removed":
      return { actor: "", text: `${name} removed` };
    case "finished":
      return { actor: "", text: `${name} finished` };
    case "working":
      return { actor: "", text: `${name} started working` };
    default:
      return { actor: "", text: `${name} became ${e.to}` };
  }
}

/** Newest first (stable by id for equal timestamps). */
export function feedOrder(events: readonly TimelineEvent[]): TimelineEvent[] {
  return events.slice().sort((a, b) => ts(b.at) - ts(a.at) || b.id - a.id);
}

/** Evenly spaced axis ticks (including both ends) for the window. */
export function axisTicks(now: number, rangeMs: number, count = 3): number[] {
  const n = Math.max(2, count);
  const start = now - rangeMs;
  return Array.from({ length: n }, (_, i) => start + (rangeMs * i) / (n - 1));
}
