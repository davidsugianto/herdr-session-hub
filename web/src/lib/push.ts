// Web Push client: capability detection, subscribe/unsubscribe and mute helpers.
// enablePush must be called straight from a tap: iOS only shows the permission prompt for a user gesture.
import * as api from "./api";
import type { Mute, MuteScope, NotifyInfo } from "./types";

export type PushStatus = "unsupported" | "needs-install" | "denied" | "off" | "on";
export interface PushState {
  status: PushStatus;
  /** Human-readable explanation for statuses that are not on/off. */
  message: string;
}

export const isIOS = (): boolean =>
  /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);

export const isStandalone = (): boolean =>
  (typeof matchMedia === "function" && matchMedia("(display-mode: standalone)").matches) ||
  (navigator as Navigator & { standalone?: boolean }).standalone === true;

const hasPushApis = (): boolean =>
  "serviceWorker" in navigator && typeof window !== "undefined" && "PushManager" in window && "Notification" in window;

/** Sync part of the check; null when push may work. */
function blocker(): PushState | null {
  if (isIOS() && !isStandalone()) {
    return { status: "needs-install", message: "On iPhone, add this app to your Home Screen (Share, Add to Home Screen) and open it from there to enable notifications." };
  }
  if (!hasPushApis()) {
    return { status: "unsupported", message: "This browser does not support push notifications (it needs HTTPS and a service worker)." };
  }
  if (Notification.permission === "denied") {
    return { status: "denied", message: "Notifications are blocked. Allow them for this app in system settings." };
  }
  return null;
}

async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.getRegistration();
  return reg ? reg.pushManager.getSubscription() : null;
}

export async function getPushState(): Promise<PushState> {
  const b = blocker();
  if (b) return b;
  try {
    const sub = Notification.permission === "granted" ? await currentSubscription() : null;
    return sub ? { status: "on", message: "Notifications are on for this device." } : { status: "off", message: "Notifications are off." };
  } catch {
    return { status: "off", message: "Notifications are off." };
  }
}

function keyBytes(b64url: string): Uint8Array<ArrayBuffer> {
  const b64 = (b64url + "=".repeat((4 - (b64url.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** Asks for permission, subscribes and registers with the hub. Call from a tap. Throws Error with a readable message. */
export async function enablePush(): Promise<PushState> {
  const b = blocker();
  if (b) throw new Error(b.message);
  // First await in the gesture: the permission prompt.
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    throw new Error(perm === "denied" ? "Notifications are blocked. Allow them for this app in system settings." : "Permission was not granted.");
  }
  const reg = await navigator.serviceWorker.ready;
  const key = await api.getPushKey();
  const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
  try {
    await api.pushSubscribe(sub.toJSON());
  } catch (e) {
    // Keep browser and hub in agreement: a subscription the hub does not know is useless.
    await sub.unsubscribe().catch(() => {});
    throw e;
  }
  return { status: "on", message: "Notifications are on for this device." };
}

export async function disablePush(): Promise<PushState> {
  const sub = await currentSubscription();
  if (sub) {
    try { await api.pushUnsubscribe(sub.endpoint); } catch { /* hub copy is pruned on the next failed send */ }
    await sub.unsubscribe();
  }
  return { status: "off", message: "Notifications are off." };
}

export const sendTestPush = (): Promise<void> => api.pushTest();
export const getNotifyInfo = (): Promise<NotifyInfo> => api.getNotify();

export const getMutes = (): Promise<Mute[]> => api.getMutes();
export const setMute = (scope: MuteScope, key: string, until: Date | null = null): Promise<void> =>
  api.putMute(scope, key, until ? until.toISOString() : null);
export const clearMute = (scope: MuteScope, key: string): Promise<void> => api.deleteMute(scope, key);
export const isMuted = (mutes: Mute[], scope: MuteScope, key: string): boolean =>
  mutes.some((m) => m.scope === scope && m.key === key);

/** Sets or clears the app icon badge; unsupported or failing platforms are ignored. */
export function setBadge(n: number): void {
  const nav = navigator as Navigator & { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> };
  try {
    const p = n > 0 ? nav.setAppBadge?.(n) : nav.clearAppBadge?.();
    void p?.catch(() => {});
  } catch { /* ignore */ }
}

/** Closes delivered notifications; used when the app is opened/visible so stale ones do not linger. */
export async function closeStaleNotifications(): Promise<void> {
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    const list = (await reg?.getNotifications()) ?? [];
    list.forEach((n) => n.close());
  } catch { /* ignore */ }
}
