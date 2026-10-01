import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { clearChatContentCache, createChatContentAccess } from "./chat-content-reader.ts";
import type { LycaonClient } from "../../../api/client.ts";
import type { ChatContentReference, ChatContentRow } from "../../../api/types.ts";

const preview = (text: string): ChatContentRow[] => Array.from({ length: 64 }, (_, index) => ({ index, offset: index * Array.from(text).length, text, spans: [] }));
const reference = (text: string): ChatContentReference => ({ field: "tool_output", sha256: "revision", total_runes: 100000, size_bytes: 400000, rows: 10000, preview_rows: preview(text) });
describe("retained chat content", () => {
  it("prepares complete Unicode lines without a request", () => {
    clearChatContentCache();
    const fetch = vi.fn();
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: fetch }), sessionId: "s", messageId: "m", reference: reference("🌲 happened.\n") });
    for (let i = 0; i < 64; i++) {
      expect(reader.peek(i)?.text).toBe("🌲 happened.\n");
      expect(reader.peek(i)?.offset).toBe(i * 12);
    }
    expect(fetch).not.toHaveBeenCalled();
  });
  it("isolates equal revision IDs on different connections", () => {
    clearChatContentCache();
    const common = { sessionId: "s", messageId: "m" };
    const first = createChatContentAccess({ ...common, client: stubClient(), reference: reference("first\n") });
    const second = createChatContentAccess({ ...common, client: stubClient(), reference: reference("second\n") });
    expect(first.peek(0)?.text).toBe("first\n");
    expect(second.peek(0)?.text).toBe("second\n");
  });
  it("copies a range through bounded reads without dropping Unicode", async () => {
    const text = "🌲".repeat(20000);
    const get = vi.fn(async (...[, , ref, position]: Parameters<LycaonClient["getChatContent"]>) => {
      if (!("offset" in position)) throw new Error("expected a byte range read");
      const { offset, limit } = position;
      return { reference: ref, offset, end_offset: offset+limit, text: Array.from(text).slice(offset, offset+limit).join(""), complete: offset+limit===20000, spans: [] };
    });
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: get }), sessionId: "s", messageId: "m", reference: reference("seed") });
    expect(await reader.text(0,20000,new AbortController().signal)).toBe(text);
    expect(get).toHaveBeenCalledTimes(2);
    expect(get.mock.calls[0]?.[3]).toEqual({ offset: 0, limit: 16384 });
  });
});
