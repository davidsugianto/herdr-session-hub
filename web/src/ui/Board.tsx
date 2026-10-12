import { useEffect, useState } from "preact/hooks";
import { agents, counts, sessionFilter, sessions, sortedAgents } from "../lib/store";
import { sessionColor } from "../lib/format";
import type { Agent } from "../lib/types";
import { AgentCard } from "./AgentCard";
import "./board.css";

function Group({ title, list }: { title: string; list: Agent[] }) {
  if (list.length === 0) return null;
  return (
    <section class="board-group">
      <h2 class="board-group-title">{title} <span class="board-group-count">{list.length}</span></h2>
      {list.map((a) => <AgentCard key={a.id} agent={a} />)}
    </section>
  );
}

export function Board() {
  // Re-render periodically so relative times stay fresh.
  const [, setTick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 30000);
    return () => clearInterval(t);
  }, []);

  const list = sortedAgents.value;
  const c = counts.value;
  const ss = sessions.value;
  const filter = sessionFilter.value;
  const total = Object.values(agents.value).filter((a) => a.state !== "unknown").length;
  const offline = ss.filter((s) => !s.online).length;
  const summary = `${ss.length} session${ss.length === 1 ? "" : "s"}${offline ? `, ${offline} offline` : ""}`;

  const needs = list.filter((a) => a.state === "blocked" || a.state === "finished");
  const working = list.filter((a) => a.state === "working");
  const waiting = list.filter((a) => a.state === "waiting");

  return (
    <main class="board">
      <header class="board-header">
        <h1 class="board-title">herd</h1>
        <span class="board-summary">{summary}</span>
      </header>

      <div class="chips" role="group" aria-label="Filter by session">
        <button type="button" class={`chip${filter === "" ? " active" : ""}`} aria-pressed={filter === ""} onClick={() => { sessionFilter.value = ""; }}>All {total}</button>
        {ss.map((s) => (
          <button key={s.name} type="button" class={`chip ${sessionColor(s.name)}${filter === s.name ? " active" : ""}${s.online ? "" : " offline"}`} aria-pressed={filter === s.name} onClick={() => { sessionFilter.value = filter === s.name ? "" : s.name; }}>
            <span class="chip-dot" />{s.label || s.name} {s.agents}
          </button>
        ))}
      </div>

      <div class="counts">
        <span class="count blocked"><b>{c.blocked}</b> blocked</span>
        <span class="count finished"><b>{c.finished}</b> finished</span>
        <span class="count working"><b>{c.working}</b> working</span>
        <span class="count waiting"><b>{c.waiting}</b> waiting</span>
      </div>

      {list.length === 0 && <p class="board-empty">No agents{filter ? " in this session" : ""}.</p>}
      <Group title="Needs you" list={needs} />
      <Group title="Working" list={working} />
      <Group title="Waiting" list={waiting} />
    </main>
  );
}
