import type { Accessor } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ChatContentReference, ChatContentRow, RedactedSpan } from "../../../api/types.ts";
import { PRETTY_MAX_INPUT } from "../../../components/source/pretty/pretty-layout.ts";
import { ByteCache, retainedValueBytes } from "../../../utils/byte-cache.ts";

/** One display row. Rows partition the document text, so each keeps its line break. */
export type ChatContentDocumentRow = {
  readonly index: number;
  readonly text: string;
  readonly spans: readonly RedactedSpan[];
};

export type ChatContentBoundedSnapshot = {
  readonly revision: string;
  readonly text: string;
  readonly spans: RedactedSpan[];
};

/** The whole document for Pretty, its length when it is too large, or null when it cannot be read whole. */
export type ChatContentPrettySnapshot = ChatContentBoundedSnapshot | { readonly limit: number } | null;

/** A Find match: a rune range of the whole document, or of one row. */
export type ChatContentMatch =
  | { readonly offset: number; readonly length: number }
  | { readonly row: number; readonly column: number; readonly length: number };

export type ChatContentFindPage = { readonly matches: readonly ChatContentMatch[]; readonly next_cursor?: string };

/** Rows to load for a match, and its UTF-16 range within the matched row. */
export type ChatContentRevealTarget = {
  readonly rows: readonly ChatContentDocumentRow[];
  readonly row: number;
  readonly from: number;
  readonly to: number;
};

/** A read-only document read in row windows. */
export type ChatContentAccess = {
  /** Identity of the rows on display; a new revision invalidates every row read before it. */
  revision: Accessor<string>;
  rows: Accessor<number>;
  /** False while the records behind the document may still change. */
  settled: Accessor<boolean>;
  previewRows: () => readonly ChatContentDocumentRow[];
  position: () => number;
  rememberPosition: (row: number) => void;
  peek: (index: number) => ChatContentDocumentRow | undefined;
  read: (index: number, signal: AbortSignal) => Promise<ChatContentDocumentRow[]>;
  copyAll: (signal: AbortSignal) => Promise<string>;
  prettySnapshot: (signal: AbortSignal) => Promise<ChatContentPrettySnapshot>;
  find: (query: string, cursor: string | undefined, caseSensitive: boolean, signal: AbortSignal) => Promise<ChatContentFindPage>;
  revealTarget: (match: ChatContentMatch, signal: AbortSignal) => Promise<ChatContentRevealTarget | undefined>;
  /** Adopts the current revision. */
  refresh: (signal: AbortSignal) => Promise<void>;
};

/** Access to one retained message field, which keeps its reference for links back to it. */
export type RetainedChatContentAccess = ChatContentAccess & {
  readonly reference: ChatContentReference;
  readonly messageId: string;
};

const rows = new ByteCache<string, ChatContentDocumentRow>(4 * 1024 * 1024, 8192);
const positions = new ByteCache<string, number>(512 * 1024, 2048);
const snapshots = new ByteCache<string, ChatContentBoundedSnapshot>(4 * 1024 * 1024, 64);

/** Row, position and snapshot caches shared by every document access. */
export const chatContentCaches = { rows, positions, snapshots };

/** Whether retained content is too large to load whole for Pretty. A character is at most four UTF-8 bytes. */
function exceedsPrettyInput(reference: { total_runes: number; size_bytes: number }): boolean {
  return reference.total_runes > PRETTY_MAX_INPUT || reference.size_bytes > 4 * PRETTY_MAX_INPUT;
}

/** UTF-16 range of a rune range within text. */
export function runeRangeToUtf16(text: string, start: number, length: number): { from: number; to: number } {
  const runes = Array.from(text);
  const from = runes.slice(0, start).join("").length;
  return { from, to: from + runes.slice(start, start + length).join("").length };
}

export function createChatContentAccess(options: {
  client: LycaonClient;
  sessionId: string;
  messageId: string;
  reference: ChatContentReference;
}): RetainedChatContentAccess {
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
  const text = async (from: number, to: number, signal: AbortSignal) => {
    const parts: string[] = [];
    for (let offset = from; offset < to;) {
      const page = await client.getChatContent(sessionId, messageId, reference, { offset, limit: Math.min(16384, to-offset) }, signal);
      if (signal.aborted) throw new DOMException("Canceled", "AbortError");
      if (page.end_offset <= offset) throw new Error("Content could not be read completely.");
      parts.push(page.text); offset = page.end_offset;
    }
    return parts.join("");
  };
  return {
    reference, messageId,
    revision: () => reference.sha256,
    rows: () => reference.rows,
    settled: () => true,
    previewRows: () => reference.preview_rows ?? [],
    position: () => positions.get(prefix) ?? 0,
    rememberPosition: row => positions.set(prefix,row,prefix.length*2+32),
    peek: index => rows.get(prefix + index),
    read: (row, signal) => readPage({ row }, signal),
    copyAll: signal => text(0, reference.total_runes, signal),
    prettySnapshot: async (signal) => {
      if (exceedsPrettyInput(reference)) return { limit: reference.total_runes };
      const cacheKey = `${prefix}snapshot`;
      const cached = snapshots.get(cacheKey);
      if (cached?.revision === reference.sha256) return cached;
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
      const result: ChatContentBoundedSnapshot = { revision: reference.sha256, text: parts.join(""), spans };
      snapshots.set(cacheKey, result, retainedValueBytes(result) + cacheKey.length * 2);
      return result;
    },
    find: async (query, cursor, sensitive, signal) => {
      const page = await client.searchChatContent(sessionId, messageId, reference, query, cursor, sensitive, signal);
      return { matches: page.matches, next_cursor: page.next_cursor };
    },
    revealTarget: async (match, signal) => {
      if ("row" in match) {
        const loaded = await readPage({ row: match.row }, signal);
        const row = loaded.find(value => value.index === match.row);
        return row && { rows: loaded, row: row.index, ...runeRangeToUtf16(row.text, match.column, match.length) };
      }
      const loaded = await readPage({ locate: match.offset }, signal);
      const row = loaded[0];
      return row && { rows: loaded, row: row.index, ...runeRangeToUtf16(row.text, match.offset - row.offset, match.length) };
    },
    refresh: async () => {},
  };
}

const connections = new WeakMap<object, number>();
let sequence = 0;
export function connectionKey(client: object): number {
  let id = connections.get(client);
  if (id === undefined) { id = ++sequence; connections.set(client, id); }
  return id;
}
export function clearChatContentCache(): void { rows.clear(); positions.clear(); snapshots.clear(); }
