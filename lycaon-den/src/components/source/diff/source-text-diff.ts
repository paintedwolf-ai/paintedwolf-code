import { Change, diff, type DiffConfig } from "@codemirror/merge";
import { diffLines } from "diff";
import { sourceLineAnchors } from "../reader/source-line-anchors.ts";

const DIFF_BUDGET_MS = 25;
const LINE_BUDGET_MS = 12;

/** Line anchors preserve unchanged context between distant edits. */
export function diffSourceText(before: string, after: string): readonly Change[] {
  if (before === after) return [];
  const deadline = performance.now() + DIFF_BUDGET_MS;
  const changes: Change[] = [];
  let fromA = 0, fromB = 0;
  for (const anchor of sourceLineAnchors(before, after)) {
    refineLines(before, after, fromA, anchor.fromA, fromB, anchor.fromB, deadline, changes);
    fromA = anchor.toA;
    fromB = anchor.toB;
  }
  refineLines(before, after, fromA, before.length, fromB, after.length, deadline, changes);
  return changes;
}

function remaining(deadline: number): number {
  return Math.max(1, Math.ceil(deadline - performance.now()));
}

function refineCharacters(before: string, after: string, fromA: number, toA: number,
  fromB: number, toB: number, deadline: number, changes: Change[]): void {
  if (fromA === toA && fromB === toB) return;
  if (toA - fromA === toB - fromB && before.slice(fromA, toA) === after.slice(fromB, toB)) return;
  if (performance.now() >= deadline) {
    changes.push(new Change(fromA, toA, fromB, toB));
    return;
  }
  for (const change of diff(before.slice(fromA, toA), after.slice(fromB, toB), {
    scanLimit: 500, timeout: remaining(deadline),
  })) {
    changes.push(new Change(fromA + change.fromA, fromA + change.toA, fromB + change.fromB, fromB + change.toB));
  }
}

function refineLines(before: string, after: string, fromA: number, toA: number,
  fromB: number, toB: number, deadline: number, changes: Change[]): void {
  if (fromA === toA && fromB === toB) return;
  if (performance.now() >= deadline || fromA === toA || fromB === toB) {
    refineCharacters(before, after, fromA, toA, fromB, toB, deadline, changes);
    return;
  }
  const parts = diffLines(before.slice(fromA, toA), after.slice(fromB, toB), {
    maxEditLength: 2000, timeout: Math.min(LINE_BUDGET_MS, remaining(deadline)),
  });
  if (!parts) {
    refineCharacters(before, after, fromA, toA, fromB, toB, deadline, changes);
    return;
  }
  let endA = fromA, endB = fromB;
  for (const part of parts) {
    if (part.added) endB += part.value.length;
    else if (part.removed) endA += part.value.length;
    else {
      refineCharacters(before, after, fromA, endA, fromB, endB, deadline, changes);
      fromA = endA += part.value.length;
      fromB = endB += part.value.length;
    }
  }
  refineCharacters(before, after, fromA, endA, fromB, endB, deadline, changes);
}

export const SOURCE_DIFF_CONFIG: DiffConfig = { override: diffSourceText };
