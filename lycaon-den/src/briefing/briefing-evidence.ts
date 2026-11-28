import type { BriefingEvidenceRef } from "../api/types.ts";
import type { ChatContentDocument } from "../chat/transcript/content/chat-content-document.ts";
import { openFilesSurface } from "../platform/navigation/open-files-surface.ts";

/** Where focus returns after reading evidence: a label and the element to focus. */
type BriefingReturnAnchor = { label: string; focus: () => void };

/** A document the summarized activity already offers, such as its input or receipt. */
export type BriefingRecordLink = { label: string; document: ChatContentDocument };

type Opened = { returnAnchor?: BriefingReturnAnchor; records: readonly BriefingRecordLink[] };

const opened = new Map<string, Opened>();
const MAX_OPENED = 64;

/** Opens an evidence record in Files; each record has its own tab. */
export function openBriefingEvidence(
  reference: BriefingEvidenceRef,
  returnAnchor?: BriefingReturnAnchor,
  records: readonly BriefingRecordLink[] = [],
): void {
  if (returnAnchor || records.length > 0) {
    opened.delete(reference.record_id);
    opened.set(reference.record_id, { returnAnchor, records });
    while (opened.size > MAX_OPENED) {
      const oldest = opened.keys().next().value;
      if (oldest === undefined) break;
      opened.delete(oldest);
    }
  }
  openFilesSurface({ kind: "briefing-evidence", projectId: reference.project_id, recordId: reference.record_id });
}

export function briefingReturnAnchor(recordId: string): BriefingReturnAnchor | undefined {
  return opened.get(recordId)?.returnAnchor;
}

export function briefingRecordLinks(recordId: string): readonly BriefingRecordLink[] {
  return opened.get(recordId)?.records ?? [];
}
