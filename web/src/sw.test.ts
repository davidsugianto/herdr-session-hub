import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it, vi } from "vitest";

type Handler = (e: unknown) => void;
const src = readFileSync("src/sw.template.js", "utf8")
  .replace("__VERSION__", "t").replace("__PRECACHE__", "[]");

function boot(clients: unknown[] = []) {
  const handlers = {} as Record<"push" | "notificationclick", Handler>;
  const self = {
    addEventListener: (t: string, h: Handler) => { (handlers as Record<string, Handler>)[t] = h; },
    registration: { showNotification: vi.fn().mockResolvedValue(undefined) },
    navigator: { setAppBadge: vi.fn().mockResolvedValue(undefined), clearAppBadge: vi.fn().mockResolvedValue(undefined) },
    location: { origin: "https://hub.example" },
    clients: { matchAll: vi.fn().mockResolvedValue(clients), openWindow: vi.fn().mockResolvedValue(null), claim: vi.fn() },
  };
  new Function("self", "location", src)(self, self.location);
  return { self, handlers };
}

async function fire(h: Handler, e: Record<string, unknown>) {
  let p: Promise<unknown> = Promise.resolve();
  h({ ...e, waitUntil: (x: Promise<unknown>) => { p = x; } });
  await p;
}
const pushData = (json: unknown) => ({ json: () => json });

describe("sw push", () => {
  let t: ReturnType<typeof boot>;
  beforeEach(() => { t = boot(); });

  it("shows a notification from the payload and sets the badge", async () => {
    await fire(t.handlers.push, { data: pushData({ title: "T", body: "B", url: "/#/a/work%2Ft1", tag: "work/t1", badge: 3 }) });
    expect(t.self.registration.showNotification).toHaveBeenCalledWith("T", expect.objectContaining({
      body: "B", tag: "work/t1", renotify: true, data: expect.objectContaining({ url: "/#/a/work%2Ft1" }),
    }));
    expect(t.self.navigator.setAppBadge).toHaveBeenCalledWith(3);
  });

  it("clears the badge when the count is zero", async () => {
    await fire(t.handlers.push, { data: pushData({ title: "T", badge: 0 }) });
    expect(t.self.navigator.clearAppBadge).toHaveBeenCalled();
  });

  it("falls back to a generic notification on a malformed payload", async () => {
    await fire(t.handlers.push, { data: { json: () => { throw new SyntaxError("bad"); } } });
    expect(t.self.registration.showNotification).toHaveBeenCalledTimes(1);
    expect(t.self.registration.showNotification.mock.calls[0]![1].data.url).toBe("/#/");
    await fire(t.handlers.push, { data: null });
    expect(t.self.registration.showNotification).toHaveBeenCalledTimes(2);
  });

  it("rejects cross-origin urls from the payload", async () => {
    await fire(t.handlers.push, { data: pushData({ title: "T", url: "//evil.example/x" }) });
    expect(t.self.registration.showNotification.mock.calls[0]![1].data.url).toBe("/#/");
  });

  it("tells a visible window about the push", async () => {
    const win = { visibilityState: "visible", postMessage: vi.fn() };
    const b = boot([win]);
    await fire(b.handlers.push, { data: pushData({ title: "T" }) });
    expect(win.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "push" }));
  });
});

describe("sw notificationclick", () => {
  const note = (url?: string) => ({ close: vi.fn(), data: url ? { url } : undefined });

  it("closes, focuses an existing window and navigates to the url", async () => {
    const win = { url: "https://hub.example/", focus: vi.fn().mockResolvedValue(undefined), navigate: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn() };
    const t = boot([win]);
    const notification = note("/#/a/work%2Ft1");
    await fire(t.handlers.notificationclick, { notification });
    expect(notification.close).toHaveBeenCalled();
    expect(win.focus).toHaveBeenCalled();
    expect(win.navigate).toHaveBeenCalledWith("https://hub.example/#/a/work%2Ft1");
    expect(t.self.clients.openWindow).not.toHaveBeenCalled();
  });

  it("falls back to postMessage when navigate is unavailable", async () => {
    const win = { url: "https://hub.example/", focus: vi.fn().mockResolvedValue(undefined), navigate: vi.fn().mockRejectedValue(new Error("no")), postMessage: vi.fn() };
    const t = boot([win]);
    await fire(t.handlers.notificationclick, { notification: note("/#/settings") });
    expect(win.postMessage).toHaveBeenCalledWith({ type: "navigate", url: "/#/settings" });
  });

  it("opens a window when none exists", async () => {
    const t = boot([]);
    await fire(t.handlers.notificationclick, { notification: note("/#/a/work%2Ft1") });
    expect(t.self.clients.openWindow).toHaveBeenCalledWith("https://hub.example/#/a/work%2Ft1");
  });
});
