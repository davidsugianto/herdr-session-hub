import { useState } from "preact/hooks";
import { ApiError } from "../lib/api";
import { pairWithCode } from "../lib/pair";
import { startSync } from "../lib/store";
import "./pair.css";

export function pairErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_code") return "Invalid or expired code";
    if (err.code === "rate_limited") return "Too many attempts, wait a minute";
    return err.message || `Pairing failed (${err.code})`;
  }
  return err instanceof Error && err.message ? err.message : "Pairing failed";
}

/** Manual code entry. Used by PairScreen and Settings (the Home Screen app cannot use the QR link). */
export function PairForm(props: { onPaired?: () => void }) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    const c = code.trim();
    if (!c) {
      setError("Enter the pairing code");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await pairWithCode(c);
      startSync();
      setCode("");
      props.onPaired?.();
    } catch (err) {
      setError(pairErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form class="pair-form" onSubmit={submit}>
      <label class="pair-label" for="pair-code">Pairing code</label>
      <input
        id="pair-code"
        class="pair-input"
        type="text"
        value={code}
        onInput={(e) => setCode((e.currentTarget as HTMLInputElement).value)}
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck={false}
        enterkeyhint="go"
        disabled={busy}
        aria-invalid={error ? "true" : "false"}
        aria-describedby={error ? "pair-error" : undefined}
      />
      <button class="primary pair-btn" type="submit" disabled={busy}>
        {busy ? "Pairing…" : "Pair"}
      </button>
      {error && (
        <p id="pair-error" class="pair-error" role="alert">
          {error}
        </p>
      )}
    </form>
  );
}
