// Offline shell + Web Push. Never caches /api/*.
// push: always shows a system notification (iOS revokes subscriptions that do not), sets the app badge and
// tells any open window. notificationclick: focuses a window and opens the payload url, else opens one.
const VERSION = "854b4cc79d";
const CACHE = "hub-shell-" + VERSION;
const PRECACHE = ["/","/assets/index-ONVQITAa.js","/icon-192.png","/manifest.webmanifest"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(PRECACHE)).then(() => self.skipWaiting()));
});
self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((ks) => Promise.all(ks.filter((k) => k.startsWith("hub-shell-") && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});
self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin || url.pathname.startsWith("/api/") || url.pathname === "/healthz") return;
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => caches.match("/")));
    return;
  }
  e.respondWith(caches.match(req).then((hit) => hit || fetch(req)));
});

const GENERIC = { title: "Herdr Session Hub", body: "Something needs your attention", url: "/#/", tag: "hub" };

function parsePayload(data) {
  let p = null;
  try { p = data ? data.json() : null; } catch { /* malformed: fall back to generic */ }
  if (!p || typeof p !== "object") return { ...GENERIC };
  const str = (v, d) => (typeof v === "string" && v ? v : d);
  return {
    title: str(p.title, GENERIC.title),
    body: str(p.body, ""),
    // Only same-origin paths: a push must not be able to open an arbitrary site.
    url: typeof p.url === "string" && p.url.startsWith("/") && !p.url.startsWith("//") ? p.url : GENERIC.url,
    tag: str(p.tag, GENERIC.tag),
    badge: Number.isFinite(p.badge) ? p.badge : undefined,
    kind: str(p.kind, ""),
    agent_id: str(p.agent_id, ""),
  };
}

async function onPush(event) {
  const p = parsePayload(event.data);
  const work = [self.registration.showNotification(p.title, {
    body: p.body, tag: p.tag, renotify: true, icon: "/icon-192.png", data: { url: p.url, agent_id: p.agent_id, kind: p.kind },
  })];
  const nav = self.navigator;
  if (p.badge !== undefined && nav && nav.setAppBadge) {
    work.push((p.badge > 0 ? nav.setAppBadge(p.badge) : nav.clearAppBadge ? nav.clearAppBadge() : Promise.resolve()).catch(() => {}));
  }
  work.push(
    self.clients.matchAll({ type: "window", includeUncontrolled: true })
      .then((cs) => cs.filter((c) => c.visibilityState === "visible").forEach((c) => c.postMessage({ type: "push", payload: p })))
      .catch(() => {}),
  );
  await Promise.all(work);
}

async function onClick(event) {
  event.notification.close();
  const raw = (event.notification.data && event.notification.data.url) || GENERIC.url;
  const url = new URL(raw, self.location.origin).href;
  const cs = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const c = cs.find((w) => new URL(w.url).origin === self.location.origin);
  if (c) {
    try { await c.focus(); } catch { /* best effort */ }
    try { await c.navigate(url); } catch { c.postMessage({ type: "navigate", url: raw }); }
    return;
  }
  await self.clients.openWindow(url);
}

self.addEventListener("push", (e) => e.waitUntil(onPush(e)));
self.addEventListener("notificationclick", (e) => e.waitUntil(onClick(e)));
