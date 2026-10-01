import { Text } from "@codemirror/state";
import { Chunk } from "@codemirror/merge";

import { SOURCE_DIFF_CONFIG } from "../../components/source/diff/source-text-diff.ts";

export type DiffLineHunk =
  | { kind: "equal"; beforeStart: number; beforeEnd: number; afterStart: number; afterEnd: number }
  | { kind: "change"; beforeStart: number; beforeEnd: number; afterStart: number; afterEnd: number };

/** CRLF and LF compare as the same line ending. */
function stripTrailingCR(lines: string[]): string[] {
  return lines.map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
}

export function computeDiffHunks(before: string, after: string): DiffLineHunk[] {
  if (before === after) return [];
  const a = Text.of(stripTrailingCR(before.split("\n")));
  const b = Text.of(stripTrailingCR(after.split("\n")));
  const changes = Chunk.build(a, b, SOURCE_DIFF_CONFIG);
  return diffHunksFromChunks(a, b, changes);
}

/** Restore controls use the same chunks that the editor paints. */
export function diffHunksFromChunks(a: Text, b: Text, changes: readonly Chunk[]): DiffLineHunk[] {
  const hunks: DiffLineHunk[] = [];
  let beforeLine = 0;
  let afterLine = 0;
  for (const change of changes) {
    const beforeStart = lineIndexAt(a, change.fromA);
    const beforeEnd = lineEndIndex(a, change.fromA, change.toA);
    const afterStart = lineIndexAt(b, change.fromB);
    const afterEnd = lineEndIndex(b, change.fromB, change.toB);
    if (beforeLine < beforeStart || afterLine < afterStart) {
      hunks.push({
        kind: "equal",
        beforeStart: beforeLine,
        beforeEnd: beforeStart,
        afterStart: afterLine,
        afterEnd: afterStart,
      });
    }
    hunks.push({
      kind: "change",
      beforeStart,
      beforeEnd,
      afterStart,
      afterEnd,
    });
    beforeLine = beforeEnd;
    afterLine = afterEnd;
  }
  if (beforeLine < a.lines || afterLine < b.lines) {
    hunks.push({
      kind: "equal",
      beforeStart: beforeLine,
      beforeEnd: a.lines,
      afterStart: afterLine,
      afterEnd: b.lines,
    });
  }
  return hunks;
}

function lineIndexAt(doc: Text, position: number): number {
  return doc.lineAt(Math.min(position, doc.length)).number - 1;
}

function lineEndIndex(doc: Text, from: number, to: number): number {
  if (from === to) return lineIndexAt(doc, from);
  return doc.lineAt(Math.min(Math.max(from, to - 1), doc.length)).number;
}

function lineOffset(text: string, lineIndex: number): number {
  if (lineIndex <= 0) return 0;
  let offset = 0;
  for (let line = 0; line < lineIndex; line++) {
    const newline = text.indexOf("\n", offset);
    if (newline < 0) return text.length;
    offset = newline + 1;
  }
  return offset;
}

export function applyHunkReject(
  currentAfter: string,
  before: string,
  hunk: DiffLineHunk,
): string {
  if (hunk.kind !== "change") return currentAfter;
  const afterFrom = lineOffset(currentAfter, hunk.afterStart);
  const afterTo = lineOffset(currentAfter, hunk.afterEnd);
  const beforeFrom = lineOffset(before, hunk.beforeStart);
  const beforeTo = lineOffset(before, hunk.beforeEnd);
  return currentAfter.slice(0, afterFrom) +
    before.slice(beforeFrom, beforeTo) +
    currentAfter.slice(afterTo);
}

/** Includes a deletion at the requested after-side line. */
export function changeHunkAtAfterLine(
  hunks: readonly DiffLineHunk[],
  afterLine0: number,
): DiffLineHunk | null {
  for (const hunk of hunks) {
    if (hunk.kind !== "change") continue;
    if (hunk.afterStart === hunk.afterEnd) {
      if (afterLine0 === hunk.afterStart) return hunk;
      continue;
    }
    if (afterLine0 >= hunk.afterStart && afterLine0 < hunk.afterEnd) return hunk;
  }
  return null;
}
