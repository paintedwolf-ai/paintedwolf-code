import type { ToolPartView } from "./tool-part-model.ts";
import { isApiErrorCode } from "../../api/http.ts";
import { findLiteralOffsets } from "../../find/find-match.ts";
import type { VirtualListMatch } from "../../find/virtual-list-find.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

export const TOOL_CONTENT_REVEAL_EVENT = "den:tool-content-reveal";

/** Text Find searches for a collapsed card, from its data; retained content searches remotely. */
export function toolPartFindCorpus(part: ToolPartView): string {
  if (part.commandActivity) return [part.commandActivity.fact.label, part.commandActivity.fact.subject, part.commandActivity.fact.outcome, ...part.commandActivity.calls.map(toolPartFindCorpus)].join("\n");
  return [part.title, part.displaySubject, part.argsReference ? "" : JSON.stringify(part.args), part.outputReference ? "" : part.output]
    .filter(Boolean)
    .join("\n");
}

/** Search card data without creating its body or downloading retained output. */
export async function searchToolPart(part: ToolPartView, sessionId: string | undefined, query: string, sensitive: boolean, signal: AbortSignal): Promise<VirtualListMatch[]> {
  const matches: VirtualListMatch[] = [];
  if (part.commandActivity) {
    const fact = part.commandActivity.fact;
    const header = [fact.label, fact.subject, fact.outcome].join(" · ");
    matches.push(...findLiteralOffsets(header, query, sensitive, 10000).map(match => ({ from: match.start, to: match.start + match.length, field: "title", toolCallId: part.toolCallId })));
    for (const call of part.commandActivity.calls) {
      if (signal.aborted || matches.length >= 10000) break;
      const found = await searchToolPart(call, sessionId, query, sensitive, signal);
      matches.push(...found.map(match => ({ ...match, toolCallId: call.toolCallId })));
    }
    return matches.slice(0, 10000);
  }
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
    const collect = async () => {
      const found: typeof matches = [];
      for (let cursor: string | undefined = undefined; matches.length + found.length < 10000; ) {
        const page = await client.searchChatContent(sessionId, messageId, reference, query, cursor, sensitive, signal);
        if (signal.aborted) return [];
        found.push(...page.matches.map(match => ({ from: match.offset, to: match.offset+match.length, field })));
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return found;
    };
    // An engine restart expires cursors; the field is searched again once.
    matches.push(...await collect().catch(cause => {
      if (isApiErrorCode(cause, ["cursor_generation_expired"])) return collect();
      throw cause;
    }));
    if (signal.aborted) return [];
  }
  return matches.slice(0,10000);
}
