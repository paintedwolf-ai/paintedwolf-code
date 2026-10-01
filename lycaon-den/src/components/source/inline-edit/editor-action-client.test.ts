import { stubClient } from "../../../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { waitEditorAction } from "./editor-action-client.ts";

const receipt = { id: "action-1", role: "user", ord: 9 };
const options = { messageId: receipt.id, afterMessageId: "m4", afterOrd: 4, timeoutMs: 1_000 };

afterEach(() => vi.useRealTimers());

describe("waitEditorAction", () => {
  it("waits for its accepted submission even when the session starts idle", async () => {
    vi.useFakeTimers();
    const listSessionMessages = vi.fn()
      .mockResolvedValueOnce({ messages: [{ id: "other", ord: 7 }] })
      .mockResolvedValueOnce({ messages: [receipt] });
    const getSession = vi.fn().mockResolvedValue({ status: "idle" });
    const done = vi.fn();
    const pending = waitEditorAction(stubClient({ getSession, listSessionMessages }), "s1", options);
    void pending.then(done);
    await vi.advanceTimersByTimeAsync(0);
    expect(done).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(200);
    await expect(pending).resolves.toBe(9);
    expect(listSessionMessages).toHaveBeenLastCalledWith("s1", { afterMessageId: "other", limit: 500 });
  });

  it("recognizes a fast completion without requiring an observed busy transition", async () => {
    const getSession = vi.fn().mockResolvedValue({ status: "idle" });
    await expect(waitEditorAction(stubClient({
      getSession,
      listSessionMessages: vi.fn().mockResolvedValue({ messages: [receipt] }),
    }), "s1", options)).resolves.toBe(9);
    expect(getSession).toHaveBeenCalledTimes(1);
  });

  it("waits for settlement after the matching submission appears", async () => {
    vi.useFakeTimers();
    const getSession = vi.fn()
      .mockResolvedValueOnce({ status: "busy" })
      .mockResolvedValue({ status: "idle" });
    const listSessionMessages = vi.fn().mockResolvedValue({ messages: [receipt] });
    const pending = waitEditorAction(stubClient({ getSession, listSessionMessages }), "s1", options);
    await vi.advanceTimersByTimeAsync(200);
    await expect(pending).resolves.toBe(9);
    expect(listSessionMessages).toHaveBeenCalledTimes(1);
    expect(getSession).toHaveBeenCalledTimes(2);
  });

  it("finds the exact submission across transcript pages", async () => {
    const listSessionMessages = vi.fn()
      .mockResolvedValueOnce({ messages: [{ id: "other", ord: 7 }], after_cursor: "cur7" })
      .mockResolvedValueOnce({ messages: [receipt] });
    await expect(waitEditorAction(stubClient({
      listSessionMessages,
      getSession: vi.fn().mockResolvedValue({ status: "idle" }),
    }), "s1", options)).resolves.toBe(9);
    expect(listSessionMessages).toHaveBeenLastCalledWith("s1", { after: "cur7", limit: 500 });
  });

  it("refuses a nonadvancing transcript page", async () => {
    await expect(waitEditorAction(stubClient({
      listSessionMessages: vi.fn().mockResolvedValue({ messages: [], after_cursor: "cur4" }),
    }), "s1", options)).rejects.toThrow("transcript did not advance");
  });

  it("rejects a failed session before or after the submission starts", async () => {
    for (const messages of [[], [receipt]]) {
      await expect(waitEditorAction(stubClient({
        listSessionMessages: vi.fn().mockResolvedValue({ messages }),
        getSession: vi.fn().mockResolvedValue({ status: "error" }),
      }), "s1", options)).rejects.toThrow("failed before it completed");
    }
  });

  it("times out if idle never includes the accepted submission", async () => {
    vi.useFakeTimers();
    const pending = waitEditorAction(stubClient({
      listSessionMessages: vi.fn().mockResolvedValue({ messages: [] }),
      getSession: vi.fn().mockResolvedValue({ status: "idle" }),
    }), "s1", options);
    const assertion = expect(pending).rejects.toThrow("timed out");
    await vi.advanceTimersByTimeAsync(1_000);
    await assertion;
  });

  it("honors cancellation while a session read is in flight", async () => {
    const controller = new AbortController();
    await expect(waitEditorAction(stubClient({
      listSessionMessages: vi.fn().mockResolvedValue({ messages: [receipt] }),
      getSession: vi.fn().mockImplementation(async () => {
        controller.abort();
        return { status: "idle" };
      }),
    }), "s1", { ...options, signal: controller.signal })).rejects.toMatchObject({ name: "AbortError" });
  });
});
