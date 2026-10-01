import type { LycaonClient } from "../../../api/client.ts";
import type { ChatContentReference, ChatContentRow } from "../../../api/types.ts";
import { ByteCache, retainedValueBytes } from "../../../utils/byte-cache.ts";

const rows = new ByteCache<string, ChatContentRow>(4 * 1024 * 1024, 8192);
const positions = new ByteCache<string, number>(512 * 1024, 2048);
export type ChatContentAccess = {
  reference: ChatContentReference;
  messageId: string;
  position: () => number;
  rememberPosition: (row: number) => void;
  peek: (index: number) => ChatContentRow | undefined;
  read: (index: number, signal: AbortSignal) => Promise<ChatContentRow[]>;
  locate: (offset: number, signal: AbortSignal) => Promise<ChatContentRow[]>;
  text: (from: number, to: number, signal: AbortSignal) => Promise<string>;
  search: (query: string, cursor: string | undefined, caseSensitive: boolean, signal: AbortSignal) => ReturnType<LycaonClient["searchChatContent"]>;
};

/** The observer projection carries the first viewport before a card is clicked. */
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
export function clearChatContentCache(): void { rows.clear(); positions.clear(); }
