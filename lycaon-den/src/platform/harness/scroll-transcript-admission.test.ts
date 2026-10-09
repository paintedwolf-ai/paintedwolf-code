// @vitest-environment jsdom
import { beforeEach, expect, it, vi } from "vitest";
import { goto, type JumpDeps } from "./harness-jump.ts";
import { harnessControlFetch } from "../connection/backend.ts";

vi.mock("../connection/backend.ts", () => ({ harnessControlFetch: vi.fn() }));
vi.mock("../connection/app-connection.ts", () => ({ getRegisteredNoticeStore: vi.fn() }));
const control = vi.mocked(harnessControlFetch);
const deps: JumpDeps = {
  readState: () => ({ sessionId: "current-session" }),
  waitForSettledSession: async () => ({ ok: true }),
  liveByTestid: () => null,
  liveResolve: () => null,
  waitForIdle: vi.fn(),
  sendPrompt: vi.fn(),
  llmManual: vi.fn(),
  llmRespond: vi.fn(),
};
beforeEach(() => vi.clearAllMocks());

it("admits fixture rows to the currently selected session without model work", async () => {
  control.mockResolvedValue({ ok: true });
  expect(await goto(deps, "scroll-transcript", { turns: 5, activity: true })).toMatchObject({ ok: true });
  expect(control).toHaveBeenCalledOnce();
  const [path, init] = control.mock.calls[0]!;
  expect(path).toBe("/harness/transcript");
  const body = JSON.parse(String(init?.body));
  expect(body.session_id).toBe("current-session");
  expect(body.messages.filter((row: { role: string }) => row.role === "tool")).toHaveLength(3);
  expect(deps.sendPrompt).not.toHaveBeenCalled();
  expect(deps.llmRespond).not.toHaveBeenCalled();
});

it("reports refused fixture admission and propagates control failures", async () => {
  control.mockResolvedValue({ ok: false });
  expect(await goto(deps, "scroll-transcript")).toMatchObject({ ok: false });
  control.mockRejectedValue(new Error("fixture authorization failed"));
  await expect(goto(deps, "scroll-transcript")).rejects.toThrow("fixture authorization failed");
});

it("requires a selected session before attempting fixture admission", async () => {
  expect(await goto({ ...deps, readState: () => ({}) }, "scroll-transcript")).toMatchObject({ ok: false });
  expect(control).not.toHaveBeenCalled();
});
