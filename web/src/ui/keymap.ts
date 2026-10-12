// Key definitions for the Keys tab. Combos are herdr key names; the Go side only checks
// them against /^[A-Za-z0-9+_-]+$/ (max 32 chars), so keep this list conservative.

export interface KeyDef {
  label: string;
  combo: string;
  /** Needs a press-and-hold to fire (destructive). */
  confirm?: boolean;
}

export const KEYS: readonly KeyDef[] = [
  { label: "Esc", combo: "esc" },
  { label: "Tab", combo: "tab" },
  { label: "⇧Tab", combo: "shift+tab" },
  { label: "↑", combo: "up" },
  { label: "↓", combo: "down" },
  { label: "←", combo: "left" },
  { label: "→", combo: "right" },
  { label: "Enter", combo: "enter" },
  { label: "Ctrl+C", combo: "ctrl+c", confirm: true },
  { label: "PgUp", combo: "pageup" },
  { label: "PgDn", combo: "pagedown" },
];

export const DIGIT_KEYS: readonly KeyDef[] = [1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => ({
  label: String(n),
  combo: String(n),
}));

export const ALL_KEYS: readonly KeyDef[] = [...KEYS, ...DIGIT_KEYS];
