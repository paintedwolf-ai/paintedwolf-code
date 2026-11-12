import type { LycaonClient } from "../../../api/client.ts";
import type { ChatContentReference, ChatContentRow, RedactedSpan } from "../../../api/types.ts";
import { PRETTY_MAX_INPUT } from "../../../components/source/pretty/pretty-layout.ts";
import { ByteCache, retainedValueBytes } from "../../../utils/byte-cache.ts";

export type ChatContentBoundedSnapshot = {
  readonly sha256: string;
  readonly totalRunes: number;
  readonly text: string;
  readonly spans: RedactedSpan[];
};

const rows = new ByteCache<string, ChatContentRow>(4 * 1024 * 1024, 8192);
const positions = new ByteCache<string, number>(512 * 1024, 2048);
const snapshots = new ByteCache<string, ChatContentBoundedSnapshot>(4 * 1024 * 1024, 64);
export type ChatContentAccess = {
  reference: ChatContentReference;
  messageId: string;
  position: () => number;
  rememberPosition: (row: number) => void;
  peek: (index: number) => ChatContentRow | undefined;
  read: (index: number, signal: AbortSignal) => Promise<ChatContentRow[]>;
  locate: (offset: number, signal: AbortSignal) => Promise<ChatContentRow[]>;
  text: (from: number, to: number, signal: AbortSignal) => Promise<string>;
  readBoundedSnapshot: (signal: AbortSignal) => Promise<ChatContentBoundedSnapshot | null>;
  search: (query: string, cursor: string | undefined, caseSensitive: boolean, signal: AbortSignal) => ReturnType<LycaonClient["searchChatContent"]>;
};

/** The observer projection carries the first viewport before a card is clicked. */
/** Whether retained content is too large to load whole for Pretty. A character is at most four UTF-8 bytes. */
export function exceedsPrettyInput(reference: { total_runes: number; size_bytes: number }): boolean {
  return reference.total_runes > PRETTY_MAX_INPUT || reference.size_bytes > 4 * PRETTY_MAX_INPUT;
}

export function createChatContentAccess(options: {
  client: LycaonClient;
  sessionId: string;
  messageId: string;
  reference: ChatContentReference;
}): ChatContentAccess {
  const { client, sessionId, messageId, reference } = options;
  const prefix = `${connectionKey(client)}:${sessionId}:${messageId}:${reference.field}:${reference.tool_call_id ?? ""}:${reference.sha256}:`;
  const remember = (values: ChatContentRow[]) => {
    for (const row of values) rows.set(prefix + row.index, row, retainedValueBytes(row) + prefix.length * 2);
    return values;
  };
  remember(reference.preview_rows ?? []);
  const readPage = async (position: { row: number } | { locate: number }, signal: AbortSignal) => {
    const page = await client.getChatContent(sessionId, messageId, reference, position, signal);
    if (signal.aborted) throw new DOMException("Canceled", "AbortError");
    return remember(page.rows ?? []);
  };
  return {
    reference, messageId,
    position: () => positions.get(prefix) ?? 0,
    rememberPosition: row => positions.set(prefix,row,prefix.length*2+32),
    peek: index => rows.get(prefix + index),
    read: (row, signal) => readPage({ row }, signal),
    locate: (locate, signal) => readPage({ locate }, signal),
    text: async (from, to, signal) => {
      const parts: string[] = [];
      for (let offset = from; offset < to;) {
        const page = await client.getChatContent(sessionId, messageId, reference, { offset, limit: Math.min(16384, to-offset) }, signal);
        if (signal.aborted) throw new DOMException("Canceled", "AbortError");
        if (page.end_offset <= offset) throw new Error("Content could not be read completely.");
        parts.push(page.text); offset = page.end_offset;
      }
      return parts.join("");
    },
    readBoundedSnapshot: async (signal) => {
      if (exceedsPrettyInput(reference)) return null;
      const cacheKey = `${prefix}snapshot`;
      const cached = snapshots.get(cacheKey);
      if (cached && cached.sha256 === reference.sha256 && cached.totalRunes === reference.total_runes) {
        return cached;
      }
      if (reference.total_runes === 0) {
        const empty: ChatContentBoundedSnapshot = {
          sha256: reference.sha256,
          totalRunes: 0,
          text: "",
          spans: [],
        };
        snapshots.set(cacheKey, empty, prefix.length * 2 + 64);
        return empty;
      }
      const parts: string[] = [];
      const spans: RedactedSpan[] = [];
      let utf16Total = 0;
      for (let offset = 0; offset < reference.total_runes; ) {
        const page = await client.getChatContent(
          sessionId,
          messageId,
          reference,
          { offset, limit: Math.min(16384, reference.total_runes - offset) },
          signal,
        );
        if (signal.aborted) throw new DOMException("Canceled", "AbortError");
        if (!page || page.reference?.sha256 !== reference.sha256) return null;
        if (page.offset !== offset || page.end_offset <= offset || page.end_offset > reference.total_runes) {
          return null;
        }
        const pageRunes = Array.from(page.text).length;
        if (pageRunes !== page.end_offset - page.offset) return null;
        if (!Array.isArray(page.spans)) return null;
        for (const span of page.spans) {
          if (
            typeof span.start !== "number" ||
            typeof span.length !== "number" ||
            span.start < 0 ||
            span.length < 0 ||
            span.start + span.length > pageRunes
          ) {
            return null;
          }
          spans.push({ ...span, start: offset + span.start });
        }
        utf16Total += page.text.length;
        if (utf16Total > PRETTY_MAX_INPUT) return null;
        parts.push(page.text);
        offset = page.end_offset;
        if (offset === reference.total_runes && page.complete === false) return null;
      }
      const fullText = parts.join("");
      const result: ChatContentBoundedSnapshot = {
        sha256: reference.sha256,
        totalRunes: reference.total_runes,
        text: fullText,
        spans,
      };
      snapshots.set(cacheKey, result, retainedValueBytes(result) + cacheKey.length * 2);
      return result;
    },
    search: (query, cursor, sensitive, signal) => client.searchChatContent(sessionId, messageId, reference, query, cursor, sensitive, signal),
  };
}

const connections = new WeakMap<object, number>();
let sequence = 0;
function connectionKey(client: object): number {
  let id = connections.get(client);
  if (id === undefined) { id = ++sequence; connections.set(client, id); }
  return id;
}
export function clearChatContentCache(): void { rows.clear(); positions.clear(); snapshots.clear(); }
