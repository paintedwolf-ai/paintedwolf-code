import type { ActivitySpanEntry, TranscriptItem } from "../projection/transcript-item-model.ts";
import type { ToolPartView } from "../../tool/tool-part-model.ts";

/**
 * Digest of what sets a row's height, so a remembered measurement is reused only for the
 * content it measured. A row keeps its key while its content changes (an activity span gains
 * entries, a tool settles with output, a reply is edited); the digest changes with it.
 * Rows keep their object identity while unchanged, so the digest is computed once per row
 * object, and tool output is summarized by its length rather than hashed.
 */
const revisionByItem = new WeakMap<TranscriptItem, string>();

function fnv1a(text: string, seed = 0x811c9dc5): number {
  let hash = seed;
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

function toolPartFacts(part: ToolPartView): string {
  return [
    part.id,
    part.status,
    part.title ?? "",
    part.displaySubject ?? "",
    part.output?.length ?? -1,
    part.error ? part.error.length : -1,
    part.fileEdit ? `${part.fileEdit.added ?? ""}/${part.fileEdit.removed ?? ""}` : "",
    part.visual?.id ?? "",
    part.outcome ?? "",
    part.codes?.length ?? 0,
    part.feedback?.length ?? 0,
  ].join("\u0001");
}

function entryFacts(entry: ActivitySpanEntry): string {
  switch (entry.kind) {
    case "tool": return toolPartFacts(entry.part);
    case "turn_load": return `turn_load\u0001${entry.key}\u0001${JSON.stringify(entry.row.load)}`;
    case "index_warming": return `index_warming\u0001${entry.key}\u0001${JSON.stringify(entry.meta)}`;
  }
}

function rowFacts(item: TranscriptItem): string {
  switch (item.kind) {
    case "user":
      return `${item.text}\u0001${(item.artifactIds ?? []).join(",")}`;
    case "pending_user":
    case "assistant":
    case "fallback":
      return item.text;
    case "draft":
      return `${item.text}\u0001${item.draftStatus ?? ""}\u0001${item.draftVersionCount ?? 0}`;
    case "tool":
      return toolPartFacts(item.part);
    case "activity_span":
      return `${item.label}\u0002${item.entries.map(entryFacts).join("\u0002")}`;
    case "worker_group":
      return item.parts.map(toolPartFacts).join("\u0002");
    case "file_edit":
    case "worker_file_edit":
      return item.folds
        .map((fold) => `${fold.key}\u0001${fold.path}\u0001${fold.steps.length}\u0001${fold.net.added ?? ""}/${fold.net.removed ?? ""}`)
        .join("\u0002");
    case "progress_complete":
      return item.steps.map((step) => step.label).join("\u0002");
    case "progress_update":
      return JSON.stringify([item.initial, item.steps, item.changes, item.summary ?? null]);
    case "workflow_feedback":
    case "workflow_explain":
    case "index_warming":
    case "checkpoint":
      return JSON.stringify(item.meta);
    case "blueprint_card":
      return `${item.view.revisionKey}\u0001${item.view.phase}\u0001${item.view.collapsed}\u0001${item.content.length}`;
    case "workflow_boundary":
      return item.label;
    case "turn_load":
      return JSON.stringify(item.row.load);
    case "time_marker":
      return `${item.variant}\u0001${item.at}`;
    case "unread_marker":
      return String(item.seenAt);
    case "turn_tail":
      return `${item.settledAt ?? ""}\u0001${item.activeMs}\u0001${item.workMs}`;
  }
}

/** Digest of the content that sets a row's height. */
export function transcriptRowContentRevision(item: TranscriptItem): string {
  const known = revisionByItem.get(item);
  if (known !== undefined) return known;
  const facts = rowFacts(item);
  const revision = `${facts.length.toString(36)}.${fnv1a(facts).toString(36)}`;
  revisionByItem.set(item, revision);
  return revision;
}
