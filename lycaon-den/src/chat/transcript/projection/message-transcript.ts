import type { Message } from "../../../api/types.ts";

/** Span bookkeeping row hidden from chat and model history. */
export function isWorkflowBoundaryMessage(msg: Message): boolean {
  return msg.kind === "workflow_boundary" || msg.workflow_boundary != null;
}

/** Host row hidden from Den chat (internal kicks, ambient boundaries). */
export function isInternalTranscriptMessage(msg: Message): boolean {
  return msg.visibility === "internal";
}

/** Host synthetic user turns hidden from chat transcript (wire visibility). */
export function isInternalTranscriptUserMessage(msg: Message): boolean {
  return msg.role === "user" && msg.visibility === "internal";
}

/** Whether a row belongs in the visible transcript. */
export function isDenTranscriptMessage(msg: Message): boolean {
  if (isInternalTranscriptMessage(msg)) return false;
  return true;
}

/** Whether a session contains any user-visible transcript row. */
export function hasDenTranscriptMessages(messages: readonly Message[]): boolean {
  return messages.some(isDenTranscriptMessage);
}

/** Tool result with the exact durable assistant/call parent identity. */
export function findToolResult(
  messages: readonly Message[],
  assistantMessageId: string,
  toolCallId: string,
): Message | undefined {
  const parentId = assistantMessageId.trim();
  const id = toolCallId.trim();
  if (!parentId || !id) return undefined;
  return messages.find(
    (candidate) =>
      candidate.role === "tool" &&
      candidate.tool_result?.assistant_message_id?.trim() === parentId &&
      candidate.tool_result?.tool_call_id?.trim() === id,
  );
}
