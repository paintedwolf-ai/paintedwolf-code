import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { readCommandReceiptDocument } from "./command-receipt-document.ts";
import type { CommandReceiptContent } from "../../chat/transcript/content/chat-content-document.ts";

const content: CommandReceiptContent = { kind: "command_receipts", programs: [{ id: "program", activityId: "activity" }], context: '{"facts":[]}' };

describe("execution records document", () => {
  it("reads every record in bounded requests and retains activity scope", async () => {
    const get = vi.fn(async (_session: string, _program: string, opts?: { cursor?: string }) => {
      const start = Number(opts?.cursor ?? 0);
      return { program: { id: "program" }, executions: Array.from({ length: 200 }, (_, index) => ({ id: start + index })), executions_omitted: 10000 - start - 200, next_cursor: String(start + 200) };
    });
    const signal = new AbortController().signal;
    const text = await readCommandReceiptDocument(stubClient({ getCommandProgramReceipt: get }), "session", content, signal);
    expect(text.split("\n")).toHaveLength(10002);
    expect(text).toContain('"id":9999');
    expect(get).toHaveBeenCalledTimes(50);
    expect(get).toHaveBeenLastCalledWith("session", "program", { limit: 200, cursor: "9800", activity_id: "activity", signal });
  });

  it("finishes at the count observed when the document opened", async () => {
    const get = vi.fn().mockResolvedValueOnce({ program: {}, executions: [{ id: 1 }], executions_omitted: 1, next_cursor: "next" })
      .mockResolvedValueOnce({ program: {}, executions: [{ id: 2 }, { id: 3 }], executions_omitted: 100, next_cursor: "later" });
    const text = await readCommandReceiptDocument(stubClient({ getCommandProgramReceipt: get }), "session", content, new AbortController().signal);
    expect(text).toContain('"id":2');
    expect(text).not.toContain('"id":3');
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("rejects incomplete or repeating pages instead of presenting a partial document", async () => {
    for (const next_cursor of [undefined, "same"]) {
      const get = vi.fn().mockResolvedValue({ program: {}, executions: [{ id: 1 }], executions_omitted: 3, next_cursor });
      await expect(readCommandReceiptDocument(stubClient({ getCommandProgramReceipt: get }), "session", content, new AbortController().signal)).rejects.toThrow("read completely");
      expect(get.mock.calls.length).toBeLessThanOrEqual(2);
    }
  });

  it("stops requesting records when the document closes", async () => {
    const controller = new AbortController();
    const get = vi.fn(async () => {
      controller.abort();
      return { program: {}, executions: [{ id: 1 }], executions_omitted: 1, next_cursor: "next" };
    });
    await expect(readCommandReceiptDocument(stubClient({ getCommandProgramReceipt: get }), "session", content, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(get).toHaveBeenCalledOnce();
  });
});
