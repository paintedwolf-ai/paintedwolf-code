import { describe, expect, it, vi } from "vitest";
import type { ChatContentReference, Message, SearchHit, SessionTranscriptPage } from "../api/types.ts";
import { loadSearchToolContent } from "./search-tool-content.ts";

const hit: SearchHit = { hit_id: "hit", hit_kind: "tool", source: "tool", project_id: "project",
  session_id: "worker-session", parent_session_id: "parent", source_ref: "call", title: "summarize", snippet: "truncated" };
const reference: ChatContentReference = { field: "tool_output", sha256: "hash", total_runes: 9000, size_bytes: 9000, rows: 20 };
const message = (extra: Partial<Message>): Message => ({ id: "result", role: "tool", content: "", origin: "tool",
  authority: "none", trust_tier: "untrusted", created_at: "2026-09-15T00:00:00Z", ...extra } as Message);
const page = (messages: Message[], extra: Partial<SessionTranscriptPage> = {}): SessionTranscriptPage => ({
  messages, watermark: 1, turn_clocks: {}, turn_loads: {}, ...extra,
});

describe("loadSearchToolContent", () => {
  it("finds retained output on an older page in the originating worker session", async () => {
    const listSessionMessages = vi.fn()
      .mockResolvedValueOnce(page([message({ tool_result: { tool_call_id: "other", content: "wrong" } })], { before_cursor: "cursor-100" }))
      .mockResolvedValueOnce(page([message({ tool_result: { tool_call_id: "call", content: "preview", content_ref: reference } })]));
    const document = await loadSearchToolContent({ listSessionMessages }, hit, new AbortController().signal);
    expect(document).toMatchObject({ sessionId: "worker-session", messageId: "result", toolCallId: "call", pane: "output",
      content: { kind: "retained", reference } });
    expect(listSessionMessages).toHaveBeenNthCalledWith(2, "worker-session", { limit: 100, before: "cursor-100" });
  });

  it("opens complete inline output and preserves its redaction metadata", async () => {
    const text = "full output\n".repeat(500);
    const redaction = { spans: [] };
    const listSessionMessages = vi.fn().mockResolvedValue(page([message({ host_secret_redaction: redaction,
      tool_result: { tool_call_id: "call", content: text } })]));
    const document = await loadSearchToolContent({ listSessionMessages }, hit, new AbortController().signal);
    expect(document.content).toEqual({ kind: "inline", text, redaction });
  });

  it("opens input references from the result's immutable argument snapshot", async () => {
    const argsRef = { ...reference, field: "tool_args" as const, tool_call_id: "call" };
    const listSessionMessages = vi.fn().mockResolvedValue(page([message({ tool_result: {
      tool_call_id: "call", content: "output", tool_args_ref: argsRef,
    } })]));
    const document = await loadSearchToolContent({ listSessionMessages }, { ...hit, source: "message" }, new AbortController().signal);
    expect(document).toMatchObject({ pane: "args", content: { kind: "retained", reference: argsRef } });
  });

  it("opens the exact assistant call's input rather than another call in its batch", async () => {
    const listSessionMessages = vi.fn().mockResolvedValue(page([message({ role: "assistant", tool_calls: [
      { id: "other", name: "read", args: { path: "wrong" } }, { id: "call", name: "read", args: { path: "right" } },
    ] })]));
    const document = await loadSearchToolContent({ listSessionMessages }, { ...hit, source: "message" }, new AbortController().signal);
    expect(document.content).toEqual({ kind: "inline", text: '{\n  "path": "right"\n}' });
  });

  it("does not open snippets when history is missing or paging cannot advance", async () => {
    const listSessionMessages = vi.fn().mockResolvedValue(page([], { before_cursor: "same" }));
    await expect(loadSearchToolContent({ listSessionMessages }, hit, new AbortController().signal)).rejects.toThrow("no longer available");
    expect(listSessionMessages).toHaveBeenCalledTimes(2);
  });

  it("stops pending navigation when the selected hit changes", async () => {
    const controller = new AbortController();
    const listSessionMessages = vi.fn().mockImplementation(async () => {
      controller.abort();
      return page([message({ tool_result: { tool_call_id: "call", content: "old result" } })]);
    });
    await expect(loadSearchToolContent({ listSessionMessages }, hit, controller.signal)).rejects.toThrow();
    expect(listSessionMessages).toHaveBeenCalledTimes(1);
  });
});
