import type { Change, Snapshot } from "./types";

export interface StreamHandlers {
  onSnapshot(s: Snapshot): void;
  onChange(c: Change): void;
  onStatus(s: "connecting" | "online" | "offline"): void;
  /** The browser gave up on the stream (non-200 reply, e.g. a revoked device): it will not reconnect by itself. */
  onDead?(): void;
}

const parse = <T>(e: Event): T | null => {
  try {
    const v = JSON.parse((e as MessageEvent).data);
    return v && typeof v === "object" ? (v as T) : null;
  } catch {
    return null;
  }
};

/** Opens /api/stream with EventSource; returns a close function. */
export function openStream(h: StreamHandlers): () => void {
  let es: EventSource | null = null;
  let closed = false;

  const connect = () => {
    es?.close();
    h.onStatus("connecting");
    const src = new EventSource("/api/stream");
    es = src;
    src.addEventListener("open", () => h.onStatus("online"));
    src.addEventListener("error", () => {
      h.onStatus("offline");
      if (src.readyState === 2 /* CLOSED */ && !closed) h.onDead?.();
    });
    src.addEventListener("snapshot", (e) => {
      const s = parse<Snapshot>(e);
      if (s) h.onSnapshot(s);
    });
    src.addEventListener("change", (e) => {
      const c = parse<Change>(e);
      if (c) h.onChange(c);
    });
  };

  // iOS suspends background streams; a fresh connection yields a fresh snapshot.
  const reconnect = () => {
    if (!closed) connect();
  };
  const onVisible = () => {
    if (document.visibilityState === "visible") reconnect();
  };
  document.addEventListener("visibilitychange", onVisible);
  window.addEventListener("online", reconnect);

  connect();

  return () => {
    closed = true;
    document.removeEventListener("visibilitychange", onVisible);
    window.removeEventListener("online", reconnect);
    es?.close();
    es = null;
  };
}
