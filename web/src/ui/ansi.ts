// Strips terminal escape sequences (CSI, OSC, charset selects, simple two-char escapes).
// eslint-disable-next-line no-control-regex
const ANSI = /\u001b(?:\[[0-?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)?|[()][0-9A-Za-z]|[@-Z\\^_=>])/g;

export function stripAnsi(s: string): string {
  return s.replace(ANSI, "").replace(/\r\n/g, "\n");
}
