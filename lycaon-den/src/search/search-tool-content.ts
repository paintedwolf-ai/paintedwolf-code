import type { LycaonClient } from "../api/client.ts";
import type { Message, SearchHit } from "../api/types.ts";
import type { ChatContentDocument } from "../chat/transcript/content/chat-content-document.ts";

export function searchToolContentLabel(hit: SearchHit): string | undefined {
  if (hit.hit_kind !== "tool" || !hit.project_id?.trim() ||
      !hit.session_id?.trim() || !hit.source_ref?.trim()) return undefined;
  return hit.source === "message" ? "Input" : "Output";
}

/** Search snippets can omit content from the recorded call. */
export async function loadSearchToolContent(
  client: Pick<LycaonClient, "listSessionMessages">,
  hit: SearchHit,
  signal: AbortSignal,
): Promise<ChatContentDocument> {
  const label = searchToolContentLabel(hit);
  const sessionId = hit.session_id?.trim();
  const toolCallId = hit.source_ref?.trim();
  if (!label || !sessionId || !toolCallId) throw new Error("This search result has no recorded tool address.");
  let before: string | undefined;
  while (true) {
    signal.throwIfAborted();
    const page = await client.listSessionMessages(sessionId, { limit: 100, before });
    signal.throwIfAborted();
    for (const message of page.messages) {
      const content = contentFromMessage(message, toolCallId, label);
      if (content) return {
        kind: "tool", sessionId, messageId: message.id, toolCallId,
        pane: label === "Input" ? "args" : "output",
        title: `${label} · ${hit.title}`, content,
      };
    }
    if (!page.before_cursor || page.before_cursor === before) break;
    before = page.before_cursor;
  }
  throw new Error("The recorded tool content is no longer available.");
}

function contentFromMessage(
  message: Message,
  toolCallId: string,
  label: string,
): ChatContentDocument["content"] | undefined {
  const result = message.tool_result;
  if (label === "Output") {
    if (result?.tool_call_id !== toolCallId) return undefined;
    return result.content_ref
      ? { kind: "retained", reference: result.content_ref }
      : { kind: "inline", text: result.content, redaction: message.host_secret_redaction };
  }
  const call = message.tool_calls?.find(candidate => candidate.id === toolCallId);
  const matchingResult = result?.tool_call_id === toolCallId ? result : undefined;
  const reference = call?.args_ref ?? matchingResult?.tool_args_ref;
  if (reference) return { kind: "retained", reference };
  const args = call?.args ?? matchingResult?.tool_args;
  if (!args) return undefined;
  return { kind: "inline", text: JSON.stringify(args, null, 2) };
}
