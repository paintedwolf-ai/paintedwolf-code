import type { ChatContentReference, HostSecretRedactionMeta } from "../../../api/types.ts";

type InlineContent = { kind: "inline"; text: string; redaction?: HostSecretRedactionMeta };
type Content = InlineContent | { kind: "retained"; reference: ChatContentReference } | { kind: "process"; handle: string };

export type ChatContentDocument = {
  sessionId: string;
  pane: string;
  title: string;
  revealOffset?: number;
  find?: { query: string; caseSensitive: boolean; offset?: number };
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
