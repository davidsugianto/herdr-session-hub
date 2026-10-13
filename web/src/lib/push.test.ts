import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearMute, enablePush, getPushState, setBadge, setMute, disablePush } from "./push";

const jsonRes = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const setUA = (ua: string) => Object.defineProperty(navigator, "userAgent", { value: ua, configurable: true });
const setStandalone = (v: boolean) =>
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: v && q.includes("standalone") }));

function stubPush(existing: unknown = null) {
  const sub = { endpoint: "https://push.example/1", toJSON: () => ({ endpoint: "https://push.example/1", keys: { p256dh: "a", auth: "b" } }), unsubscribe: vi.fn().mockResolvedValue(true) };
  const pushManager = { getSubscription: vi.fn().mockResolvedValue(existing), subscribe: vi.fn().mockResolvedValue(sub) };
  const reg = { pushManager };
  Object.defineProperty(navigator, "serviceWorker", { value: { ready: Promise.resolve(reg), getRegistration: vi.fn().mockResolvedValue(reg) }, configurable: true });
  vi.stubGlobal("PushManager", class {});
  const N = { permission: "default", requestPermission: vi.fn().mockImplementation(async () => { N.permission = "granted"; return "granted"; }) };
  vi.stubGlobal("Notification", N);
  return { sub, pushManager, N };
}

let fetchMock: ReturnType<typeof vi.fn>;
const call = (n: number): [string, RequestInit & { body: string }] => fetchMock.mock.calls[n] as [string, RequestInit & { body: string }];
beforeEach(() => {
  setUA("Mozilla/5.0 (Macintosh)");
  setStandalone(false);
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, "serviceWorker");
});

describe("getPushState", () => {
  it("reports unsupported without push APIs instead of throwing", async () => {
    expect((await getPushState()).status).toBe("unsupported");
  });

  it("asks iOS Safari users to install to the Home Screen", async () => {
    setUA("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)");
    stubPush();
    expect((await getPushState()).status).toBe("needs-install");
  });

  it("is off when permitted but not subscribed, on when subscribed, denied when blocked", async () => {
    const s = stubPush();
    expect((await getPushState()).status).toBe("off");
    s.N.permission = "granted";
    expect((await getPushState()).status).toBe("off");
    s.pushManager.getSubscription.mockResolvedValue(s.sub);
    expect((await getPushState()).status).toBe("on");
    s.N.permission = "denied";
    expect((await getPushState()).status).toBe("denied");
  });
});

describe("enablePush", () => {
  it("requests permission, subscribes with the VAPID key and posts the subscription", async () => {
    const s = stubPush();
    fetchMock.mockResolvedValueOnce(jsonRes({ public_key: "AQID" })).mockResolvedValueOnce(jsonRes({ ok: true }));
    const st = await enablePush();
    expect(st.status).toBe("on");
    expect(s.N.requestPermission).toHaveBeenCalled();
    const opts = s.pushManager.subscribe.mock.calls[0]![0];
    expect(opts.userVisibleOnly).toBe(true);
    expect(Array.from(opts.applicationServerKey)).toEqual([1, 2, 3]);
    const [path, init] = call(1);
    expect(path).toBe("/api/push/subscribe");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ subscription: s.sub.toJSON() });
  });

  it("rolls back the browser subscription when the hub rejects it", async () => {
    const s = stubPush();
    fetchMock.mockResolvedValueOnce(jsonRes({ public_key: "AQID" })).mockResolvedValueOnce(jsonRes({ error: { code: "bad_request", message: "no" } }, 400));
    await expect(enablePush()).rejects.toThrow("no");
    expect(s.sub.unsubscribe).toHaveBeenCalled();
  });

  it("explains a declined permission prompt", async () => {
    const s = stubPush();
    s.N.requestPermission.mockResolvedValue("denied");
    await expect(enablePush()).rejects.toThrow(/blocked/);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("throws a readable error when unsupported", async () => {
    await expect(enablePush()).rejects.toThrow(/does not support/);
  });
});

describe("disablePush, mutes, badge", () => {
  it("deletes the hub subscription then unsubscribes", async () => {
    const s = stubPush();
    s.pushManager.getSubscription.mockResolvedValue(s.sub);
    fetchMock.mockImplementation(async () => jsonRes({ ok: true }));
    expect((await disablePush()).status).toBe("off");
    const [path, init] = call(0);
    expect(path).toBe("/api/push/subscribe");
    expect(init.method).toBe("DELETE");
    expect(JSON.parse(init.body)).toEqual({ endpoint: "https://push.example/1" });
    expect(s.sub.unsubscribe).toHaveBeenCalled();
  });

  it("sends mute and unmute requests", async () => {
    fetchMock.mockImplementation(async () => jsonRes({ ok: true }));
    await setMute("agent", "work/t1", new Date("2030-01-01T00:00:00Z"));
    expect(call(0)[1].method).toBe("PUT");
    expect(JSON.parse(call(0)[1].body)).toEqual({ scope: "agent", key: "work/t1", until: "2030-01-01T00:00:00.000Z" });
    await clearMute("session", "work");
    expect(call(1)[1].method).toBe("DELETE");
    expect(JSON.parse(call(1)[1].body)).toEqual({ scope: "session", key: "work" });
  });

  it("setBadge feature-detects and swallows errors", async () => {
    expect(() => setBadge(2)).not.toThrow();
    const set = vi.fn().mockRejectedValue(new Error("x"));
    const clear = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "setAppBadge", { value: set, configurable: true });
    Object.defineProperty(navigator, "clearAppBadge", { value: clear, configurable: true });
    setBadge(2);
    setBadge(0);
    expect(set).toHaveBeenCalledWith(2);
    expect(clear).toHaveBeenCalled();
    Reflect.deleteProperty(navigator, "setAppBadge");
    Reflect.deleteProperty(navigator, "clearAppBadge");
  });
});
