import { useEffect, useRef, useState } from "preact/hooks";
import type { ComponentChildren } from "preact";
import "./detail.css";

/** Button that fires `onConfirm` only after being held for `ms`. A tap just shows the hint. */
export function HoldButton({ onConfirm, children, hint, ms = 800, class: cls = "", disabled }: {
  onConfirm: () => void; children: ComponentChildren; hint: string; ms?: number; class?: string; disabled?: boolean;
}) {
  const [holding, setHolding] = useState(false);
  const [tapped, setTapped] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const cancel = () => {
    if (timer.current) { clearTimeout(timer.current); timer.current = null; }
    setHolding(false);
  };
  const start = () => {
    if (disabled || timer.current) return;
    setHolding(true);
    setTapped(false);
    timer.current = setTimeout(() => { timer.current = null; setHolding(false); onConfirm(); }, ms);
  };
  const end = () => {
    if (timer.current) {
      // released early
      cancel();
      setTapped(true);
      if (hintTimer.current) clearTimeout(hintTimer.current);
      hintTimer.current = setTimeout(() => setTapped(false), 2000);
    }
  };
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
    if (hintTimer.current) clearTimeout(hintTimer.current);
  }, []);

  return (
    <button
      type="button"
      class={`hold ${cls}${holding ? " holding" : ""}`}
      disabled={disabled}
      onPointerDown={start}
      onPointerUp={end}
      onPointerLeave={cancel}
      onPointerCancel={cancel}
      onContextMenu={(e) => e.preventDefault()}
      onKeyDown={(e) => { if ((e.key === " " || e.key === "Enter") && !e.repeat) { e.preventDefault(); start(); } }}
      onKeyUp={(e) => { if (e.key === " " || e.key === "Enter") end(); }}
      onBlur={cancel}
    >
      <span class="hold-label">{tapped ? hint : children}</span>
    </button>
  );
}
