/** Where a dragged pinned row lands, from the rows' shared pitch. */
export function pinnedDropIndex(from: number, dy: number, pitch: number, count: number): number {
  if (pitch <= 0 || count <= 0) return from;
  return Math.max(0, Math.min(count - 1, from + Math.round(dy / pitch)));
}

/** How far a row that is not being dragged steps aside for the dragged one. */
export function pinnedRowShift(index: number, from: number, to: number, pitch: number): number {
  if (from < index && index <= to) return -pitch;
  if (to <= index && index < from) return pitch;
  return 0;
}

/** The dragged row follows the pointer but stops a little past either end. */
export function pinnedDragOffset(from: number, dy: number, pitch: number, count: number): number {
  const overshoot = pitch / 4;
  const min = -from * pitch - overshoot;
  const max = (count - 1 - from) * pitch + overshoot;
  return Math.max(min, Math.min(max, dy));
}
