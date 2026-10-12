import { relTime, sessionColor, stateLabel } from "../lib/format";
import type { Agent } from "../lib/types";
import { useAgentDetail, useApprovalActions } from "./approvalActions";
import "./board.css";

const base = (p: string) => p.replace(/\/+$/, "").split("/").pop() || p;
export const agentHref = (id: string) => `#/a/${encodeURIComponent(id)}`;

export function AgentCard({ agent }: { agent: Agent }) {
  const blocked = agent.state === "blocked" && agent.online;
  const { detail, error, refetch } = useAgentDetail(agent.id, blocked, agent.state);
  const { busy, unavailable, run, hash } = useApprovalActions(agent.id, detail, refetch);
  const canAct = blocked && !!hash;
  const disabled = busy !== null;

  return (
    <article class={`card ${agent.state}${agent.online ? "" : " offline"}`}>
      <a class="card-main" href={agentHref(agent.id)}>
        <div class="card-top">
          <span class={`badge ${agent.state}`}>{stateLabel(agent.state)}</span>
          <span class="card-name"><span class="card-kind">{agent.kind}</span> {agent.name}</span>
          <span class="card-time">{relTime(agent.since)}</span>
        </div>
        <div class="card-meta">
          <span class={`card-session ${sessionColor(agent.session)}`}>{agent.session_label || agent.session}</span>
          {agent.cwd && <span class="card-cwd">{base(agent.cwd)}</span>}
          {!agent.online && <span class="card-offline">offline</span>}
        </div>
      </a>
      {blocked && (
        <div class="card-actions">
          {canAct ? (
            <>
              <button type="button" class="primary" disabled={disabled || !!unavailable.approve} onClick={() => run("approve")}>{busy === "approve" ? "Approving..." : "Approve"}</button>
              {!unavailable.trust && <button type="button" disabled={disabled} onClick={() => run("trust")}>{busy === "trust" ? "Trusting..." : "Trust"}</button>}
              <a class="card-open" href={agentHref(agent.id)}>Open</a>
            </>
          ) : (
            <>
              <a class="card-open" href={agentHref(agent.id)}>Open</a>
              {error && <span class="card-note">Prompt unavailable</span>}
            </>
          )}
        </div>
      )}
    </article>
  );
}
