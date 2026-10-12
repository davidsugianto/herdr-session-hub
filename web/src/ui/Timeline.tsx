import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, getTimeline } from "../lib/api";
import { relTime, sessionColor } from "../lib/format";
import { agents, sessionFilter } from "../lib/store";
import type { TimelineEvent } from "../lib/types";
import {
  RANGES,
  RANGE_MS,
  axisTicks,
  buildTracks,
  describeEvent,
  feedOrder,
  hhmm,
  type AgentTrack,
  type RangeKey,
} from "./timeline-model";
import "./timeline.css";

const POLL_MS = 30_000;
const VB_W = 1000; // strip viewBox width; geometry is fractions of it
const VB_H = 20;

const errMessage = (e: unknown): string =>
  e instanceof ApiError ? e.message || e.code : e instanceof Error ? e.message : "Could not load the timeline";

function Strip(props: { track: AgentTrack; winStart: number; rangeMs: number }) {
  const { track, winStart, rangeMs } = props;
  return (
    <div class="tl-row">
      <span class={`tl-who sc-${sessionColor(track.session)}`} title={`${track.session} · ${track.name}`}>
        {track.name}
      </span>
      <svg class="tl-strip" viewBox={`0 0 ${VB_W} ${VB_H}`} preserveAspectRatio="none" role="img" aria-label={`${track.name} state over time`}>
        <rect class="tl-track-bg" x="0" y="0" width={VB_W} height={VB_H} />
        {track.segments.map((s) => (
          <rect
            key={`${s.start}-${s.state}`}
            class={`seg seg-${s.state}`}
            x={((s.start - winStart) / rangeMs) * VB_W}
            y="0"
            width={Math.max(((s.end - s.start) / rangeMs) * VB_W, 1)}
            height={VB_H}
          >
            <title>{`${s.state} ${hhmm(s.start)}–${hhmm(s.end)}`}</title>
          </rect>
        ))}
      </svg>
    </div>
  );
}

export function Timeline() {
  const [range, setRange] = useState<RangeKey>("2h");
  const [events, setEvents] = useState<TimelineEvent[] | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const reqId = useRef(0);

  const filter = sessionFilter.value;
  const current = agents.value;

  const load = useCallback(async () => {
    const id = ++reqId.current;
    try {
      const res = await getTimeline(range, filter || undefined);
      if (id !== reqId.current) return;
      setEvents(res);
      setNow(Date.now());
      setError("");
    } catch (e) {
      if (id !== reqId.current) return;
      setError(errMessage(e));
    } finally {
      if (id === reqId.current) setLoading(false);
    }
  }, [range, filter]);

  // Refetch on range / filter change; poll every 30 s while the page is visible.
  useEffect(() => {
    setLoading(true);
    void load();
    const tick = setInterval(() => {
      if (!document.hidden) void load();
    }, POLL_MS);
    const onVisible = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      reqId.current++;
      clearInterval(tick);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [load]);

  const rangeMs = RANGE_MS[range];
  const winStart = now - rangeMs;
  const shown = (events ?? []).filter((e) => !filter || e.session === filter);
  const currentAgents = Object.values(current).filter((a) => a.online && (!filter || a.session === filter));
  const tracks = buildTracks(shown, now, rangeMs, currentAgents);
  const feed = feedOrder(shown);

  return (
    <main class="tl">
      <header class="tl-head">
        <h1>Timeline</h1>
        <div class="tl-chips" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button key={r} type="button" class={`tl-chip${r === range ? " on" : ""}`} aria-pressed={r === range} onClick={() => setRange(r)}>
              {r}
            </button>
          ))}
        </div>
      </header>

      {error && (
        <div class="tl-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={() => void load()}>Retry</button>
        </div>
      )}

      {loading && events === null && !error && <p class="tl-note">Loading…</p>}

      {events !== null && (
        <>
          <section class="tl-strips" aria-label="Agent state strips">
            {tracks.length === 0 ? (
              <p class="tl-note">No agent activity in the last {range}.</p>
            ) : (
              <>
                {tracks.map((t) => (
                  <Strip key={t.agentId} track={t} winStart={winStart} rangeMs={rangeMs} />
                ))}
                <div class="tl-axis" aria-hidden="true">
                  {axisTicks(now, rangeMs, 3).map((t) => (
                    <span key={t}>{hhmm(t)}</span>
                  ))}
                </div>
                <ul class="tl-legend" aria-label="Legend">
                  {(["working", "waiting", "blocked", "finished"] as const).map((s) => (
                    <li key={s}>
                      <i class={`tl-dot seg-${s}`} /> {s}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </section>

          <h2 class="tl-sub">Events</h2>
          {feed.length === 0 ? (
            <p class="tl-note">No events in the last {range}.</p>
          ) : (
            <ul class="tl-feed">
              {feed.map((e) => {
                const line = describeEvent(e);
                const at = Date.parse(e.at);
                return (
                  <li key={e.id} class={`tl-item${e.source === "hub" ? " hub" : ""}`}>
                    <time class="tl-time" dateTime={e.at} title={relTime(e.at, now)}>
                      {hhmm(at)}
                    </time>
                    <span class={`tl-dotbar sc-bg-${sessionColor(e.session)}`} aria-hidden="true" />
                    <span class="tl-text">
                      <span class={`tl-sess sc-${sessionColor(e.session)}`}>{e.session}</span>{" "}
                      {line.actor && <b>{line.actor} </b>}
                      {line.text}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
    </main>
  );
}
