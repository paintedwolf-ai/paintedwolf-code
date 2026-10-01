import type { ReaderBoundary } from "./source-reader-document.ts";

export type ReaderTextAccess = {
  selection(start: ReaderBoundary, end: ReaderBoundary): Promise<string>;
  content(side: "before" | "after"): Promise<string>;
};

/** Selection endpoints use source coordinates, including unloaded text between them. */
export function selectedReaderText(access: Pick<ReaderTextAccess, "selection">, start: ReaderBoundary, end: ReaderBoundary): Promise<string> {
  return access.selection(start, end);
}
