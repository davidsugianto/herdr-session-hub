import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { consumePairFragment, deviceName, pairWithCode } from "./pair";
import { auth, resetStore } from "./store";

beforeEach(() => {
  resetStore();
  history.replaceState(null, "", "/?x=1");
});
afterEach(() => vi.unstubAllGlobals());

describe("consumePairFragment", () => {
  it("returns null and leaves the hash when absent", () => {
    history.replaceState(null, "", "/#other=1");
    expect(consumePairFragment()).toBeNull();
    expect(location.hash).toBe("#other=1");
  });

  it("returns the decoded code and clears the hash", () => {
    history.replaceState(null, "", "/?x=1#pair=ab%20cd-12");
    expect(consumePairFragment()).toBe("ab cd-12");
    expect(location.hash).toBe("");
    expect(location.pathname + location.search).toBe("/?x=1");
  });

  it("keeps malformed escapes raw and clears the hash", () => {
    history.replaceState(null, "", "/#pair=%E0%A4%A");
    expect(consumePairFragment()).toBe("%E0%A4%A");
    expect(location.hash).toBe("");
  });
});

describe("deviceName", () => {
  it("derives a short label from the UA", () => {
    expect(deviceName("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1")).toBe("iPhone Safari");
    expect(deviceName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0 Safari/537.36")).toBe("Mac Chrome");
    expect(deviceName("Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0")).toBe("Linux Firefox");
    expect(deviceName("???")).toBe("Browser");
    expect(deviceName("Windows " + "Chrome/1 ".repeat(50)).length).toBeLessThanOrEqual(100);
  });
});

describe("pairWithCode", () => {
  it("posts code + device_name and sets auth to paired", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ id: "d" }), { status: 200 }));
    vi.stubGlobal("fetch", f);
    await pairWithCode("CODE");
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/pair");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["X-Requested-With"]).toBe("hub");
    const body = JSON.parse(init.body as string);
    expect(body.code).toBe("CODE");
    expect(typeof body.device_name).toBe("string");
    expect(body.device_name.length).toBeLessThanOrEqual(100);
    expect(auth.value).toBe("paired");
  });

  it("throws ApiError on 401 without flipping auth to unpaired", async () => {
    auth.value = "unknown";
    vi.stubGlobal("fetch", vi.fn(async () =>
      new Response(JSON.stringify({ error: { code: "invalid_code", message: "bad" } }), { status: 401 })));
    const err = await pairWithCode("x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, code: "invalid_code" });
    expect(auth.value).toBe("unknown");
  });
});
