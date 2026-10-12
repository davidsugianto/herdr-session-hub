import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  agents, applyChange, applySnapshot, auth, connection, counts, resetStore, sessionFilter,
  sessions, sortedAgents, startSync, stopSync,
} from "./store";
import type { Agent, Change, State } from "./types";

const mk = (id: string, state: State, since: string, over: Partial<Agent> = {}): Agent => {
  const session = over.session ?? id.split("/")[0]!;
  return {
    id, session, session_label: session, terminal_id: id.split("/")[1]!, pane_id: "p", name: id, kind: "claude",
    cwd: "/", title: "", state, raw_status: "", since, online: true, ...over,
  };
};
const change = (type: Change["type"], agent: Agent, session = agent.session): Change =>
  ({ type, session, agent, at: "2026-01-01T00:00:00Z" });
const sessionChange = (type: "session_online" | "session_offline", session: string): Change =>
  ({ type, session, agent: mk("", "unknown", ""), at: "2026-01-01T00:00:00Z" });

beforeEach(() => resetStore());

describe("snapshot", () => {
  it("replaces agents and sessions", () => {
    applySnapshot({ sessions: [{ name: "a", label: "A", online: true, agents: 1 }], agents: [mk("a/1", "working", "2026-01-01T00:00:00Z")] });
    applySnapshot({ sessions: [{ name: "b", label: "B", online: true, agents: 1 }], agents: [mk("b/1", "waiting", "2026-01-01T00:00:00Z")] });
    expect(Object.keys(agents.value)).toEqual(["b/1"]);
    expect(sessions.value.map((s) => s.name)).toEqual(["b"]);
  });
});

describe("applyChange", () => {
  beforeEach(() => {
    applySnapshot({
      sessions: [{ name: "a", label: "A", online: true, agents: 1 }, { name: "b", label: "B", online: true, agents: 1 }],
      agents: [mk("a/1", "working", "2026-01-01T00:00:00Z"), mk("b/1", "waiting", "2026-01-01T00:00:00Z")],
    });
  });

  it("added upserts and bumps the session count", () => {
    applyChange(change("added", mk("a/2", "blocked", "2026-01-02T00:00:00Z")));
    expect(agents.value["a/2"]?.state).toBe("blocked");
    expect(sessions.value.find((s) => s.name === "a")?.agents).toBe(2);
  });

  it("state and updated replace the agent without changing the count", () => {
    applyChange(change("state", mk("a/1", "blocked", "2026-01-03T00:00:00Z")));
    expect(agents.value["a/1"]?.state).toBe("blocked");
    applyChange(change("updated", mk("a/1", "blocked", "2026-01-03T00:00:00Z", { title: "new" })));
    expect(agents.value["a/1"]?.title).toBe("new");
    expect(sessions.value.find((s) => s.name === "a")?.agents).toBe(1);
  });

  it("removed deletes the agent", () => {
    applyChange(change("removed", mk("a/1", "working", "2026-01-01T00:00:00Z")));
    expect(agents.value["a/1"]).toBeUndefined();
    expect(sessions.value.find((s) => s.name === "a")?.agents).toBe(0);
    applyChange(change("removed", mk("a/1", "working", "2026-01-01T00:00:00Z")));
    expect(sessions.value.find((s) => s.name === "a")?.agents).toBe(0);
  });

  it("session_offline / session_online toggle the session and its agents only", () => {
    applyChange(sessionChange("session_offline", "a"));
    expect(sessions.value.find((s) => s.name === "a")?.online).toBe(false);
    expect(agents.value["a/1"]?.online).toBe(false);
    expect(agents.value["b/1"]?.online).toBe(true);
    expect(sessions.value.find((s) => s.name === "b")?.online).toBe(true);
    applyChange(sessionChange("session_online", "a"));
    expect(sessions.value.find((s) => s.name === "a")?.online).toBe(true);
    expect(agents.value["a/1"]?.online).toBe(true);
  });

  it("ignores unknown change types and empty agents", () => {
    const before = agents.value;
    applyChange({ ...change("added", mk("a/9", "working", "x")), type: "bogus" as Change["type"] });
    applyChange(change("added", mk("", "working", "x")));
    expect(agents.value).toBe(before);
  });
});

describe("sortedAgents / counts", () => {
  beforeEach(() => {
    applySnapshot({
      sessions: [],
      agents: [
        mk("a/w1", "working", "2026-01-01T10:00:00Z"),
        mk("a/wa", "waiting", "2026-01-01T12:00:00Z"),
        mk("a/b1", "blocked", "2026-01-01T09:00:00Z"),
        mk("b/b2", "blocked", "2026-01-01T11:00:00Z"),
        mk("b/f1", "finished", "2026-01-01T08:00:00Z"),
        mk("b/u1", "unknown", "2026-01-01T13:00:00Z"),
        mk("a/w2", "working", "2026-01-01T10:30:00Z"),
      ],
    });
  });

  it("orders by state attention then newest first, hiding unknown", () => {
    expect(sortedAgents.value.map((a) => a.id)).toEqual(["b/b2", "a/b1", "b/f1", "a/w2", "a/w1", "a/wa"]);
  });

  it("filters by session and counts over the filtered set", () => {
    expect(counts.value).toEqual({ blocked: 2, finished: 1, working: 2, waiting: 1, unknown: 1 });
    sessionFilter.value = "a";
    expect(sortedAgents.value.map((a) => a.id)).toEqual(["a/b1", "a/w2", "a/w1", "a/wa"]);
    expect(counts.value).toEqual({ blocked: 1, finished: 0, working: 2, waiting: 1, unknown: 0 });
  });
});

describe("startSync", () => {
  class FakeES {
    static instances: FakeES[] = [];
    closed = false;
    readyState = 0;
    listeners: Record<string, ((e: Event) => void)[]> = {};
    constructor(public url: string) { FakeES.instances.push(this); }
    addEventListener(t: string, fn: (e: Event) => void) { (this.listeners[t] ??= []).push(fn); }
    close() { this.closed = true; }
    emit(t: string, data?: string) {
      const e = data === undefined ? new Event(t) : new MessageEvent(t, { data });
      this.listeners[t]?.forEach((fn) => fn(e));
    }
  }
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    FakeES.instances = [];
    vi.stubGlobal("EventSource", FakeES);
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    stopSync();
    vi.unstubAllGlobals();
  });
  const flush = () => new Promise((r) => setTimeout(r, 0));

  it("pairs, opens the stream once, and wires events into the store", async () => {
    fetchMock.mockImplementation(async () => new Response(JSON.stringify({ id: "d" }), { status: 200 }));
    startSync();
    startSync(); // idempotent
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(auth.value).toBe("paired");
    expect(FakeES.instances).toHaveLength(1);
    const es = FakeES.instances[0]!;
    es.emit("open");
    expect(connection.value).toBe("online");
    es.emit("snapshot", JSON.stringify({ sessions: [], agents: [mk("a/1", "working", "2026-01-01T00:00:00Z")] }));
    expect(agents.value["a/1"]).toBeDefined();
    es.emit("change", JSON.stringify(change("removed", mk("a/1", "working", "x"))));
    expect(agents.value["a/1"]).toBeUndefined();
    es.emit("error");
    expect(connection.value).toBe("offline");
    stopSync();
    expect(es.closed).toBe(true);
  });

  it("on 401 sets unpaired and does not open the stream", async () => {
    fetchMock.mockImplementation(async () =>
      new Response(JSON.stringify({ error: { code: "unauthorized", message: "no" } }), { status: 401 }));
    startSync();
    await flush();
    expect(auth.value).toBe("unpaired");
    expect(FakeES.instances).toHaveLength(0);
  });

  it("stopSync before /api/me resolves prevents opening the stream", async () => {
    fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
    startSync();
    stopSync();
    await flush();
    expect(FakeES.instances).toHaveLength(0);
  });

  it("retries /api/me with backoff after a non-401 failure", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockRejectedValueOnce(new TypeError("network"));
      fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
      startSync();
      await vi.advanceTimersByTimeAsync(0);
      expect(connection.value).toBe("offline");
      expect(FakeES.instances).toHaveLength(0);
      await vi.advanceTimersByTimeAsync(1500);
      expect(auth.value).toBe("paired");
      expect(FakeES.instances).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a dead stream (readyState CLOSED) re-checks auth: 401 -> unpaired, stream closed", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementationOnce(async () => new Response("{}", { status: 200 }));
      startSync();
      await vi.advanceTimersByTimeAsync(0);
      const es = FakeES.instances[0]!;
      fetchMock.mockImplementation(async () =>
        new Response(JSON.stringify({ error: { code: "unauthorized", message: "no" } }), { status: 401 }));
      es.readyState = 2;
      es.emit("error");
      await vi.advanceTimersByTimeAsync(1500);
      expect(auth.value).toBe("unpaired");
      expect(es.closed).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a dead stream with a healthy hub reopens the stream", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
      startSync();
      await vi.advanceTimersByTimeAsync(0);
      const es = FakeES.instances[0]!;
      es.readyState = 2;
      es.emit("error");
      await vi.advanceTimersByTimeAsync(1500);
      expect(FakeES.instances.length).toBe(2);
      expect(es.closed).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("auth turning unpaired (e.g. self-revoke) stops the stream", async () => {
    fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
    startSync();
    await flush();
    const es = FakeES.instances[0]!;
    auth.value = "unpaired";
    expect(es.closed).toBe(true);
  });

  it("a stream that keeps dying while /api/me works is retried with growing delays, not in a loop", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
      startSync();
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const die = () => { const es = FakeES.instances.at(-1)!; es.readyState = 2; es.emit("error"); };
      die();
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(1); // no immediate re-attempt
      await vi.advanceTimersByTimeAsync(1000);
      expect(fetchMock).toHaveBeenCalledTimes(2);
      die();
      await vi.advanceTimersByTimeAsync(1000);
      expect(fetchMock).toHaveBeenCalledTimes(2); // second wait is 2s
      await vi.advanceTimersByTimeAsync(1000);
      expect(fetchMock).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("coming back online or to the foreground retries at once instead of waiting out the backoff", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockRejectedValue(new TypeError("network"));
      startSync();
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(1000); // 2nd failure, backoff now 4s
      const before = fetchMock.mock.calls.length;
      fetchMock.mockImplementation(async () => new Response("{}", { status: 200 }));
      window.dispatchEvent(new Event("online"));
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock.mock.calls.length).toBe(before + 1);
      expect(FakeES.instances).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a stale /api/me from a stopped sync does not clear the new sync's in-flight flag", async () => {
    vi.useFakeTimers();
    try {
      const resolvers: ((r: Response) => void)[] = [];
      fetchMock.mockImplementation(() => new Promise<Response>((res) => { resolvers.push(res); }));
      startSync(); // check #1 in flight
      stopSync();
      startSync(); // check #2 in flight
      expect(fetchMock).toHaveBeenCalledTimes(2);
      resolvers[0]!(new Response("{}", { status: 200 })); // the old one settles first
      await vi.advanceTimersByTimeAsync(0);
      window.dispatchEvent(new Event("online")); // must not start a third, concurrent check
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
