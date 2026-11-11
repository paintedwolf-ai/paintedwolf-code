import type { BriefingEvidenceRef } from "../api/types.ts";
import { openFilesSurface } from "../platform/navigation/open-files-surface.ts";

/** Where focus returns after reading evidence: a label and the element to focus. */
export type BriefingReturnAnchor = { label: string; focus: () => void };

const returnAnchors = new Map<string, BriefingReturnAnchor>();
const MAX_RETURN_ANCHORS = 64;

/** Opens an evidence record in Files; each record has its own tab. */
export function openBriefingEvidence(reference: BriefingEvidenceRef, returnAnchor?: BriefingReturnAnchor): void {
  if (returnAnchor) {
    returnAnchors.delete(reference.record_id);
    returnAnchors.set(reference.record_id, returnAnchor);
    while (returnAnchors.size > MAX_RETURN_ANCHORS) {
      const oldest = returnAnchors.keys().next().value;
      if (oldest === undefined) break;
      returnAnchors.delete(oldest);
    }
  }
  openFilesSurface({ kind: "briefing-evidence", projectId: reference.project_id, recordId: reference.record_id });
}

export function briefingReturnAnchor(recordId: string): BriefingReturnAnchor | undefined {
  return returnAnchors.get(recordId);
}
