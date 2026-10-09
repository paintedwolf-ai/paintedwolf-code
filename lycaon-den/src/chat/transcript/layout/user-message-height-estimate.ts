/** The user attachment rail wraps into at most three chip rows. */
export function attachmentRailHeight(labels: readonly string[], widthPx: number, remPx: number, bodyPx: number): number {
  if (!labels.length) return 0;
  const fontPx = remPx * 12 / 14;
  const gap = 4;
  const chipHeight = Math.max(22, bodyPx * 1.65);
  let rows = 1;
  let used = 0;
  for (const label of labels) {
    const width = Math.min(widthPx, 18 * fontPx, label.length * fontPx * 0.5 + 32);
    if (used && used + gap + width > widthPx) { rows++; used = 0; }
    used += (used ? gap : 0) + width;
  }
  const visibleRows = Math.min(3, rows);
  return visibleRows * chipHeight + (visibleRows - 1) * gap + 6;
}
