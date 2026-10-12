import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, approve, deny, getAgentDetail, trust } from "../lib/api";
import type { AgentDetail } from "../lib/types";
import { errMsg, showToast } from "./Toast";

const PROMPT_POLL_MS = 3000;

export type ActionKind = "approve" | "trust" | "deny";

/** Fetches agent detail; refetches when `state` changes and every few seconds while blocked when `poll` is set (the detail screen). Skips the fetch while `enabled` is false. */
export function useAgentDetail(id: string, enabled: boolean, state: string, poll = false) {
  const [detail, setDetail] = useState<AgentDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const seq = useRef(0);
  const inflight = useRef(false);

  const refetch = useCallback(async () => {
    const n = ++seq.current;
    inflight.current = true;
    try {
      const d = await getAgentDetail(id);
      if (n === seq.current) { setDetail(d); setError(null); }
    } catch (e) {
      if (n === seq.current) setError(errMsg(e));
    } finally {
      if (n === seq.current) inflight.current = false;
    }
  }, [id]);

  useEffect(() => {
    if (!enabled) return;
    void refetch();
    // The state stays "blocked" when an agent shows a second prompt, so poll for it
    // (the server still rejects a stale hash; this keeps the card readable).
    const t = poll && state === "blocked"
      ? setInterval(() => { if (document.visibilityState === "visible" && !inflight.current) void refetch(); }, PROMPT_POLL_MS)
      : undefined;
    return () => { seq.current++; inflight.current = false; clearInterval(t); };
  }, [enabled, state, poll, refetch]);

  return { detail: detail && detail.id === id ? detail : null, error, refetch };
}

/** Approve / trust / deny with the shared error handling. `refetch` is called after success and when the prompt is stale. */
export function useApprovalActions(agentId: string, detail: AgentDetail | null, refetch: () => void | Promise<void>) {
  const [busy, setBusy] = useState<ActionKind | null>(null);
  const [unavailable, setUnavailable] = useState<Partial<Record<ActionKind, string>>>({});
  const hash = detail?.prompt?.prompt?.hash ?? "";

  useEffect(() => { setUnavailable({}); }, [agentId, hash]);

  const run = async (kind: ActionKind, note?: string): Promise<boolean> => {
    if (busy) return false;
    if (!hash) { showToast("No prompt to act on"); return false; }
    setBusy(kind);
    try {
      if (kind === "approve") await approve(agentId, hash);
      else if (kind === "trust") await trust(agentId, hash);
      else await deny(agentId, hash, note || undefined);
      showToast(kind === "approve" ? "Approved" : kind === "trust" ? "Approved and trusted" : "Denied");
      void refetch();
      return true;
    } catch (e) {
      if (e instanceof ApiError) {
        switch (e.code) {
          case "prompt_changed":
            showToast("Prompt changed, review the new one");
            void refetch();
            break;
          case "not_blocked":
          case "no_prompt":
            showToast(e.message || "The agent is no longer waiting on a prompt");
            void refetch();
            break;
          case "action_unavailable":
            setUnavailable((u) => ({ ...u, [kind]: e.message || "Not available for this prompt" }));
            showToast(e.message || "Not available for this prompt");
            break;
          default:
            showToast(e.message || `Request failed (${e.code})`);
        }
      } else {
        showToast(errMsg(e));
      }
      return false;
    } finally {
      setBusy(null);
    }
  };

  return { busy, unavailable, run, hash };
}
