import type { ChatContentReference, HostSecretRedactionMeta } from "../../../api/types.ts";

type InlineContent = { kind: "inline"; text: string; redaction?: HostSecretRedactionMeta };
/** A whole recorded program, or the executions one host activity selects. */
export type CommandReceiptScope = { readonly programId: string; readonly activityId?: string };
export type CommandReceiptContent = { kind: "command_receipts"; programs: { id: string; activityId?: string }[]; context: string };
type Content = CommandReceiptContent | InlineContent | { kind: "retained"; reference: ChatContentReference } | { kind: "process"; handle: string };

export type ChatContentDocument = {
  sessionId: string;
  pane: string;
  title: string;
  revealOffset?: number;
  find?: { query: string; caseSensitive: boolean; offset?: number };
  /** The file the content was read from, which decides its layout. */
  path?: string;
  /** The content's language where no path names it. */
  language?: string;
} & (
  | { kind: "tool"; messageId: string; toolCallId: string; content: Content }
  | { kind: "approval"; checkpointId: string; content: InlineContent }
);

export function chatContentDocumentKey(document: ChatContentDocument): string {
  const identity = document.kind === "tool"
    ? [document.messageId, document.toolCallId]
    : [document.checkpointId];
  return `chat:${JSON.stringify([document.kind, document.sessionId, ...identity, document.pane])}`;
}
