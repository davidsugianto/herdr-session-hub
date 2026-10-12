import type { State } from "./types";

export const relTime = (iso: string, now: number = Date.now()): string => {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.floor((now - t) / 1000);
  if (s < 60) return "just now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
};

const LABELS: Record<State, string> = {
  blocked: "Blocked",
  finished: "Finished",
  working: "Working",
  waiting: "Waiting",
  unknown: "Unknown",
};
export const stateLabel = (s: State): string => LABELS[s] ?? s;

/** Stable colour token name for a session name, e.g. "s1".."s6" (CSS var --session-N). */
export const sessionColor = (name: string): string => {
  let h = 5381;
  for (let i = 0; i < name.length; i++) h = (Math.imul(h, 33) + name.charCodeAt(i)) >>> 0;
  return `s${(h % 6) + 1}`;
};
