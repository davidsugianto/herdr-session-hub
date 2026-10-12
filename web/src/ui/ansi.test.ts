import { describe, expect, it } from "vitest";
import { stripAnsi } from "./ansi";

describe("stripAnsi", () => {
  it("leaves plain text alone", () => {
    expect(stripAnsi("hello\nworld")).toBe("hello\nworld");
  });
  it("strips SGR colour codes", () => {
    expect(stripAnsi("\u001b[1;31mred\u001b[0m text")).toBe("red text");
  });
  it("strips cursor and erase sequences", () => {
    expect(stripAnsi("a\u001b[2K\u001b[1Gb\u001b[?25lc")).toBe("abc");
  });
  it("strips OSC titles terminated by BEL or ST", () => {
    expect(stripAnsi("\u001b]0;title\u0007x\u001b]8;;http://a\u001b\\y")).toBe("xy");
  });
  it("strips charset selects and simple escapes", () => {
    expect(stripAnsi("\u001b(Bok\u001b=\u001b>")).toBe("ok");
  });
  it("normalises CRLF", () => {
    expect(stripAnsi("a\r\nb")).toBe("a\nb");
  });
});
