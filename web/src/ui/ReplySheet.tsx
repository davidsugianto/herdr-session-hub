import { useEffect, useRef, useState } from "preact/hooks";
import { reply, ApiError } from "../lib/api";
import { agents } from "../lib/store";
import type { Agent } from "../lib/types";
import "./reply.css";

const QUICK = ["yes", "no", "continue", "retry", "commit and push"];
const PRESET_KEY = "hub.presets";
const MAX_PRESETS = 10;
const MAX_PRESET_LEN = 200;

function loadPresets(): string[] {
  try {
    const raw = localStorage.getItem(PRESET_KEY);
    const v: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(v)) return [];
    return v
      .filter((s): s is string => typeof s === "string" && s.trim() !== "" && s.length <= MAX_PRESET_LEN)
      .slice(0, MAX_PRESETS);
  } catch {
    return [];
  }
}

function savePresets(p: string[]): void {
  try {
    localStorage.setItem(PRESET_KEY, JSON.stringify(p));
  } catch {
    /* storage unavailable: presets stay in memory only */
  }
}

function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "session_offline": return "Session is offline.";
      case "agent_not_found": return "Agent no longer exists.";
      case "rate_limited": return "Too many requests. Wait a moment.";
      default: return e.message || "Could not send the reply.";
    }
  }
  return e instanceof Error && e.message ? e.message : "Could not send the reply.";
}

export function ReplySheet({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [blockedSeen, setBlockedSeen] = useState(false);
  const [presets, setPresets] = useState<string[]>(loadPresets);
  const area = useRef<HTMLTextAreaElement>(null);

  const blocked = blockedSeen || agents.value[agent.id]?.state === "blocked";

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => {
    if (!blocked) area.current?.focus();
  }, [blocked]);

  async function send(msg: string) {
    if (busy || msg.trim() === "") return;
    setBusy(true);
    setError("");
    try {
      await reply(agent.id, msg);
      onClose();
    } catch (e) {
      if (e instanceof ApiError && e.code === "agent_blocked") setBlockedSeen(true);
      else setError(errorMessage(e));
      setBusy(false);
    }
  }

  function addPreset() {
    const t = text.trim();
    if (!t || t.length > MAX_PRESET_LEN || presets.length >= MAX_PRESETS || presets.includes(t)) return;
    const next = [...presets, t];
    setPresets(next);
    savePresets(next);
  }

  function removePreset(p: string) {
    const next = presets.filter((x) => x !== p);
    setPresets(next);
    savePresets(next);
  }

  const canAdd =
    text.trim() !== "" && text.trim().length <= MAX_PRESET_LEN && presets.length < MAX_PRESETS && !presets.includes(text.trim());

  return (
    <div class="reply-backdrop" onClick={onClose}>
      <div
        class="reply-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={`Reply to ${agent.name}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div class="reply-grab" aria-hidden="true" />
        <div class="reply-head">
          <h2 class="reply-title">Reply to {agent.name}</h2>
          <button type="button" class="reply-close" aria-label="Close" onClick={onClose}>×</button>
        </div>
        {blocked ? (
          <div class="reply-blocked">
            <p>Agent is waiting on a permission prompt</p>
            <button type="button" class="primary" onClick={onClose}>Back to approval</button>
          </div>
        ) : (
          <>
            <textarea
              ref={area}
              class="reply-text"
              autofocus
              rows={4}
              placeholder="Type a reply…"
              value={text}
              disabled={busy}
              onInput={(e) => setText((e.currentTarget as HTMLTextAreaElement).value)}
            />
            <div class="reply-chips">
              {QUICK.map((q) => (
                <button type="button" key={q} class="reply-chip" disabled={busy} onClick={() => send(q)}>{q}</button>
              ))}
              {presets.map((p) => (
                <span key={p} class="reply-chip reply-chip-preset">
                  <button type="button" class="reply-chip-main" disabled={busy} onClick={() => send(p)}>
                    {p.length > 28 ? p.slice(0, 27) + "…" : p}
                  </button>
                  <button type="button" class="reply-chip-x" aria-label={`Remove preset ${p}`} onClick={() => removePreset(p)}>✕</button>
                </span>
              ))}
              {canAdd && (
                <button type="button" class="reply-chip reply-chip-add" onClick={addPreset}>+ preset</button>
              )}
            </div>
            {error && <p class="reply-error" role="alert">{error}</p>}
            <div class="reply-actions">
              <button type="button" class="primary" disabled={busy || text.trim() === ""} onClick={() => send(text)}>
                {busy ? "Sending…" : "Send"}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
