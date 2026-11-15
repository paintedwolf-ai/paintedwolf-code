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
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: get }), sessionId: "s", messageId: "m", reference: { ...reference("seed"), total_runes: 20000 } });
    expect(await reader.copyAll(new AbortController().signal)).toBe(text);
    expect(get).toHaveBeenCalledTimes(2);
    expect(get.mock.calls[0]?.[3]).toEqual({ offset: 0, limit: 16384 });
  });

  it("gathers complete bounded snapshot text and shifted redaction spans for the same reference sha256 and rejects mismatched sha256 or gaps (FMT-AC-08)", async () => {
    clearChatContentCache();
    const page1 = '{"secret":"';
    const page2 = '[REDACTED]","b":2}';
    const ref: ChatContentReference = {
      field: "tool_output",
      sha256: "sha-ok",
      total_runes: Array.from(page1 + page2).length,
      size_bytes: (page1 + page2).length,
      rows: 1,
      preview_rows: [],
    };
    const get = vi.fn(async (...[, , , position]: Parameters<LycaonClient["getChatContent"]>) => {
      if (!("offset" in position)) throw new Error("expected offset read");
      if (position.offset === 0) {
        return {
          reference: ref,
          offset: 0,
          end_offset: Array.from(page1).length,
          text: page1,
          complete: false,
          spans: [],
        };
      }
      return {
        reference: ref,
        offset: Array.from(page1).length,
        end_offset: ref.total_runes,
        text: page2,
        complete: true,
        spans: [{ field: "tool_result.content", start: 0, length: 10, kind: "secret", rule_title: "API Key" }],
      };
    });
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: get }), sessionId: "s", messageId: "m", reference: ref });
    const snap = await reader.prettySnapshot(new AbortController().signal);
    expect(snap).toEqual({
      revision: "sha-ok",
      text: '{"secret":"[REDACTED]","b":2}',
      spans: [{ field: "tool_result.content", start: 11, length: 10, kind: "secret", rule_title: "API Key" }],
    });

    // Mismatched sha256 returns null instead of publishing stale/unprotected text.
    clearChatContentCache();
    const badShaGet = vi.fn(async () => ({
      reference: { ...ref, sha256: "different-sha" },
      offset: 0,
      end_offset: ref.total_runes,
      text: page1 + page2,
      complete: true,
      spans: [],
    }));
    const badReader = createChatContentAccess({ client: stubClient({ getChatContent: badShaGet }), sessionId: "s", messageId: "m", reference: ref });
    expect(await badReader.prettySnapshot(new AbortController().signal)).toBeNull();
  });

  it("reports the length of content too large for Pretty without reading it", async () => {
    const get = vi.fn();
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: get }), sessionId: "s", messageId: "m", reference: { ...reference("x"), total_runes: 3_000_000, size_bytes: 3_000_000 } });
    expect(await reader.prettySnapshot(new AbortController().signal)).toEqual({ limit: 3_000_000 });
    expect(get).not.toHaveBeenCalled();
  });

  it("reveals a document offset as a range of its row", async () => {
    const rows: ChatContentRow[] = [{ index: 7, offset: 70, text: "🌲 needle here\n", spans: [] }];
    const get = vi.fn(async (..._args: Parameters<LycaonClient["getChatContent"]>) => ({ reference: reference("x"), rows, offset: 70, end_offset: 85, complete: false, text: "", spans: [] }));
    const reader = createChatContentAccess({ client: stubClient({ getChatContent: get }), sessionId: "s", messageId: "m", reference: reference("x") });
    const target = await reader.revealTarget({ offset: 72, length: 6 }, new AbortController().signal);
    expect(get.mock.calls[0]?.[3]).toEqual({ locate: 72 });
    expect(target).toEqual({ rows, row: 7, from: 3, to: 9 });
  });
});
