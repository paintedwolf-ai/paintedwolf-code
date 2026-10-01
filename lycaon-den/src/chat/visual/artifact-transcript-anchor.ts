/** Tool-produced media anchors to its tool call because result messages have no row key. */

import type { ArtifactListItem } from "../../api/types.ts";
import type { TranscriptRevealAnchor } from "../transcript/presentation/transcript-reveal-target.ts";

/** The row an artifact reveals to, or null when it has no addressable row. */
export function artifactTranscriptAnchor(
  item: Pick<ArtifactListItem, "tool_call_id" | "origin_message_id"> | null,
): TranscriptRevealAnchor | null {
  if (!item) return null;
  const toolCallId = item.tool_call_id?.trim() ?? "";
  if (toolCallId) return { chicklet: "tool", anchorId: toolCallId };
  const messageId = item.origin_message_id?.trim() ?? "";
  if (messageId) return { chicklet: "message", anchorId: messageId };
  return null;
}
