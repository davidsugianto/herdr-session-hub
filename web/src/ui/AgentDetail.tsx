import { useEffect, useState } from "preact/hooks";
import { interrupt, markSeen } from "../lib/api";
import { relTime, sessionColor, stateLabel } from "../lib/format";
import { agents, lastAgentId } from "../lib/store";
import { ApprovalCard } from "./ApprovalCard";
import { OutputViewer } from "./OutputViewer";
import { ReplySheet } from "./ReplySheet";
import { errMsg, showToast } from "./Toast";
import { useAgentDetail } from "./approvalActions";
import { HoldButton } from "./detailHold";
import "./detail.css";

export function AgentDetail({ id }: { id: string }) {
  const agent = agents.value[id];
  const state = agent?.state ?? "unknown";
  const { detail, refetch } = useAgentDetail(id, !!agent, state, true);
  const [replying, setReplying] = useState(false);
  const [menu, setMenu] = useState(false);

  useEffect(() => { lastAgentId.value = id; }, [id]);

  useEffect(() => {
    if (state !== "finished") return;
    (async () => {
      try { await markSeen(id); } catch (e) { showToast(`Could not mark as seen: ${errMsg(e)}`); }
    })();
  }, [id, state]);

  const stop = async (force: boolean) => {
    setMenu(false);
    try {
      await interrupt(id, force);
      showToast(force ? "Force stop sent" : "Interrupt sent");
    } catch (e) {
      showToast(`${force ? "Force stop" : "Interrupt"} failed: ${errMsg(e)}`);
    }
  };

  if (!agent) {
    return (
      <main class="detail">
        <header class="detail-header"><a class="detail-back" href="#/">‹ Back</a></header>
        <p class="detail-missing">Agent not found. It may have gone away.</p>
      </main>
    );
  }

  const offline = !agent.online;
  return (
    <main class="detail">
      <header class="detail-header">
        <a class="detail-back" href="#/" aria-label="Back to board">‹ Back</a>
        <div class="detail-head">
          <div class="detail-name"><span class="detail-kind">{agent.kind}</span> {agent.name}</div>
          <div class="detail-sub">
            <span class={`detail-session ${sessionColor(agent.session)}`}>{agent.session_label || agent.session}</span>
            <span>{relTime(agent.since)}</span>
            {offline && <span class="detail-offline">offline</span>}
          </div>
        </div>
        <span class={`badge ${agent.state}`}>{stateLabel(agent.state)}</span>
      </header>

      <div class="detail-main">
        {agent.state === "blocked" && <ApprovalCard agent={agent} detail={detail} onDone={refetch} />}
        <OutputViewer id={id} state={agent.state} />
      </div>

      <footer class="detail-actions">
        {menu && (
          <div class="detail-menu">
            <HoldButton class="danger" hint="Keep holding" onConfirm={() => stop(true)}>Hold to force stop</HoldButton>
          </div>
        )}
        <button type="button" class="primary" disabled={offline} onClick={() => setReplying(true)}>Reply</button>
        <HoldButton hint="Keep holding" disabled={offline} onConfirm={() => stop(false)}>Hold to interrupt</HoldButton>
        <button type="button" class="detail-more" aria-label="More actions" aria-expanded={menu} onClick={() => setMenu(!menu)}>⋯</button>
      </footer>

      {replying && <ReplySheet agent={agent} onClose={() => setReplying(false)} />}
    </main>
  );
}
