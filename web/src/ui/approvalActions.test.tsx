import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "preact";
import { act } from "preact/test-utils";
import type { AgentDetail } from "../lib/types";

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, approve: vi.fn(), trust: vi.fn(), deny: vi.fn(), getAgentDetail: vi.fn() };
});
vi.mock("./Toast", () => ({ showToast: vi.fn(), errMsg: (e: unknown) => String(e) }));

import * as api from "../lib/api";
import { showToast } from "./Toast";
import { useAgentDetail, useApprovalActions } from "./approvalActions";

const detail = (hash: string): AgentDetail => ({
  id: "s/1", session: "s", session_label: "s", terminal_id: "1", pane_id: "p", name: "a", kind: "claude", cwd: "/",
  title: "", state: "blocked", raw_status: "blocked", since: "2026-01-01T00:00:00Z", online: true,
  prompt: { profile: "claude", verified: true, prompt: { text: "Allow?", options: ["Yes", "No"], hash } },
});

let root: HTMLElement;
beforeEach(() => {
  root = document.createElement("div");
  document.body.appendChild(root);
  vi.clearAllMocks();
});
afterEach(() => {
  render(null, root);
  root.remove();
  vi.useRealTimers();
});

type Actions = ReturnType<typeof useApprovalActions>;
function mountActions(d: AgentDetail | null, refetch = vi.fn()) {
  const out: { cur?: Actions } = {};
  const H = () => { out.cur = useApprovalActions("s/1", d, refetch); return null; };
  act(() => { render(<H />, root); });
  return { out, refetch };
}

describe("useApprovalActions", () => {
  it("approve sends the prompt hash from the detail and refetches", async () => {
    vi.mocked(api.approve).mockResolvedValue();
    const { out, refetch } = mountActions(detail("h1"));
    let ok = false;
    await act(async () => { ok = await out.cur!.run("approve"); });
    expect(ok).toBe(true);
    expect(api.approve).toHaveBeenCalledWith("s/1", "h1");
    expect(refetch).toHaveBeenCalled();
  });

  it("refuses to act without a prompt hash", async () => {
    const { out } = mountActions(null);
    let ok = true;
    await act(async () => { ok = await out.cur!.run("approve"); });
    expect(ok).toBe(false);
    expect(api.approve).not.toHaveBeenCalled();
    expect(showToast).toHaveBeenCalledWith("No prompt to act on");
  });

  it("prompt_changed refetches the new prompt instead of acting", async () => {
    vi.mocked(api.trust).mockRejectedValue(new api.ApiError(409, "prompt_changed", "changed"));
    const { out, refetch } = mountActions(detail("h1"));
    let ok = true;
    await act(async () => { ok = await out.cur!.run("trust"); });
    expect(ok).toBe(false);
    expect(refetch).toHaveBeenCalled();
    expect(showToast).toHaveBeenCalledWith("Prompt changed, review the new one");
  });

  it("not_blocked refetches", async () => {
    vi.mocked(api.approve).mockRejectedValue(new api.ApiError(409, "not_blocked", "not blocked"));
    const { out, refetch } = mountActions(detail("h1"));
    await act(async () => { await out.cur!.run("approve"); });
    expect(refetch).toHaveBeenCalled();
  });

  it("action_unavailable marks only that action unavailable", async () => {
    vi.mocked(api.trust).mockRejectedValue(new api.ApiError(422, "action_unavailable", "no trust here"));
    const { out } = mountActions(detail("h1"));
    await act(async () => { await out.cur!.run("trust"); });
    expect(out.cur!.unavailable.trust).toBe("no trust here");
    expect(out.cur!.unavailable.approve).toBeUndefined();
  });

  it("deny passes the note; rate limiting only toasts", async () => {
    vi.mocked(api.deny).mockResolvedValue();
    const { out } = mountActions(detail("h1"));
    await act(async () => { await out.cur!.run("deny", "use 5s"); });
    expect(api.deny).toHaveBeenCalledWith("s/1", "h1", "use 5s");
    vi.mocked(api.approve).mockRejectedValue(new api.ApiError(429, "rate_limited", "slow down"));
    const refetch = vi.fn();
    const m = mountActions(detail("h1"), refetch);
    await act(async () => { await m.out.cur!.run("approve"); });
    expect(showToast).toHaveBeenCalledWith("slow down");
    expect(refetch).not.toHaveBeenCalled();
  });
});

describe("useAgentDetail", () => {
  function mountDetail(state: string, poll = true) {
    const H = () => { useAgentDetail("s/1", true, state, poll); return null; };
    act(() => { render(<H />, root); });
  }

  it("polls while blocked so a second prompt replaces the old one", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getAgentDetail).mockResolvedValue(detail("h1"));
    mountDetail("blocked");
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const first = vi.mocked(api.getAgentDetail).mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(6500); });
    expect(vi.mocked(api.getAgentDetail).mock.calls.length).toBeGreaterThanOrEqual(first + 2);
  });

  it("does not poll unless asked to (board cards fetch once)", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getAgentDetail).mockResolvedValue(detail("h1"));
    mountDetail("blocked", false);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const first = vi.mocked(api.getAgentDetail).mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(vi.mocked(api.getAgentDetail).mock.calls.length).toBe(first);
  });

  it("skips a poll tick while the previous request is still pending", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getAgentDetail).mockReturnValue(new Promise(() => {}));
    mountDetail("blocked");
    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(vi.mocked(api.getAgentDetail).mock.calls.length).toBe(1);
  });

  it("does not poll when not blocked", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getAgentDetail).mockResolvedValue({ ...detail("h1"), state: "working" });
    mountDetail("working");
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const first = vi.mocked(api.getAgentDetail).mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(vi.mocked(api.getAgentDetail).mock.calls.length).toBe(first);
  });
});

describe("ApprovalCard", () => {
  it("leaves deny mode when the prompt changes under the user", async () => {
    const { ApprovalCard } = await import("./ApprovalCard");
    const agent = detail("h1");
    const show = (d: AgentDetail) => act(() => { render(<ApprovalCard agent={agent} detail={d} onDone={() => {}} />, root); });
    show(detail("h1"));
    const deny = [...root.querySelectorAll("button")].find((b) => b.textContent === "Deny")!;
    act(() => { deny.click(); });
    expect(root.textContent).toContain("Confirm deny");
    show(detail("h2"));
    expect(root.textContent).not.toContain("Confirm deny");
    expect(root.textContent).toContain("Approve");
  });
});
