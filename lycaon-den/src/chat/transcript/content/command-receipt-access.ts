import { createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { CommandReceiptDocumentPage } from "../../../api/types.ts";
import { isApiErrorCode } from "../../../api/http.ts";
import { PRETTY_MAX_INPUT } from "../../../components/source/pretty/pretty-layout.ts";
import { retainedValueBytes } from "../../../utils/byte-cache.ts";
import type { CommandReceiptScope } from "./chat-content-document.ts";
import { chatContentCaches, connectionKey, runeRangeToUtf16, type ChatContentAccess, type ChatContentDocumentRow, type ChatContentPrettySnapshot } from "./chat-content-reader.ts";

/** Scopes per host request. */
export const COMMAND_RECEIPT_SEGMENT_SCOPES = 64;
export const COMMAND_RECEIPT_VIEWPORT_ROWS = 64;
export const COMMAND_RECEIPT_STREAM_ROWS = 500;
export const COMMAND_RECEIPT_SEARCH_LIMIT = 500;
/** Largest whole receipt Copy writes, in UTF-16 code units. */
export const COMMAND_RECEIPT_COPY_MAX = 8 * 1024 * 1024;

export class CommandReceiptLimitError extends Error {
  constructor(message: string) { super(message); this.name = "CommandReceiptLimitError"; }
}

const tooLarge = "This receipt is too large to copy whole. Select a smaller range.";
const stillChanging = "This receipt is still changing. Try again when the command finishes.";

type Segment = {
  readonly scopes: readonly string[];
  readonly revision: string;
  readonly total: number;
  readonly settled: boolean;
};

export function formatCommandReceiptScope(scope: CommandReceiptScope): string {
  return scope.activityId ? `${scope.programId}:${scope.activityId}` : scope.programId;
}

const changed = (cause: unknown) => isApiErrorCode(cause, ["chat_content_changed"]);

function documentRow(index: number, text: string): ChatContentDocumentRow {
  return { index, text: `${text}\n`, spans: [] };
}

/**
 * Opens the host-written receipt for scopes. Scopes are read in segments the
 * host accepts in one request and presented as one document.
 */
export async function openCommandReceiptAccess(options: {
  client: LycaonClient;
  sessionId: string;
  scopes: readonly CommandReceiptScope[];
  signal: AbortSignal;
}): Promise<ChatContentAccess> {
  const { client, sessionId } = options;
  const names = options.scopes.map(formatCommandReceiptScope);
  const groups: string[][] = [];
  for (let at = 0; at < names.length; at += COMMAND_RECEIPT_SEGMENT_SCOPES) groups.push(names.slice(at, at + COMMAND_RECEIPT_SEGMENT_SCOPES));
  const prefix = `${connectionKey(client)}:${sessionId}:receipt:${names.join(",")}:`;
  const { rows: cache, positions, snapshots } = chatContentCaches;
  const rowKey = (segment: Segment, local: number) => `${prefix}${segment.scopes[0]}:${segment.revision}:${local}`;
  const remember = (segment: Segment, page: CommandReceiptDocumentPage) => {
    for (const row of page.rows) {
      const value = documentRow(row.index, row.text);
      const key = rowKey(segment, row.index);
      cache.set(key, value, retainedValueBytes(value) + key.length * 2);
    }
  };
  const fetchPage = async (scopes: readonly string[], window: { offset: number; limit: number; revision?: string }, signal: AbortSignal) => {
    const page = await client.getCommandReceiptDocument(sessionId, scopes, window, signal);
    if (signal.aborted) throw new DOMException("Canceled", "AbortError");
    return page;
  };
  const bootstrap = (signal: AbortSignal) => Promise.all(groups.map(async scopes => {
    const page = await fetchPage(scopes, { offset: 0, limit: COMMAND_RECEIPT_VIEWPORT_ROWS }, signal);
    const segment: Segment = { scopes, revision: page.revision, total: page.total_rows, settled: page.settled };
    remember(segment, page);
    return segment;
  }));
  const [segments, setSegments] = createSignal<readonly Segment[]>(await bootstrap(options.signal));
  const adopt = (at: number, page: CommandReceiptDocumentPage) => {
    const segment: Segment = { scopes: groups[at]!, revision: page.revision, total: page.total_rows, settled: page.settled };
    remember(segment, page);
    setSegments(current => current.map((value, index) => index === at ? segment : value));
    return segment;
  };
  const starts = () => {
    const out: number[] = [];
    let total = 0;
    for (const segment of segments()) { out.push(total); total += segment.total; }
    return out;
  };
  const locate = (row: number) => {
    const first = starts();
    let at = 0;
    while (at + 1 < first.length && first[at + 1]! <= row) at++;
    return { at, local: row - (first[at] ?? 0), start: first[at] ?? 0 };
  };
  const rows = () => segments().reduce((sum, segment) => sum + segment.total, 0);
  const revision = () => segments().map(segment => segment.revision).join(":");
  const refresh = async (signal: AbortSignal) => { setSegments(await bootstrap(signal)); };
  /** Runs a whole-document read on one revision, restarting once if the receipt changes. */
  const consistent = async <T,>(run: () => Promise<T>, signal: AbortSignal): Promise<T> => {
    try {
      return await run();
    } catch (cause) {
      if (!changed(cause)) throw cause;
    }
    await refresh(signal);
    try {
      return await run();
    } catch (cause) {
      if (changed(cause)) throw new CommandReceiptLimitError(stillChanging);
      throw cause;
    }
  };
  /** Streams every row in order until visit returns false. */
  const stream = async (visit: (text: string) => boolean, signal: AbortSignal) => {
    for (const segment of segments()) {
      for (let offset = 0; offset < segment.total;) {
        const page = await fetchPage(segment.scopes, { offset, limit: COMMAND_RECEIPT_STREAM_ROWS, revision: segment.revision }, signal);
        if (page.end_offset <= offset) throw new Error("The receipt could not be read completely.");
        for (const row of page.rows) if (!visit(`${row.text}\n`)) return false;
        offset = page.end_offset;
      }
    }
    return true;
  };
  const peek = (index: number) => {
    const { at, local, start } = locate(index);
    const segment = segments()[at];
    const row = segment && local < segment.total ? cache.get(rowKey(segment, local)) : undefined;
    return row && { ...row, index: start + local };
  };
  const read = async (index: number, signal: AbortSignal) => {
    const { at, local, start } = locate(index);
    const segment = segments()[at]!;
    const window = { offset: local, limit: COMMAND_RECEIPT_VIEWPORT_ROWS };
    let page: CommandReceiptDocumentPage;
    try {
      page = await fetchPage(segment.scopes, { ...window, revision: segment.revision }, signal);
      remember(segment, page);
    } catch (cause) {
      if (!changed(cause)) throw cause;
      // An unpinned read cannot be refused and carries the revision it read.
      page = await fetchPage(segment.scopes, window, signal);
      adopt(at, page);
    }
    return page.rows.map(row => documentRow(start + row.index, row.text));
  };
  const oversized = new Map<string, number>();
  return {
    revision, rows, peek, read, refresh,
    settled: () => segments().every(segment => segment.settled),
    previewRows: () => {
      const first = starts();
      return segments().flatMap((segment, at) => {
        const out: ChatContentDocumentRow[] = [];
        for (let local = 0; local < Math.min(segment.total, COMMAND_RECEIPT_VIEWPORT_ROWS); local++) {
          const row = cache.get(rowKey(segment, local));
          if (!row) break;
          out.push({ ...row, index: first[at]! + local });
        }
        return out;
      });
    },
    position: () => positions.get(prefix) ?? 0,
    rememberPosition: row => positions.set(prefix, row, prefix.length * 2 + 32),
    copyAll: signal => consistent(async () => {
      const parts: string[] = [];
      let units = 0;
      await stream(text => {
        units += text.length;
        if (units > COMMAND_RECEIPT_COPY_MAX) throw new CommandReceiptLimitError(tooLarge);
        parts.push(text);
        return true;
      }, signal);
      return parts.join("");
    }, signal),
    prettySnapshot: signal => consistent(async (): Promise<ChatContentPrettySnapshot> => {
      const current = revision();
      const length = oversized.get(current);
      if (length !== undefined) return { limit: length };
      const key = `${prefix}${current}:snapshot`;
      const cached = snapshots.get(key);
      if (cached) return cached;
      const parts: string[] = [];
      let units = 0;
      const whole = await stream(text => {
        units += text.length;
        parts.push(text);
        return units <= PRETTY_MAX_INPUT;
      }, signal);
      if (!whole) {
        oversized.set(current, units);
        return { limit: units };
      }
      const snapshot = { revision: current, text: parts.join(""), spans: [] };
      snapshots.set(key, snapshot, retainedValueBytes(snapshot) + key.length * 2);
      return snapshot;
    }, signal),
    find: async (query, cursor, caseSensitive, signal) => {
      // The cursor names the segment, then the host cursor within it.
      const split = cursor?.indexOf(":") ?? -1;
      const at = split < 0 ? 0 : Number(cursor!.slice(0, split));
      const hostCursor = split < 0 ? undefined : cursor!.slice(split + 1) || undefined;
      const segment = segments()[at];
      if (!segment) return { matches: [] };
      const page = await client.searchCommandReceiptDocument(sessionId, segment.scopes, { revision: segment.revision, q: query, caseSensitive, cursor: hostCursor, limit: COMMAND_RECEIPT_SEARCH_LIMIT }, signal);
      const start = starts()[at] ?? 0;
      const next = page.next_cursor ? `${at}:${page.next_cursor}` : at + 1 < segments().length ? `${at + 1}:` : undefined;
      return { matches: page.matches.map(match => ({ row: start + match.row, column: match.column, length: match.length })), next_cursor: next };
    },
    revealTarget: async (match, signal) => {
      if (!("row" in match)) return undefined;
      const cached = peek(match.row);
      const loaded = cached ? [cached] : await read(match.row, signal);
      const row = loaded.find(value => value.index === match.row);
      return row && { rows: loaded, row: row.index, ...runeRangeToUtf16(row.text, match.column, match.length) };
    },
  };
}
