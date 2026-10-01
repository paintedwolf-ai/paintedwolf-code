import type { ToolPartView } from "./tool-part-model.ts";
import { findLiteralOffsets } from "../../find/find-match.ts";
import type { VirtualListMatch } from "../../find/virtual-list-find.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

export const TOOL_CONTENT_REVEAL_EVENT = "den:tool-content-reveal";

/** Search card data without creating its body or downloading retained output. */
export async function searchToolPart(part: ToolPartView, sessionId: string | undefined, query: string, sensitive: boolean, signal: AbortSignal): Promise<VirtualListMatch[]> {
  const matches: VirtualListMatch[] = [];
  for (const [field, text, reference, messageId] of [
    ["title", part.title ?? "", undefined, part.messageId],
    ["tool_args", JSON.stringify(part.args ?? {}), part.argsReference, part.argsMessageId ?? part.assistantMessageId],
    ["tool_output", part.output ?? "", part.outputReference, part.messageId],
  ] as const) {
    if (matches.length >= 10000 || signal.aborted) break;
    const client = reference ? getLycaonClient() : undefined;
    if (!reference || !client || !sessionId) {
      matches.push(...findLiteralOffsets(text, query, sensitive, 10000-matches.length).map(match => ({ from: match.start, to: match.start+match.length, field })));
      continue;
    }
    for (let cursor: string | undefined = undefined; matches.length < 10000; ) {
      const page = await client.searchChatContent(sessionId, messageId, reference, query, cursor, sensitive, signal);
      if (signal.aborted) return [];
      matches.push(...page.matches.map(match => ({ from: match.offset, to: match.offset+match.length, field })));
      if (!page.next_cursor) break;
      cursor = page.next_cursor;
    }
  }
  return matches.slice(0,10000);
}
