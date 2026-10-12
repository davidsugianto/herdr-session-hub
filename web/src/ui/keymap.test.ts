import { describe, expect, it } from "vitest";
import { DIGIT_KEYS, KEYS, ALL_KEYS } from "./keymap";

describe("keymap", () => {
  it("has non-empty labels and combos matching the server key regexp", () => {
    for (const k of ALL_KEYS) {
      expect(k.label.length).toBeGreaterThan(0);
      expect(k.combo.length).toBeGreaterThan(0);
      expect(k.combo.length).toBeLessThanOrEqual(32);
      expect(k.combo).toMatch(/^[A-Za-z0-9+_-]+$/);
    }
  });

  it("has unique combos", () => {
    const combos = ALL_KEYS.map((k) => k.combo);
    expect(new Set(combos).size).toBe(combos.length);
  });

  it("digit row is 1-9", () => {
    expect(DIGIT_KEYS.map((k) => k.combo)).toEqual(["1", "2", "3", "4", "5", "6", "7", "8", "9"]);
  });

  it("only uses the herdr combos the UI is allowed to send", () => {
    const allowed = new Set([
      "esc", "tab", "shift+tab", "up", "down", "left", "right", "enter", "ctrl+c", "pageup", "pagedown",
      "1", "2", "3", "4", "5", "6", "7", "8", "9",
    ]);
    for (const k of ALL_KEYS) expect(allowed.has(k.combo)).toBe(true);
    expect(KEYS.length).toBe(11);
  });

  it("marks only ctrl+c as needing confirmation", () => {
    expect(ALL_KEYS.filter((k) => k.confirm).map((k) => k.combo)).toEqual(["ctrl+c"]);
  });
});
