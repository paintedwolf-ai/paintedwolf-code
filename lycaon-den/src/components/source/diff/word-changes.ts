/** A half-open range of UTF-16 offsets. */
export type TextRange = { from: number; to: number };

const WORD = /[\p{L}\p{N}\p{M}_]/u;

/** Keep word marks only on lines with an unchanged word; line fills cover the rest. */
export function wordChanges(text: string, ranges: readonly TextRange[]): TextRange[] {
  const out: TextRange[] = [];
  let next = 0;
  for (let lineFrom = 0; lineFrom <= text.length;) {
    const newline = text.indexOf("\n", lineFrom);
    const lineTo = newline < 0 ? text.length : newline;
    next = skipFinishedRanges(ranges, next, lineFrom);
    const line: TextRange[] = [];
    for (let i = next; i < ranges.length; i++) {
      const range = ranges[i];
      if (!range || range.from >= lineTo) break;
      const from = Math.max(lineFrom, range.from);
      const to = Math.min(lineTo, range.to);
      if (from < to) line.push({ from, to });
    }
    if (line.length > 0 && keepsWord(text, lineFrom, lineTo, line)) out.push(...line);
    if (newline < 0) break;
    lineFrom = newline + 1;
  }
  return out;
}

function keepsWord(text: string, from: number, to: number, ranges: readonly TextRange[]): boolean {
  let next = 0;
  for (let at = from; at < to;) {
    const point = text.codePointAt(at);
    if (point === undefined) break;
    const size = point > 0xffff ? 2 : 1;
    next = skipFinishedRanges(ranges, next, at);
    const range = ranges[next];
    const covered = range !== undefined && range.from <= at;
    if (!covered && WORD.test(String.fromCodePoint(point))) return true;
    at += size;
  }
  return false;
}

function skipFinishedRanges(ranges: readonly TextRange[], index: number, offset: number): number {
  let range = ranges[index];
  while (range && range.to <= offset) range = ranges[++index];
  return index;
}
