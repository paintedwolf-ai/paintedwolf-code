import type { SourceComparisonSummary } from "../../../api/types.ts";
import type { ScopeChangeKind } from "../../../files/components/files-scope-change-mark.ts";
import type { ReaderSide } from "./source-reader-document.ts";

/** What a comparison did to its file; `absent` means neither side has a file. */
export type SourceReaderChange = ScopeChangeKind | "absent";

export function sourceChangeFromPresence(before: boolean, after: boolean): SourceReaderChange {
  if (before) return after ? "changed" : "deleted";
  return after ? "added" : "absent";
}

/** Availability is the host's presence fact; row kinds only describe lines. */
export function sourceReaderChange(reader: Pick<SourceComparisonSummary, "before" | "after">): SourceReaderChange {
  return sourceChangeFromPresence(reader.before.availability !== "absent", reader.after.availability !== "absent");
}

/** The change when it covers the whole file. */
export function wholeFileChange(change: SourceReaderChange | undefined): "added" | "deleted" | undefined {
  return change === "added" || change === "deleted" ? change : undefined;
}

/** The same name mark for a recorded or proposed operation. */
export function wholeFileOpChange(op: string): "added" | "deleted" | undefined {
  return op === "create" ? "added" : op === "delete" ? "deleted" : undefined;
}

/** An added file reads as the file itself; a deleted file stays visibly removed. */
export function readerShowsChangeMarks(change: SourceReaderChange): boolean {
  return change === "changed" || change === "deleted";
}

/** The side whose lines number the document. */
export function readerContentSide(change: SourceReaderChange): ReaderSide {
  return change === "deleted" ? "before" : "after";
}

/** Two present sides enable folding and side-by-side comparison. */
export function readerComparesSides(change: SourceReaderChange): boolean {
  return change === "changed";
}
