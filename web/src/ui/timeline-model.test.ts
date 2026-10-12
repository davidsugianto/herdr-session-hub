import { describe, expect, it } from "vitest";
import type { TimelineEvent } from "../lib/types";
import { RANGE_MS, axisTicks, buildTracks, describeEvent, feedOrder } from "./timeline-model";

const NOW = Date.parse("2026-01-01T12:00:00Z");
const H = 3_600_000;
const M = 60_000;
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString();

let nextId = 1;
const ev = (agent: string, msAgo: number, from: string, to: string, extra: Partial<TimelineEvent> = {}): TimelineEvent => ({
  id: nextId++,
  session: "work",
  agent_id: `work/${agent}`,
  name: agent,
  from,
  to,
  at: iso(msAgo),
  source: "herdr",
  ...extra,
});

describe("buildTracks", () => {
  it("returns nothing for empty input", () => {
    expect(buildTracks([], NOW, RANGE_MS["2h"])).toEqual([]);
  });

  it("builds a single segment from one event up to now", () => {
    const tracks = buildTracks([ev("a", 30 * M, "", "working")], NOW, 2 * H);
    expect(tracks).toHaveLength(1);
    expect(tracks[0]!.segments).toEqual([{ state: "working", start: NOW - 30 * M, end: NOW }]);
  });

  it("uses the first event's `from` for the part of the window before it", () => {
    const tracks = buildTracks([ev("a", 30 * M, "waiting", "working")], NOW, 2 * H);
    expect(tracks[0]!.segments).toEqual([
      { state: "waiting", start: NOW - 2 * H, end: NOW - 30 * M },
      { state: "working", start: NOW - 30 * M, end: NOW },
    ]);
  });

  it("keeps agents separate and sorts tracks by session then name", () => {
    const tracks = buildTracks(
      [
        ev("zed", 40 * M, "", "working"),
        ev("amy", 50 * M, "", "waiting"),
        ev("amy", 20 * M, "waiting", "blocked"),
        ev("bob", 10 * M, "", "finished", { session: "home", agent_id: "home/bob" }),
      ],
      NOW,
      2 * H,
    );
    expect(tracks.map((t) => t.agentId)).toEqual(["home/bob", "work/amy", "work/zed"]);
    const amy = tracks.find((t) => t.name === "amy")!;
    expect(amy.segments.map((s) => s.state)).toEqual(["waiting", "blocked"]);
    expect(amy.segments[1]!.end).toBe(NOW);
  });

  it("clamps segments to the window", () => {
    // Event older than the window, and one after `now`.
    const tracks = buildTracks(
      [ev("a", 5 * H, "", "working"), ev("a", 30 * M, "working", "blocked"), ev("a", -10 * M, "blocked", "finished")],
      NOW,
      2 * H,
    );
    const segs = tracks[0]!.segments;
    expect(segs[0]).toEqual({ state: "working", start: NOW - 2 * H, end: NOW - 30 * M });
    expect(segs[1]).toEqual({ state: "blocked", start: NOW - 30 * M, end: NOW });
    expect(segs).toHaveLength(2);
    for (const s of segs) {
      expect(s.start).toBeGreaterThanOrEqual(NOW - 2 * H);
      expect(s.end).toBeLessThanOrEqual(NOW);
    }
  });

  it("ends the segment at a `removed` event and does not extend to now", () => {
    const tracks = buildTracks([ev("a", 60 * M, "", "working"), ev("a", 20 * M, "working", "removed")], NOW, 2 * H);
    expect(tracks[0]!.segments).toEqual([{ state: "working", start: NOW - 60 * M, end: NOW - 20 * M }]);
  });

  it("resumes after removed when the agent comes back", () => {
    const tracks = buildTracks(
      [ev("a", 90 * M, "", "working"), ev("a", 60 * M, "working", "removed"), ev("a", 30 * M, "", "waiting")],
      NOW,
      2 * H,
    );
    expect(tracks[0]!.segments.map((s) => [s.state, s.start, s.end])).toEqual([
      ["working", NOW - 90 * M, NOW - 60 * M],
      ["waiting", NOW - 30 * M, NOW],
    ]);
  });

  it("ignores hub-sourced events for state", () => {
    const tracks = buildTracks([ev("a", 30 * M, "", "working"), ev("a", 10 * M, "", "approved", { source: "hub" })], NOW, 2 * H);
    expect(tracks[0]!.segments).toEqual([{ state: "working", start: NOW - 30 * M, end: NOW }]);
  });

  it("adds a full-width track for a current agent with no events in the window", () => {
    const tracks = buildTracks([], NOW, 2 * H, [
      { id: "work/idle", name: "idle", session: "work", state: "waiting", since: iso(10 * H) },
    ]);
    expect(tracks).toHaveLength(1);
    expect(tracks[0]!.segments).toEqual([{ state: "waiting", start: NOW - 2 * H, end: NOW }]);
  });

  it("does not duplicate a current agent that already has events", () => {
    const tracks = buildTracks([ev("a", 30 * M, "", "working")], NOW, 2 * H, [
      { id: "work/a", name: "a", session: "work", state: "working", since: iso(30 * M) },
    ]);
    expect(tracks).toHaveLength(1);
  });
});

describe("describeEvent", () => {
  it("words herdr events", () => {
    expect(describeEvent(ev("a", 0, "", "finished"))).toEqual({ actor: "", text: "a finished" });
    expect(describeEvent(ev("a", 0, "", "working")).text).toBe("a started working");
    expect(describeEvent(ev("a", 0, "working", "blocked")).text).toBe("a became blocked");
    expect(describeEvent(ev("a", 0, "working", "removed")).text).toBe("a removed");
  });

  it("labels hub events as You", () => {
    const l = describeEvent(ev("a", 0, "", "approved", { source: "hub" }));
    expect(l.actor).toBe("You");
    expect(l.text).toBe("approved a");
  });
});

describe("feedOrder / axisTicks", () => {
  it("orders newest first", () => {
    const a = ev("a", 30 * M, "", "working");
    const b = ev("a", 10 * M, "working", "finished");
    expect(feedOrder([a, b]).map((e) => e.id)).toEqual([b.id, a.id]);
  });

  it("spaces ticks across the window", () => {
    expect(axisTicks(NOW, 2 * H, 3)).toEqual([NOW - 2 * H, NOW - H, NOW]);
  });
});
