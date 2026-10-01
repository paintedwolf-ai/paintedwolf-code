// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installHarnessDriver } from "./harness-driver.ts";

const { listMessages } = vi.hoisted(() => ({ listMessages: vi.fn() }));
vi.mock("../connection/app-connection.ts", async (original) => ({
  ...await original<typeof import("../connection/app-connection.ts")>(),
  getLycaonClient: () => ({ listSessionMessages: listMessages }),
}));

describe("harness prompt admission", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    listMessages.mockReset().mockResolvedValue({ messages: [], watermark: 0 });
    document.body.innerHTML = '<div data-testid="chat-stream" data-session-id="session-1"></div><textarea data-testid="chat-composer"></textarea><button data-testid="composer-send" disabled>Send</button>';
    for (const element of document.body.children) {
      Object.defineProperty(element, "offsetParent", { configurable: true, get: () => document.body });
    }
    installHarnessDriver();
  });

  afterEach(() => {
    delete window.__harness;
    document.body.replaceChildren();
    vi.useRealTimers();
  });

  it("reports blocked submission instead of claiming an idle chat completed a prompt", async () => {
    const submit = vi.fn();
    document.querySelector("textarea")!.addEventListener("keydown", submit);
    document.querySelector("button")!.addEventListener("click", submit);
    const result = window.__harness!.prompt("hello");
    await vi.advanceTimersByTimeAsync(6_000);
    expect(await result).toMatchObject({ ok: false });
    expect(submit).not.toHaveBeenCalled();
  });

  it("submits once the actual send control accepts input", async () => {
    const button = document.querySelector("button")!;
    const submit = vi.fn();
    button.addEventListener("click", submit);
    listMessages.mockResolvedValueOnce({ messages: [], watermark: 0 });
    listMessages.mockResolvedValue({ messages: [{ id: "message-1", role: "user", seq: 1 }], watermark: 1 });
    const result = window.__harness!.sendPrompt("hello");
    button.disabled = false;
    await vi.advanceTimersByTimeAsync(500);
    expect(await result).toMatchObject({ ok: true, sent: "hello", messageId: "message-1" });
    expect(submit).toHaveBeenCalledOnce();
  });

  it("does not report completion when an enabled composer rejects its draft", async () => {
    document.querySelector("button")!.disabled = false;
    const result = window.__harness!.prompt("rejected inline draft");
    await vi.advanceTimersByTimeAsync(6_000);
    expect(await result).toMatchObject({ ok: false });
    expect(listMessages).toHaveBeenCalledWith("session-1", { limit: 100, from: "oldest" });
  });

  it("opens a new chat from a full stage without waiting for an absent conversation", async () => {
    document.body.innerHTML = '<button data-testid="new-chat-btn">New chat</button>';
    const button = document.querySelector("button")!;
    Object.defineProperty(button, "offsetParent", { get: () => document.body });
    button.addEventListener("click", () => {
      document.body.insertAdjacentHTML("beforeend", '<div class="den-resident-surface"><div class="den-shell-stage--chat"><div data-testid="chat-stream" data-session-id="new-session"></div><textarea data-testid="chat-composer"></textarea></div></div>');
    });
    const result = window.__harness!.newSession();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await result).toMatchObject({ ok: true, state: { sessionId: "new-session" } });
  });
});
