import { SearchCursor } from "@codemirror/search";
import type { EditorState, Text } from "@codemirror/state";

const OCCURRENCE_MIN_WORD_LENGTH = 2;
const OCCURRENCE_MAX_TARGET_LENGTH = 200;
export const OCCURRENCE_MAX_MARKS = 500;

const WORD_CHAR = /[A-Za-z0-9_$]/;

export type OccurrenceHit = { from: number; to: number };

type OccurrenceTarget = {
  text: string;
  from: number;
  to: number;
  wholeWord: boolean;
};

function caretTargetAt(state: EditorState): OccurrenceTarget | null {
  const sel = state.selection.main;
  if (!sel.empty) return null;
  const range = state.wordAt(sel.head);
  if (!range) return null;
  const text = state.doc.sliceString(range.from, range.to);
  if (
    text.length < OCCURRENCE_MIN_WORD_LENGTH ||
    text.length > OCCURRENCE_MAX_TARGET_LENGTH
  ) {
    return null;
  }
  if (!WORD_CHAR.test(text[0]!)) return null;
  return { text, from: range.from, to: range.to, wholeWord: true };
}

/** Returns selected text or the caret word. */
export function occurrenceTargetAt(state: EditorState): OccurrenceTarget | null {
  if (state.selection.ranges.length !== 1) return null;
  const sel = state.selection.main;
  if (sel.empty) return caretTargetAt(state);
  const length = sel.to - sel.from;
  if (length > OCCURRENCE_MAX_TARGET_LENGTH) return null;
  const text = state.doc.sliceString(sel.from, sel.to);
  return text ? { text, from: sel.from, to: sel.to, wholeWord: false } : null;
}

export function occurrenceTargetKey(target: OccurrenceTarget | null): string {
  return target
    ? JSON.stringify([target.wholeWord, target.from, target.to, target.text])
    : "";
}

function isWholeWord(doc: Text, hit: number, textLength: number): boolean {
  const before = hit > 0 ? doc.sliceString(hit - 1, hit) : "";
  const end = hit + textLength;
  const after = end < doc.length ? doc.sliceString(end, end + 1) : "";
  return (
    (!before || !WORD_CHAR.test(before)) && (!after || !WORD_CHAR.test(after))
  );
}

export function collectOccurrenceHits(
  doc: Text,
  target: OccurrenceTarget,
  options: { from?: number; to?: number; cap?: number } = {},
): OccurrenceHit[] {
  const from = options.from ?? 0;
  const to = options.to ?? doc.length;
  const cap = options.cap ?? OCCURRENCE_MAX_MARKS;
  const hits: OccurrenceHit[] = [];
  const cursor = new SearchCursor(doc, target.text, from, to);
  while (hits.length < cap) {
    const next = cursor.next();
    if (next.done) break;
    const { from, to } = next.value;
    if (from < target.to && to > target.from) continue;
    if (target.wholeWord && !isWholeWord(doc, from, target.text.length)) {
      continue;
    }
    hits.push({ from, to });
  }
  return hits;
}

export function collectOccurrenceLines(
  doc: Text,
  target: OccurrenceTarget,
  cap = OCCURRENCE_MAX_MARKS,
): number[] {
  const lines: number[] = [];
  const seen = new Set<number>();
  for (const hit of collectOccurrenceHits(doc, target, { cap })) {
    const line = doc.lineAt(hit.from).number;
    if (seen.has(line)) continue;
    seen.add(line);
    lines.push(line);
  }
  return lines;
}
