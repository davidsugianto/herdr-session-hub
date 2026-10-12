import { useEffect, useRef, useState } from "preact/hooks";
import { sendKeys, sendText, ApiError } from "../lib/api";
import { agents, lastAgentId } from "../lib/store";
import { stateLabel } from "../lib/format";
import { DIGIT_KEYS, KEYS, type KeyDef } from "./keymap";
import "./keys.css";

const HOLD_MS = 800;

function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "session_offline": return "Session is offline.";
      case "agent_not_found": return "Agent no longer exists.";
      case "rate_limited": return "Too many requests. Wait a moment.";
      default: return e.message || "Request failed.";
    }
  }
  return e instanceof Error && e.message ? e.message : "Request failed.";
}

function tick() {
  try {
    navigator.vibrate?.(10);
  } catch {
    /* haptics unsupported */
  }
}

export function Keys() {
  const id = lastAgentId.value;
  const agent = id ? agents.value[id] : undefined;
  const [error, setError] = useState("");
  const [pressed, setPressed] = useState("");
  const [holding, setHolding] = useState(false);
  const [text, setText] = useState("");
  const [enter, setEnter] = useState(true);
  const [sending, setSending] = useState(false);
  const holdTimer = useRef<number | undefined>(undefined);
  const pressTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => {
    window.clearTimeout(holdTimer.current);
    window.clearTimeout(pressTimer.current);
  }, []);

  if (!id || !agent) {
    return (
      <div class="keys">
        <div class="keys-empty">
          <span>Open an agent first</span>
          <a href="#/">Go to agents</a>
        </div>
      </div>
    );
  }

  const disabled = !agent.online;

  async function fire(k: KeyDef) {
    tick();
    setPressed(k.combo);
    window.clearTimeout(pressTimer.current);
    pressTimer.current = window.setTimeout(() => setPressed(""), 160);
    setError("");
    try {
      await sendKeys(agent!.id, [k.combo]);
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  function startHold(k: KeyDef) {
    if (disabled) return;
    window.clearTimeout(holdTimer.current);
    setHolding(true);
    holdTimer.current = window.setTimeout(() => {
      setHolding(false);
      void fire(k);
    }, HOLD_MS);
  }

  function cancelHold() {
    window.clearTimeout(holdTimer.current);
    setHolding(false);
  }

  function renderKey(k: KeyDef) {
    if (k.confirm) {
      return (
        <button
          type="button"
          key={k.combo}
          class={`keys-key danger${holding ? " holding" : ""}${pressed === k.combo ? " pressed" : ""}`}
          disabled={disabled}
          aria-label={`${k.label} (hold to send)`}
          onPointerDown={() => startHold(k)}
          onPointerUp={cancelHold}
          onPointerLeave={cancelHold}
          onPointerCancel={cancelHold}
          onKeyDown={(e) => {
            if ((e.key === "Enter" || e.key === " ") && !e.repeat) startHold(k);
          }}
          onKeyUp={cancelHold}
          onBlur={cancelHold}
          onContextMenu={(e) => e.preventDefault()}
        >
          {k.label}
        </button>
      );
    }
    return (
      <button
        type="button"
        key={k.combo}
        class={`keys-key${pressed === k.combo ? " pressed" : ""}`}
        disabled={disabled}
        onClick={() => void fire(k)}
      >
        {k.label}
      </button>
    );
  }

  async function submitText() {
    if (sending || disabled || (text === "" && !enter)) return;
    setSending(true);
    setError("");
    try {
      await sendText(agent!.id, text, enter);
      tick();
      setText("");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSending(false);
    }
  }

  return (
    <div class="keys">
      <div class="keys-target">
        <div>
          <div class="keys-target-name">{agent.name}</div>
          <div class="keys-target-sub">{agent.session_label}{disabled ? " · offline" : ""}</div>
        </div>
        <span class={`badge ${agent.state}`}>{stateLabel(agent.state)}</span>
      </div>

      {error && <p class="keys-error" role="alert">{error}</p>}

      <div class="keys-grid">{KEYS.map(renderKey)}</div>
      <p class="keys-hint">Hold Ctrl+C to send it.</p>

      <h2>Numbered menus</h2>
      <div class="keys-digits">{DIGIT_KEYS.map(renderKey)}</div>

      <h2>Send text</h2>
      <form
        class="keys-send"
        onSubmit={(e) => {
          e.preventDefault();
          void submitText();
        }}
      >
        <div class="keys-send-row">
          <input
            type="text"
            value={text}
            placeholder="Text to type…"
            autocapitalize="off"
            autocomplete="off"
            disabled={disabled || sending}
            onInput={(e) => setText((e.currentTarget as HTMLInputElement).value)}
          />
          <button type="submit" class="primary" disabled={disabled || sending || (text === "" && !enter)}>Send</button>
        </div>
        <label class="keys-check">
          <input
            type="checkbox"
            checked={enter}
            disabled={disabled}
            onChange={(e) => setEnter((e.currentTarget as HTMLInputElement).checked)}
          />
          Press Enter after
        </label>
      </form>
    </div>
  );
}
