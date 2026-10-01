import { escapeRegExp } from "../utils/escape-regexp.ts";
/** Highlight terms come from the host’s fts_terms response. */
export type HighlightSegment = { text: string; match: boolean };


export function highlightSegments(
  text: string,
  terms: readonly string[] | undefined,
  options?: { caseSensitive?: boolean },
): HighlightSegment[] {
  const plain: HighlightSegment[] = [{ text, match: false }];
  if (!text) return plain;
  const cleaned = [
    ...new Set(
      (terms ?? [])
        .map((t) => t.trim())
        .filter((t) => t.length >= 2)
        .map(escapeRegExp),
    ),
  ];
  if (cleaned.length === 0) return plain;
  // Longest first so overlapping terms ("store", "stores") mark the full run.
  cleaned.sort((a, b) => b.length - a.length);
  const re = new RegExp(cleaned.join("|"), options?.caseSensitive ? "g" : "gi");
  const segments: HighlightSegment[] = [];
  let last = 0;
  for (const m of text.matchAll(re)) {
    const start = m.index;
    if (start > last) segments.push({ text: text.slice(last, start), match: false });
    segments.push({ text: m[0], match: true });
    last = start + m[0].length;
  }
  if (segments.length === 0) return plain;
  if (last < text.length) segments.push({ text: text.slice(last), match: false });
  return segments;
}

/** Segments marking the UTF-16 indexes the host says matched. */
export function segmentsAtIndexes(text: string, indexes: readonly number[]): HighlightSegment[] {
  if (!text || indexes.length === 0) return [{ text, match: false }];
  const marked = new Set(indexes);
  const segments: HighlightSegment[] = [];
  for (let i = 0; i < text.length; i++) {
    const match = marked.has(i);
    const last = segments[segments.length - 1];
    if (last && last.match === match) last.text += text[i];
    else segments.push({ text: text[i]!, match });
  }
  return segments;
}

/** A hit title's highlights: the host's own match positions when it sent
 * them, otherwise the query terms found in the title. */
export function hitTitleSegments(
  title: string,
  titleMatches: readonly number[] | undefined,
  terms: readonly string[] | undefined,
  options?: { caseSensitive?: boolean },
): HighlightSegment[] {
  return titleMatches?.length ? segmentsAtIndexes(title, titleMatches) : highlightSegments(title, terms, options);
}
