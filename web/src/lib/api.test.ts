import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import { auth, resetStore } from "./store";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
  resetStore();
  fetchMock = vi.fn(async () => json({}));
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const lastCall = () => {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit];
  return { url, init, headers: (init.headers ?? {}) as Record<string, string> };
};

describe("api", () => {
  it("sends no CSRF header on GET and uses same-origin credentials", async () => {
    fetchMock.mockResolvedValueOnce(json({ id: "d1" }));
    await api.getMe();
    const { url, init, headers } = lastCall();
    expect(url).toBe("/api/me");
    expect(init.method).toBe("GET");
    expect(init.credentials).toBe("same-origin");
    expect(headers["X-Requested-With"]).toBeUndefined();
    expect(init.body).toBeUndefined();
  });

  it("sends CSRF + JSON headers on POST and DELETE", async () => {
    await api.approve("labs/t1", "h");
    let c = lastCall();
    expect(c.init.method).toBe("POST");
    expect(c.headers["X-Requested-With"]).toBe("hub");
    expect(c.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(c.init.body as string)).toEqual({ prompt_hash: "h" });

    fetchMock.mockResolvedValueOnce(json({ ok: true, warning: "w" }));
    await expect(api.revokeDevice("dev 1")).resolves.toEqual({ ok: true, warning: "w" });
    c = lastCall();
    expect(c.url).toBe("/api/devices/dev%201");
    expect(c.init.method).toBe("DELETE");
    expect(c.headers["X-Requested-With"]).toBe("hub");
  });

  it("encodes agent ids in paths", async () => {
    await api.getAgentDetail("labs/term_abc");
    expect(lastCall().url).toBe("/api/agents/labs%2Fterm_abc");
    fetchMock.mockResolvedValueOnce(json({ text: "hello" }));
    await expect(api.getOutput("labs/term_abc", 50)).resolves.toBe("hello");
    expect(lastCall().url).toBe("/api/agents/labs%2Fterm_abc/output?lines=50");
    fetchMock.mockResolvedValueOnce(json({ text: "x" }));
    await api.getOutput("labs/term_abc");
    expect(lastCall().url).toBe("/api/agents/labs%2Fterm_abc/output?lines=300");
  });

  it("posts the right body for each action", async () => {
    const body = () => JSON.parse(lastCall().init.body as string);
    await api.trust("a/b", "h");
    expect(lastCall().url).toBe("/api/agents/a%2Fb/trust");
    expect(body()).toEqual({ prompt_hash: "h" });
    await api.deny("a/b", "h", "no");
    expect(lastCall().url).toBe("/api/agents/a%2Fb/deny");
    expect(body()).toEqual({ prompt_hash: "h", note: "no" });
    await api.reply("a/b", "hi");
    expect(lastCall().url).toBe("/api/agents/a%2Fb/reply");
    expect(body()).toEqual({ text: "hi" });
    await api.sendText("a/b", "ls", true);
    expect(lastCall().url).toBe("/api/agents/a%2Fb/send");
    expect(body()).toEqual({ text: "ls", enter: true });
    await api.sendKeys("a/b", ["Up", "Enter"]);
    expect(lastCall().url).toBe("/api/agents/a%2Fb/keys");
    expect(body()).toEqual({ keys: ["Up", "Enter"] });
    await api.interrupt("a/b");
    expect(lastCall().url).toBe("/api/agents/a%2Fb/interrupt");
    expect(body()).toEqual({ force: false });
    await api.interrupt("a/b", true);
    expect(body()).toEqual({ force: true });
    await api.markSeen("a/b");
    expect(lastCall().url).toBe("/api/agents/a%2Fb/seen");
    expect(body()).toEqual({});
  });

  it("builds timeline and devices requests", async () => {
    fetchMock.mockImplementation(async () => json([]));
    await api.getTimeline();
    expect(lastCall().url).toBe("/api/timeline?since=2h");
    await api.getTimeline("30m", "my sess");
    expect(lastCall().url).toBe("/api/timeline?since=30m&session=my+sess");
    await api.getDevices();
    expect(lastCall().url).toBe("/api/devices");
  });

  it("maps error bodies to ApiError", async () => {
    fetchMock.mockResolvedValueOnce(json({ error: { code: "prompt_changed", message: "changed" } }, 409));
    const err = await api.approve("a/b", "h").catch((e) => e);
    expect(err).toBeInstanceOf(api.ApiError);
    expect(err).toMatchObject({ status: 409, code: "prompt_changed", message: "changed" });
    expect(auth.value).toBe("unknown");
  });

  it("falls back to a generic ApiError for non-JSON errors", async () => {
    fetchMock.mockResolvedValueOnce(new Response("boom", { status: 502 }));
    const err = await api.getDevices().catch((e) => e);
    expect(err).toBeInstanceOf(api.ApiError);
    expect(err.status).toBe(502);
    expect(err.code).toBe("internal");
  });

  it("sets auth to unpaired on 401", async () => {
    auth.value = "paired";
    fetchMock.mockResolvedValueOnce(json({ error: { code: "unauthorized", message: "no" } }, 401));
    await expect(api.getMe()).rejects.toMatchObject({ status: 401, code: "unauthorized" });
    expect(auth.value).toBe("unpaired");
  });
});

describe("request timeout", () => {
  const hang = () =>
    fetchMock.mockImplementation((_url: string, init: RequestInit) =>
      new Promise((_res, rej) => {
        init.signal?.addEventListener("abort", () => rej(new DOMException("This operation was aborted", "AbortError")));
      }));

  it("a GET that never settles fails with a readable timeout ApiError after 15 s", async () => {
    vi.useFakeTimers();
    try {
      hang();
      const settled = api.getMe().then(() => null, (e: unknown) => e);
      await vi.advanceTimersByTimeAsync(14_900);
      let done = false;
      void settled.then(() => { done = true; });
      await vi.advanceTimersByTimeAsync(0);
      expect(done).toBe(false);
      await vi.advanceTimersByTimeAsync(200);
      const e = (await settled) as InstanceType<typeof api.ApiError>;
      expect(e).toBeInstanceOf(api.ApiError);
      expect(e.code).toBe("timeout");
      expect(e.message).toBe("Request timed out");
    } finally {
      vi.useRealTimers();
    }
  });

  it("a POST gets 20 s, and its timeout says the action may have been applied", async () => {
    vi.useFakeTimers();
    try {
      hang();
      const settled = api.reply("s/1", "hi").then(() => null, (e: unknown) => e);
      let done = false;
      void settled.then(() => { done = true; });
      await vi.advanceTimersByTimeAsync(16_000);
      expect(done).toBe(false); // would be aborted already if POST used the 15 s read limit
      await vi.advanceTimersByTimeAsync(4_100);
      const e = (await settled) as InstanceType<typeof api.ApiError>;
      expect(e).toBeInstanceOf(api.ApiError);
      expect(e.code).toBe("timeout");
      expect(e.message).toMatch(/may have been applied/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not abort a request that finished in time", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockResolvedValueOnce(json({ id: "d1" }));
      await api.getMe();
      const signal = (fetchMock.mock.calls.at(-1) as [string, RequestInit])[1].signal;
      await vi.advanceTimersByTimeAsync(20_000);
      expect(signal?.aborted).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
