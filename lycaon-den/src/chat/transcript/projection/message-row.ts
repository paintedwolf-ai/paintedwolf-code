import type { Message } from "../../../api/types.ts";
import { latestMessageSnapshot, messageLiveFieldsEqual } from "./messages-equal.ts";

export function upsertMessageRow(messages: Message[], row: Message): Message[] {
  const id = row.id?.trim();
  if (!id) return messages;
  const idx = messages.findIndex((m) => m.id === id);
  if (idx >= 0) {
    const existing = messages[idx]!;
    if (latestMessageSnapshot(existing, row) === existing) return messages;
    if (messageLiveFieldsEqual(existing, row)) return messages;
    const next = messages.slice();
    next[idx] = row;
    return next;
  }
  return [...messages, row];
}
