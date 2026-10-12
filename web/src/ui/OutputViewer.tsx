import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import type { ComponentChildren } from "preact";
import { getOutput } from "../lib/api";
import { stripAnsi } from "./ansi";
import { errMsg, showToast } from "./Toast";
import "./output.css";

const REFRESH_MS = 2000;
const NEAR_BOTTOM = 24;

function highlight(text: string, q: string): { nodes: ComponentChildren[]; count: number } {
  if (!q) return { nodes: [text], count: 0 };
  const lower = text.toLowerCase();
  const needle = q.toLowerCase();
  const nodes: ComponentChildren[] = [];
  let i = 0;
  let count = 0;
  for (;;) {
    const j = lower.indexOf(needle, i);
    if (j < 0) break;
    if (j > i) nodes.push(text.slice(i, j));
    nodes.push(<mark key={j}>{text.slice(j, j + needle.length)}</mark>);
    count++;
    i = j + needle.length;
  }
  nodes.push(text.slice(i));
  return { nodes, count };
}

export function OutputViewer({ id, state }: { id: string; state: string }) {
  const [raw, setRaw] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [following, setFollowing] = useState(true);
  const [query, setQuery] = useState("");
  const [filterOnly, setFilterOnly] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);
  const inflight = useRef(false);
  const failing = useRef(false);
  const alive = useRef(true);

  const load = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      const text = stripAnsi(await getOutput(id, 300));
      if (!alive.current) return;
      setRaw((prev) => (prev === text ? prev : text));
      setError(null);
      failing.current = false;
    } catch (e) {
      if (!alive.current) return;
      setError(errMsg(e));
      if (!failing.current) showToast(`Output: ${errMsg(e)}`); // once per failure streak
      failing.current = true;
    } finally {
      inflight.current = false;
    }
  }, [id]);

  useEffect(() => {
    alive.current = true;
    setRaw(null);
    setFollowing(true);
    const tick = () => { if (document.visibilityState === "visible") void load(); };
    tick();
    const t = setInterval(tick, REFRESH_MS);
    document.addEventListener("visibilitychange", tick);
    return () => { alive.current = false; clearInterval(t); document.removeEventListener("visibilitychange", tick); };
  }, [load]);

  // Refresh right away on state change.
  const firstState = useRef(true);
  useEffect(() => {
    if (firstState.current) { firstState.current = false; return; }
    void load();
  }, [state, load]);

  const text = raw ?? "";
  const shown = useMemo(() => {
    if (!query || !filterOnly) return text;
    const q = query.toLowerCase();
    return text.split("\n").filter((l) => l.toLowerCase().includes(q)).join("\n");
  }, [text, query, filterOnly]);
  const { nodes, count } = useMemo(() => highlight(shown, query), [shown, query]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && following) el.scrollTop = el.scrollHeight;
  }, [shown, following]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM;
    setFollowing((f) => (f === atBottom ? f : atBottom));
  };

  const jump = () => {
    setFollowing(true);
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      showToast("Copied");
    } catch (e) {
      showToast(`Copy failed: ${errMsg(e)}`);
    }
  };

  return (
    <section class="output">
      <div class="output-tools">
        <input class="output-find" type="search" placeholder="Find in output" aria-label="Find in output" value={query} onInput={(e) => setQuery((e.currentTarget as HTMLInputElement).value)} />
        {query && <span class="output-count">{count} match{count === 1 ? "" : "es"}</span>}
        {query && <button type="button" class={`output-btn${filterOnly ? " active" : ""}`} aria-pressed={filterOnly} onClick={() => setFilterOnly(!filterOnly)}>Lines only</button>}
        <button type="button" class="output-btn" onClick={copy} disabled={!text}>Copy</button>
      </div>
      <div class="output-scroll" ref={scroller} onScroll={onScroll}>
        {raw === null && !error && <p class="output-muted">Loading output...</p>}
        {error && <p class="output-error">Could not load output: {error}</p>}
        {raw !== null && raw.trim() === "" && !error && <p class="output-muted">No output yet.</p>}
        <pre class="output-pre">{nodes}</pre>
      </div>
      {!following && <button type="button" class="output-jump primary" onClick={jump}>Jump to latest</button>}
    </section>
  );
}
