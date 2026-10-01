import { workerTranscriptMarkdownSource } from "../markdown/markdown-output.ts";
import type { DisplayTranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import type { VirtualListMatch } from "../../find/virtual-list-find.ts";
import { findLiteralOffsets } from "../../find/find-match.ts";
import { searchToolPart } from "../tool/tool-part-find.ts";

/** Comparison readers keep their existing search and reveal contract. */
export function workerItemHasExternalFind(item: DisplayTranscriptItem): boolean {
  return item.kind === "file_edit" || item.kind === "worker_file_edit";
}

export async function searchWorkerItem(item: DisplayTranscriptItem, sessionId: string | undefined, query: string, sensitive: boolean, signal: AbortSignal): Promise<VirtualListMatch[]> {
  if (workerItemHasExternalFind(item)) return [];
  const parts = item.kind === "tool" ? [item.part] : item.kind === "worker_group" ? item.parts :
    item.kind === "activity_span" ? item.entries.flatMap(entry => entry.kind === "tool" ? [entry.part] : []) : [];
  if (parts.length) {
    const found: VirtualListMatch[] = [];
    for (const part of parts) {
      if (signal.aborted || found.length>=10000) break;
      found.push(...(await searchToolPart(part,sessionId,query,sensitive,signal)).map(match => ({ ...match,toolCallId:part.toolCallId })));
    }
    return found.slice(0,10000);
  }
  const text = "text" in item ? workerTranscriptMarkdownSource(item.text) : "content" in item ? item.content : "label" in item ? item.label :
    "steps" in item ? item.steps.map(step=>step.label).join("\n") : item.kind === "checkpoint" ? [item.meta.subject,item.meta.causing_command].filter(Boolean).join("\n") : "";
  return findLiteralOffsets(text,query,sensitive).map(match=>({ from:match.start,to:match.start+match.length }));
}
