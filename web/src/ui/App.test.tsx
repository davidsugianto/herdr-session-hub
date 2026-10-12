import { describe, expect, it } from "vitest";
import { parseRoute } from "./App";

describe("parseRoute", () => {
  it("maps hashes to routes", () => {
    expect(parseRoute("")).toEqual({ name: "board" });
    expect(parseRoute("#/")).toEqual({ name: "board" });
    expect(parseRoute("#/timeline")).toEqual({ name: "timeline" });
    expect(parseRoute("#/keys")).toEqual({ name: "keys" });
    expect(parseRoute("#/settings")).toEqual({ name: "settings" });
    expect(parseRoute("#/nope")).toEqual({ name: "board" });
  });

  it("decodes agent ids (which contain a slash)", () => {
    expect(parseRoute("#/a/labs%2Fterm_abc")).toEqual({ name: "detail", id: "labs/term_abc" });
  });

  it("keeps a malformed escape as the raw id instead of throwing", () => {
    expect(parseRoute("#/a/%E0%A4%A")).toEqual({ name: "detail", id: "%E0%A4%A" });
  });
});
