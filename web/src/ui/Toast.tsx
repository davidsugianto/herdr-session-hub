import { signal } from "@preact/signals";
import "./app.css";

interface ToastItem { id: number; msg: string }
const toasts = signal<ToastItem[]>([]);
let seq = 0;

/** Shows a short message at the bottom of the screen. Used by this worker's screens only. */
export function showToast(msg: string, ms = 4000): void {
  const id = ++seq;
  toasts.value = [...toasts.value, { id, msg }].slice(-3);
  setTimeout(() => { toasts.value = toasts.value.filter((t) => t.id !== id); }, ms);
}

export function errMsg(e: unknown): string {
  return e instanceof Error && e.message ? e.message : String(e);
}

export function ToastHost() {
  return (
    <div class="toast-host" role="status" aria-live="polite">
      {toasts.value.map((t) => <div key={t.id} class="toast">{t.msg}</div>)}
    </div>
  );
}
