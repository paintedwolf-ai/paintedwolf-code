import { PrettyBudgetTracker } from "./pretty-limits.ts";
import { displayToSourceOffset, sourceToDisplayOffset } from "./pretty-map.ts";
import { printJsonSegment } from "./pretty-printer.ts";
import type {
  PrettyAmbiguousMultiCursorConflict, PrettyDisplayChange, PrettyEditMapInput, PrettyEditMapResult,
  PrettyMappedSourceChange, PrettyOriginRun, PrettyRawRange, PrettyRetainedEditIntention, PrettySelectionRange, PrettySourceMap,
} from "./pretty-types.ts";

function normRange(r: PrettySelectionRange): { from: number; to: number } {
  return r.anchor <= r.head ? { from: r.anchor, to: r.head } : { from: r.head, to: r.anchor };
}

function findOverlappingRuns(
  map: PrettySourceMap,
  dispFrom: number,
  dispTo: number,
): { realRuns: PrettyOriginRun[]; virtualRuns: PrettyOriginRun[] } {
  const realRuns: PrettyOriginRun[] = [];
  const virtualRuns: PrettyOriginRun[] = [];
  for (const run of map.runs) {
    if (run.dispFrom >= dispTo && dispFrom < dispTo) break;
    const overlaps = dispFrom === dispTo
      ? run.dispFrom <= dispFrom && dispFrom <= run.dispTo
      : run.dispTo > dispFrom && run.dispFrom < dispTo;
    if (!overlaps) continue;
    if (run.kind === "virtual-layout") virtualRuns.push(run);
    else realRuns.push(run);
  }
  return { realRuns, virtualRuns };
}

function isStrictlyInsideStringToken(map: PrettySourceMap, rawText: string, dispOffset: number): boolean {
  for (const run of map.runs) {
    if (run.dispFrom >= dispOffset) break;
    if (run.kind === "identity" && run.dispFrom < dispOffset && dispOffset < run.dispTo && rawText.charCodeAt(run.srcFrom) === 0x22) {
      return true;
    }
  }
  return false;
}

function applyChangesToString(rawText: string, changes: readonly PrettyMappedSourceChange[]): string {
  if (changes.length === 0) return rawText;
  const sorted = [...changes].sort((a, b) => a.from - b.from || a.to - b.to);
  const parts: string[] = [];
  let cursor = 0;
  for (const ch of sorted) {
    if (ch.from > cursor) parts.push(rawText.slice(cursor, ch.from));
    parts.push(ch.insert);
    cursor = Math.max(cursor, ch.to);
  }
  if (cursor < rawText.length) parts.push(rawText.slice(cursor));
  return parts.join("");
}

function isValidJsonSlice(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return false;
  const budget = new PrettyBudgetTracker(trimmed);
  if (!budget.checkInput()) return false;
  return printJsonSegment(trimmed, 0, trimmed.length, 0, 0, "  ", budget).status === "formatted";
}

/** Extracts both the visible display selection text and the corresponding enclosing raw source text. */
export function extractCopySelection(
  map: PrettySourceMap,
  rawText: string,
  displayText: string,
  ranges: readonly PrettySelectionRange[],
): { readonly displayText: string; readonly rawText: string } {
  const dispParts: string[] = [];
  const rawParts: string[] = [];
  for (const r of ranges) {
    const { from, to } = normRange(r);
    dispParts.push(displayText.slice(from, to));
    const { realRuns } = findOverlappingRuns(map, from, to);
    if (realRuns.length === 0) {
      rawParts.push("");
    } else {
      const first = realRuns[0]!;
      const last = realRuns[realRuns.length - 1]!;
      const srcFrom = first.srcFrom + Math.max(0, from - first.dispFrom);
      const srcTo = last.srcFrom + Math.min(last.srcTo - last.srcFrom, to - last.dispFrom);
      rawParts.push(rawText.slice(srcFrom, Math.max(srcFrom, srcTo)));
    }
  }
  return { displayText: dispParts.join("\n"), rawText: rawParts.join("\n") };
}

export function extractRawSelection(
  map: PrettySourceMap,
  rawText: string,
  ranges: readonly PrettySelectionRange[],
): string {
  return extractCopySelection(map, rawText, "", ranges).rawText;
}

/** Resolves a display-coordinate edit transaction into revision-bound raw source coordinates. */
export function mapDisplayEditToSource(
  input: PrettyEditMapInput,
): PrettyEditMapResult {
  const { map, rawText, displayText, intent, composing = false } = input;
  const affectedSegmentSet = new Set<number>();
  let requiresRawFallback = composing;
  let conflictRequiresRawFallback = false;

  let effectiveChanges: readonly PrettyDisplayChange[] = input.changes;
  if (
    effectiveChanges.length === 0 &&
    (intent === "delete.backward" || intent === "delete.forward") &&
    input.selection &&
    input.selection.length === 1 &&
    input.selection[0]!.anchor === input.selection[0]!.head
  ) {
    const caret = input.selection[0]!.head;
    const from = intent === "delete.backward" ? Math.max(0, caret - 1) : caret;
    const to = intent === "delete.forward" ? Math.min(map.displayLength, caret + 1) : caret;
    if (from < to) effectiveChanges = [{ from, to, insert: "" }];
  }

  if (effectiveChanges.length === 1) {
    const single = effectiveChanges[0]!;
    if (single.from < single.to && single.insert === "") {
      const { realRuns, virtualRuns } = findOverlappingRuns(map, single.from, single.to);
      if (realRuns.length === 0 && virtualRuns.length > 0) {
        const vFirst = virtualRuns[0]!;
        const vLast = virtualRuns[virtualRuns.length - 1]!;
        for (const v of virtualRuns) affectedSegmentSet.add(v.segmentIndex);
        const isSingleStepDelete =
          single.to - single.from === 1 &&
          (intent === "delete.backward" || intent === "delete.forward");
        const goLeft = intent === "delete.backward";
        const rawPos = goLeft ? vFirst.srcFrom : vLast.srcTo;
        const dispPos = goLeft ? vFirst.dispFrom : vLast.dispTo;
        const copiedDisp = displayText.slice(single.from, single.to);
        return {
          changes: [],
          rawSelection: [{ anchor: rawPos, head: rawPos }],
          displaySelection: [{ anchor: dispPos, head: dispPos }],
          ...(isSingleStepDelete ? { caretAffinity: goLeft ? "before" : "after" } : {}),
          movedToRawBoundary: isSingleStepDelete,
          requiresRawFallback,
          conflictRequiresRawFallback: false,
          affectedSegments: [...affectedSegmentSet],
          affectedRawRanges: [],
          copiedDisplayText: copiedDisp,
          copiedRawText: "",
        };
      }
    }
  }

  interface CandidateChange {
    order: number;
    from: number;
    to: number;
    insert: string;
    dispFrom: number;
    dispTo: number;
  }

  const candidates: CandidateChange[] = [];
  let deletedRecordDelimiter = false;

  for (let idx = 0; idx < effectiveChanges.length; idx++) {
    const ch = effectiveChanges[idx]!;
    const { realRuns, virtualRuns } = findOverlappingRuns(map, ch.from, ch.to);
    for (const r of realRuns) affectedSegmentSet.add(r.segmentIndex);
    for (const v of virtualRuns) affectedSegmentSet.add(v.segmentIndex);

    if (ch.from === ch.to) {
      const insertPos = displayToSourceOffset(map, ch.from, "insert");
      candidates.push({ order: idx, from: insertPos, to: insertPos, insert: ch.insert, dispFrom: ch.from, dispTo: ch.to });
      const isWhitespaceOnly = ch.insert.length > 0 && /^[ \t\r\n]+$/.test(ch.insert);
      const isLayoutIntent = intent === "enter" || intent === "tab" || intent === "indent";
      if ((isWhitespaceOnly || isLayoutIntent) && !isStrictlyInsideStringToken(map, rawText, ch.from)) {
        requiresRawFallback = true;
      }
    } else if (realRuns.length === 0 && virtualRuns.length > 0) {
      const vLast = virtualRuns[virtualRuns.length - 1]!;
      if (ch.insert.length > 0) {
        candidates.push({
          order: idx,
          from: vLast.insertOffset,
          to: vLast.insertOffset,
          insert: ch.insert,
          dispFrom: ch.from,
          dispTo: ch.to,
        });
        if (/^[ \t\r\n]+$/.test(ch.insert)) requiresRawFallback = true;
      }
    } else if (realRuns.length > 0) {
      const first = realRuns[0]!;
      const last = realRuns[realRuns.length - 1]!;
      for (const r of realRuns) {
        if (r.kind === "record-delimiter") deletedRecordDelimiter = true;
      }
      const srcFrom = first.srcFrom + Math.max(0, ch.from - first.dispFrom);
      const srcTo = last.srcFrom + Math.min(last.srcTo - last.srcFrom, ch.to - last.dispFrom);
      candidates.push({
        order: idx,
        from: srcFrom,
        to: Math.max(srcFrom, srcTo),
        insert: ch.insert,
        dispFrom: ch.from,
        dispTo: ch.to,
      });
    }
  }

  candidates.sort((a, b) => a.from - b.from || a.to - b.to || a.dispFrom - b.dispFrom);
  const coalesced: PrettyMappedSourceChange[] = [];
  let conflictReason: "same-raw-offset-distinct-insert" | "overlapping-raw-ranges" = "same-raw-offset-distinct-insert";

  for (const cand of candidates) {
    const prev = coalesced[coalesced.length - 1];
    if (!prev) {
      coalesced.push({ from: cand.from, to: cand.to, insert: cand.insert });
      continue;
    }
    if (prev.from === prev.to && cand.from === cand.to && prev.from === cand.from) {
      if (prev.insert === cand.insert) {
        continue;
      }
      conflictReason = "same-raw-offset-distinct-insert";
      conflictRequiresRawFallback = true;
      requiresRawFallback = true;
      continue;
    }
    if (cand.from < prev.to || (cand.from === prev.to && (prev.from < prev.to || cand.from < cand.to))) {
      conflictReason = "overlapping-raw-ranges";
      conflictRequiresRawFallback = true;
      requiresRawFallback = true;
      continue;
    }
    coalesced.push({ from: cand.from, to: cand.to, insert: cand.insert });
  }

  if (conflictRequiresRawFallback) {
    const intentions: PrettyRetainedEditIntention[] = [...candidates]
      .sort((a, b) => a.order - b.order)
      .map((c) => ({
        order: c.order,
        dispFrom: c.dispFrom,
        dispTo: c.dispTo,
        rawFrom: c.from,
        rawTo: c.to,
        insert: c.insert,
      }));
    const conflict: PrettyAmbiguousMultiCursorConflict = {
      sourceRevision: map.sourceRevision,
      reason: conflictReason,
      intentions,
    };
    const seenAnchors = new Set<string>();
    const rawSelection: PrettySelectionRange[] = [];
    const displaySelection: PrettySelectionRange[] = [];
    const affectedRawRanges: PrettyRawRange[] = [];
    for (const c of candidates) {
      const key = `${c.from}:${c.to}`;
      if (!seenAnchors.has(key)) {
        seenAnchors.add(key);
        rawSelection.push({ anchor: c.from, head: c.to });
        const dispAnchor = sourceToDisplayOffset(map, c.from, "right");
        const dispHead = sourceToDisplayOffset(map, c.to, "right");
        displaySelection.push({ anchor: dispAnchor, head: dispHead });
        affectedRawRanges.push({ srcFrom: c.from, srcTo: c.to });
      }
    }
    return {
      changes: [],
      rawSelection,
      displaySelection,
      movedToRawBoundary: false,
      requiresRawFallback: true,
      conflictRequiresRawFallback: true,
      conflict,
      affectedSegments: [...affectedSegmentSet],
      affectedRawRanges,
    };
  }

  if (deletedRecordDelimiter) {
    requiresRawFallback = true;
  }

  const nextRawText = applyChangesToString(rawText, coalesced);
  const affectedRawRanges: PrettyRawRange[] = [];

  if (!requiresRawFallback && coalesced.length > 0) {
    if (input.segments && input.segments.length > 0) {
      let delta = 0;
      for (const ch of coalesced) delta += ch.insert.length - (ch.to - ch.from);
      for (const segIdx of affectedSegmentSet) {
        const seg = input.segments[segIdx];
        if (seg && seg.formatted) {
          const nextSegEnd = Math.max(seg.srcFrom, Math.min(nextRawText.length, seg.srcTo + delta));
          if (!isValidJsonSlice(nextRawText.slice(seg.srcFrom, nextSegEnd))) {
            requiresRawFallback = true;
          }
        }
      }
    } else if (map.runs.some((r) => r.kind === "virtual-layout")) {
      if (!isValidJsonSlice(nextRawText)) {
        requiresRawFallback = true;
      }
    }
  }

  if (requiresRawFallback) {
    for (const ch of coalesced) {
      affectedRawRanges.push({ srcFrom: ch.from, srcTo: ch.from + ch.insert.length });
    }
  }

  const rawSelection: PrettySelectionRange[] = [];
  const displaySelection: PrettySelectionRange[] = [];
  let offsetDelta = 0;
  for (const ch of coalesced) {
    const newHead = ch.from + offsetDelta + ch.insert.length;
    rawSelection.push({ anchor: newHead, head: newHead });
    const dispHead = sourceToDisplayOffset(map, ch.to, "right");
    displaySelection.push({ anchor: dispHead, head: dispHead });
    offsetDelta += ch.insert.length - (ch.to - ch.from);
  }
  if (rawSelection.length === 0 && input.selection) {
    for (const sel of input.selection) {
      const anchor = displayToSourceOffset(map, sel.anchor, "insert");
      const head = displayToSourceOffset(map, sel.head, "insert");
      rawSelection.push({ anchor, head });
      displaySelection.push(sel);
    }
  }

  const copied = effectiveChanges.length > 0 && (intent === "cut" || effectiveChanges.some((c) => c.from < c.to))
    ? extractCopySelection(map, rawText, displayText, effectiveChanges.map((c) => ({ anchor: c.from, head: c.to })))
    : undefined;

  return {
    changes: coalesced,
    rawSelection,
    displaySelection,
    movedToRawBoundary: false,
    requiresRawFallback,
    conflictRequiresRawFallback,
    affectedSegments: [...affectedSegmentSet],
    affectedRawRanges,
    ...(copied ? { copiedDisplayText: copied.displayText, copiedRawText: copied.rawText } : {}),
  };
}
