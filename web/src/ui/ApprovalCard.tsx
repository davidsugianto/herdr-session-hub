import { useEffect, useState } from "preact/hooks";
import type { Agent, AgentDetail } from "../lib/types";
import { useApprovalActions } from "./approvalActions";
import "./approval.css";

/** `onDone` should refetch the agent detail; it is called after an action and when the prompt went stale. */
export function ApprovalCard({ agent, detail, onDone }: { agent: Agent; detail: AgentDetail | null; onDone: () => void | Promise<void> }) {
  const { busy, unavailable, run } = useApprovalActions(agent.id, detail, onDone);
  const [denying, setDenying] = useState(false);
  const [note, setNote] = useState("");
  const promptHash = detail?.prompt?.prompt?.hash ?? "";

  // A different prompt appeared: never let a deny confirm land on one the user has not read.
  useEffect(() => { setDenying(false); setNote(""); }, [promptHash]);

  if (!detail) return <section class="approval"><p class="approval-muted">Loading prompt...</p></section>;
  const view = detail.prompt;
  const prompt = view?.prompt ?? null;

  if (!prompt) {
    return (
      <section class="approval">
        <h2 class="approval-title">Needs your input</h2>
        <p>Could not read the prompt.</p>
        <p class="approval-muted">Answer it with raw keys instead.</p>
        <a class="approval-link" href="#/keys">Open Keys</a>
      </section>
    );
  }

  const disabled = busy !== null;
  const confirmDeny = async () => {
    if (await run("deny", note.trim())) { setDenying(false); setNote(""); }
  };

  return (
    <section class="approval">
      <h2 class="approval-title">Needs your input</h2>
      <pre class="approval-text">{prompt.text}</pre>
      {prompt.options.length > 0 && (
        <ol class="approval-options">
          {prompt.options.map((o, i) => <li key={i}>{o}</li>)}
        </ol>
      )}
      {prompt.trust_rule && <p class="approval-trust">Trust adds: <code>{prompt.trust_rule}</code></p>}
      {unavailable.trust && <p class="approval-muted">{unavailable.trust}</p>}
      {view && view.verified === false && <p class="approval-warn">Profile not verified{view.profile ? ` (${view.profile})` : ""}. Check the prompt carefully.</p>}
      <p class="approval-hash" title={prompt.hash}>{prompt.hash.slice(0, 8)}</p>

      {denying ? (
        <div class="approval-deny">
          <label class="approval-label" for="approval-note">Note for the agent (optional)</label>
          <input id="approval-note" type="text" value={note} disabled={disabled} onInput={(e) => setNote((e.currentTarget as HTMLInputElement).value)} />
          <div class="approval-row">
            <button type="button" disabled={disabled} onClick={() => { setDenying(false); setNote(""); }}>Cancel</button>
            <button type="button" class="danger" disabled={disabled || !!unavailable.deny} onClick={confirmDeny}>{busy === "deny" ? "Denying..." : "Confirm deny"}</button>
          </div>
        </div>
      ) : (
        <div class="approval-row">
          <button type="button" class="primary" disabled={disabled || !!unavailable.approve} onClick={() => run("approve")}>{busy === "approve" ? "Approving..." : "Approve"}</button>
          {!unavailable.trust && <button type="button" disabled={disabled} onClick={() => run("trust")}>{busy === "trust" ? "Trusting..." : "Trust"}</button>}
          <button type="button" class="danger" disabled={disabled || !!unavailable.deny} onClick={() => setDenying(true)}>Deny</button>
        </div>
      )}
    </section>
  );
}
