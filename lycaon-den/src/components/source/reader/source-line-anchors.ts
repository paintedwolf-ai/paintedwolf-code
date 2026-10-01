type LineOccurrence = { from: number; to: number; count: number };
export type SourceLineAnchor = { fromA: number; toA: number; fromB: number; toB: number };

function occurrences(text: string): Map<string, LineOccurrence> {
  const lines = new Map<string, LineOccurrence>();
  for (let from = 0; from < text.length;) {
    const newline = text.indexOf("\n", from);
    const to = newline < 0 ? text.length : newline + 1;
    const line = text.slice(from, to), held = lines.get(line);
    if (held) held.count++;
    else lines.set(line, { from, to, count: 1 });
    from = to;
  }
  return lines;
}

/** Patience anchors preserve unique unchanged lines in O(n log n) work. */
export function sourceLineAnchors(before: string, after: string): SourceLineAnchor[] {
  const a = occurrences(before), b = occurrences(after);
  const candidates: SourceLineAnchor[] = [];
  for (const [line, old] of a) {
    const next = b.get(line);
    if (old.count === 1 && next?.count === 1) {
      candidates.push({ fromA: old.from, toA: old.to, fromB: next.from, toB: next.to });
    }
  }
  const tails: number[] = [], previous: number[] = [];
  for (let i = 0; i < candidates.length; i++) {
    const position = candidates[i]!.fromB;
    let low = 0, high = tails.length;
    while (low < high) {
      const middle = (low + high) >>> 1;
      if (candidates[tails[middle]!]!.fromB < position) low = middle + 1;
      else high = middle;
    }
    previous[i] = low ? tails[low - 1]! : -1;
    tails[low] = i;
  }
  const matched: SourceLineAnchor[] = [];
  for (let i = tails.at(-1) ?? -1; i >= 0; i = previous[i]!) matched.push(candidates[i]!);
  const anchors: SourceLineAnchor[] = [];
  for (const next of matched.reverse()) {
    const held = anchors.at(-1);
    if (held && held.toA === next.fromA && held.toB === next.fromB) {
      held.toA = next.toA;
      held.toB = next.toB;
    } else anchors.push({ ...next });
  }
  return anchors;
}
