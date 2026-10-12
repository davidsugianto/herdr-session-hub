import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openStream as rawOpen } from "./sse";

const closers: (() => void)[] = [];
const openStream: typeof rawOpen = (h) => {
  const c = rawOpen(h);
  closers.push(c);
  return c;
};

class FakeES {
  static instances: FakeES[] = [];
  closed = false;
  listeners: Record<string, ((e: Event) => void)[]> = {};
  constructor(public url: string) {
    FakeES.instances.push(this);
  }
  addEventListener(t: string, fn: (e: Event) => void) {
    (this.listeners[t] ??= []).push(fn);
  }
  close() {
    this.closed = true;
  }
  emit(t: string, data?: string) {
    const e = data === undefined ? new Event(t) : new MessageEvent(t, { data });
    this.listeners[t]?.forEach((fn) => fn(e));
  }
}

const handlers = () => ({ onSnapshot: vi.fn(), onChange: vi.fn(), onStatus: vi.fn() });
const cur = () => FakeES.instances.at(-1)!;
const setVisibility = (v: "visible" | "hidden") => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => v });
  document.dispatchEvent(new Event("visibilitychange"));
};

beforeEach(() => {
  FakeES.instances = [];
  vi.stubGlobal("EventSource", FakeES);
});
afterEach(() => {
  closers.splice(0).forEach((c) => c());
  setVisibility("visible");
  vi.unstubAllGlobals();
});

describe("openStream", () => {
  it("connects to /api/stream and reports status transitions", () => {
    const h = handlers();
    openStream(h);
    expect(cur().url).toBe("/api/stream");
    expect(h.onStatus).toHaveBeenLastCalledWith("connecting");
    cur().emit("open");
    expect(h.onStatus).toHaveBeenLastCalledWith("online");
    cur().emit("error");
    expect(h.onStatus).toHaveBeenLastCalledWith("offline");
    cur().emit("open");
    expect(h.onStatus).toHaveBeenLastCalledWith("online");
  });

  it("parses snapshot and change events and ignores malformed data", () => {
    const h = handlers();
    openStream(h);
    const snap = { sessions: [], agents: [] };
    cur().emit("snapshot", JSON.stringify(snap));
    expect(h.onSnapshot).toHaveBeenCalledWith(snap);
    const ch = { type: "added", session: "s", agent: {}, at: "t" };
    cur().emit("change", JSON.stringify(ch));
    expect(h.onChange).toHaveBeenCalledWith(ch);
    cur().emit("snapshot", "{not json");
    cur().emit("change", "null");
    cur().emit("change", "42");
    expect(h.onSnapshot).toHaveBeenCalledTimes(1);
    expect(h.onChange).toHaveBeenCalledTimes(1);
  });

  it("reopens on visibilitychange to visible, not when hidden", () => {
    const h = handlers();
    openStream(h);
    const first = cur();
    setVisibility("hidden");
    expect(FakeES.instances).toHaveLength(1);
    setVisibility("visible");
    expect(first.closed).toBe(true);
    expect(FakeES.instances).toHaveLength(2);
    expect(h.onStatus).toHaveBeenLastCalledWith("connecting");
  });

  it("reopens on window online", () => {
    openStream(handlers());
    const first = cur();
    window.dispatchEvent(new Event("online"));
    expect(first.closed).toBe(true);
    expect(FakeES.instances).toHaveLength(2);
  });

  it("close() closes the source and removes listeners", () => {
    const h = handlers();
    const close = openStream(h);
    const first = cur();
    close();
    expect(first.closed).toBe(true);
    window.dispatchEvent(new Event("online"));
    setVisibility("visible");
    expect(FakeES.instances).toHaveLength(1);
  });
});

describe("openStream onDead", () => {
  it("fires when the source is CLOSED after an error, not on a retrying error", () => {
    const h = { ...handlers(), onDead: vi.fn() };
    openStream(h);
    (cur() as unknown as { readyState: number }).readyState = 0; // CONNECTING: browser is retrying
    cur().emit("error");
    expect(h.onDead).not.toHaveBeenCalled();
    (cur() as unknown as { readyState: number }).readyState = 2;
    cur().emit("error");
    expect(h.onDead).toHaveBeenCalledTimes(1);
  });
});
