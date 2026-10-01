import type { SourceComparisonFrame } from "../../../api/types.ts";
import { readerGap, type ReaderSlot } from "./source-reader-document.ts";

/** Display ranks are independent of source coordinates: one fold may span millions of rows. */
export function comparisonWindow(frames: readonly SourceComparisonFrame[], sourceRows: number, lineHeight: number): ReaderSlot[] {
  if (!frames.length) return [];
  const ordered = [...frames].sort((a, b) => a.span.start - b.span.start);
  const first = ordered[0]!;
  const total = first.extent.rows;
  for (const frame of ordered) {
    if (frame.view_id !== first.view_id || frame.intent_revision !== first.intent_revision
      || frame.projection_revision !== first.projection_revision || frame.extent.rows !== total) {
      throw new Error("The source pages belong to different presentations.");
    }
  }
  const slots: ReaderSlot[] = [];
  let display = 0, source = 0;
  const gap = (end: number, sourceEnd: number) => {
    if (end <= display) return;
    slots.push({ ...readerGap(source, sourceEnd, true, end - display), displayIndex: display, displayEnd: end });
    display = end; source = sourceEnd;
  };
  for (const frame of ordered) {
    gap(frame.span.start, frame.rows[0]?.index ?? source);
    for (let at = 0; at < frame.rows.length; at++) {
      const rank = frame.span.start + at;
      if (rank < display) continue;
      const row = frame.rows[at]!;
      slots.push({ ...row, displayIndex: rank, displayEnd: rank + 1 });
      display = rank + 1; source = row.end;
    }
  }
  gap(total, sourceRows);
  // Loaded rows consume their full height before omitted ranges share the remainder.
  const loaded = slots.filter(slot => !slot.pending).length;
  const omitted = total - loaded;
  const scale = omitted > 0 ? Math.max(0, Math.min(1, (2_000_000 / lineHeight - loaded) / omitted)) : 1;
  for (const slot of slots) if (slot.pending) slot.lines = (slot.lines ?? 1) * scale;
  return slots;
}

export function displayStart(slot: ReaderSlot): number { return slot.displayIndex ?? slot.index; }
export function displayEnd(slot: ReaderSlot): number { return slot.displayEnd ?? slot.end; }

/**
 * Whether two windows show the same rows. A window is rebuilt from fresh
 * objects on every load, so identity alone never answers; the fields the
 * document and its decorations read do.
 */
export function sameWindow(held: readonly ReaderSlot[], next: readonly ReaderSlot[]): boolean {
  if (!held.length || held.length !== next.length) return false;
  for (let at = 0; at < held.length; at++) {
    const a = held[at]!, b = next[at]!;
    if (a === b) continue;
    if (a.index !== b.index || a.end !== b.end || a.kind !== b.kind || !!a.pending !== !!b.pending
      || a.lines !== b.lines || a.displayIndex !== b.displayIndex || a.displayEnd !== b.displayEnd
      || a.text !== b.text || a.before_line !== b.before_line || a.after_line !== b.after_line
      || a.column !== b.column || a.changed !== b.changed || a.peer !== b.peer
      || a.contributors !== b.contributors) return false;
  }
  return true;
}
