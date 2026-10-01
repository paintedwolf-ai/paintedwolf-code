/** Editor findings also determine which stage actions are available. */

import type { FindingLineMark } from "../../components/source/annotations/knowing-gutter-model.ts";
import type { SecurityFinding } from "../../api/types.ts";
import { createSignal } from "solid-js";

type Entry = {
  marks: readonly FindingLineMark[];
};

const byBuffer = new Map<string, Entry>();
const [findingsRevision, setFindingsRevision] = createSignal(0);

export function setFilesEditorFindings(
  bufferKey: string,
  marks: readonly FindingLineMark[],
): void {
  byBuffer.set(bufferKey, { marks });
  setFindingsRevision((revision) => revision + 1);
}

export function clearFilesEditorFindings(bufferKey: string): void {
  byBuffer.delete(bufferKey);
  setFindingsRevision((revision) => revision + 1);
}

/** First finding whose mark/locations overlap [startLine, endLine] inclusive. */
export function findingOverlappingRange(
  bufferKey: string,
  startLine: number,
  endLine: number,
): SecurityFinding | null {
  findingsRevision();
  const entry = byBuffer.get(bufferKey);
  if (!entry) return null;
  const a = Math.min(startLine, endLine);
  const b = Math.max(startLine, endLine);
  for (const m of entry.marks) {
    if (m.line < a || m.line > b) continue;
    const first = m.findings[0];
    if (first) return first;
  }
  for (const m of entry.marks) {
    for (const f of m.findings) {
      for (const loc of f.locations ?? []) {
        const ls = loc.start_line ?? 0;
        const le = loc.end_line ?? ls;
        if (le < a || ls > b) continue;
        return f;
      }
    }
  }
  return null;
}
