import { describe, expect, it } from "vitest";
import { relTime, sessionColor, stateLabel } from "./format";

describe("relTime", () => {
  const now = Date.parse("2026-01-10T12:00:00Z");
  const ago = (ms: number) => new Date(now - ms).toISOString();
  it("formats buckets", () => {
    expect(relTime(ago(5_000), now)).toBe("just now");
    expect(relTime(ago(59_000), now)).toBe("just now");
    expect(relTime(ago(60_000), now)).toBe("1m");
    expect(relTime(ago(2 * 60_000 + 5_000), now)).toBe("2m");
    expect(relTime(ago(60 * 60_000), now)).toBe("1h");
    expect(relTime(ago(23 * 3600_000), now)).toBe("23h");
    expect(relTime(ago(3 * 86400_000), now)).toBe("3d");
  });
  it("treats future and invalid times safely", () => {
    expect(relTime(ago(-10_000), now)).toBe("just now");
    expect(relTime("garbage", now)).toBe("");
  });
});

describe("stateLabel", () => {
  it("returns a readable label", () => {
    expect(stateLabel("blocked")).toBe("Blocked");
    expect(stateLabel("unknown")).toBe("Unknown");
  });
});

describe("sessionColor", () => {
  it("is stable and within s1..s6", () => {
    expect(sessionColor("labs")).toBe(sessionColor("labs"));
    for (const n of ["", "a", "labs", "prod-session", "日本語"]) expect(sessionColor(n)).toMatch(/^s[1-6]$/);
  });
  it("spreads across tokens", () => {
    const set = new Set(Array.from({ length: 40 }, (_, i) => sessionColor("session" + i)));
    expect(set.size).toBeGreaterThan(3);
  });
});
