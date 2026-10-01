import type {
  Completion,
  CompletionContext,
  CompletionResult,
  CompletionSource,
} from "@codemirror/autocomplete";
import type { Text } from "@codemirror/state";

const MIN_WORD = 3;
const MAX_CANDIDATES = 50;
const WORD_RE = /[A-Za-z_][A-Za-z0-9_]*/g;

/** Limits each completion scan to nearby lines. */
const SCAN_RADIUS = 32_768;

type WordHit = { word: string; from: number };

type Cache = {
  /** `Text` is immutable, so identity is an exact document-version check. */
  doc: Text;
  from: number;
  to: number;
  hits: WordHit[];
};

let cache: Cache | null = null;

function scanWindow(doc: Text, cursor: number): { from: number; to: number } {
  const lo = Math.max(0, Math.min(cursor, doc.length) - SCAN_RADIUS);
  const hi = Math.min(doc.length, Math.max(cursor, 0) + SCAN_RADIUS);
  // Whole-line boundaries preserve complete tokens.
  return { from: doc.lineAt(lo).from, to: doc.lineAt(hi).to };
}

function collectHits(doc: Text, cursor: number): WordHit[] {
  const { from, to } = scanWindow(doc, cursor);
  if (cache && cache.doc === doc && cache.from === from && cache.to === to) {
    return cache.hits;
  }
  const hits: WordHit[] = [];
  const text = doc.sliceString(from, to);
  WORD_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = WORD_RE.exec(text)) != null) {
    if (m[0].length >= MIN_WORD) {
      hits.push({ word: m[0], from: from + m.index });
    }
  }
  cache = { doc, from, to, hits };
  return hits;
}

export function resetBufferWordCacheForTests(): void {
  cache = null;
}

export function bufferWordSource(
  context: CompletionContext,
): CompletionResult | null {
  const word = context.matchBefore(/[A-Za-z_][A-Za-z0-9_]*/);
  if (!word || (word.from === word.to && !context.explicit)) return null;
  const typed = word.text;
  if (typed.length < 1) return null;

  const cursor = context.pos;
  const hits = collectHits(context.state.doc, cursor);
  const counts = new Map<string, number>();
  const nearest = new Map<string, number>();

  for (const hit of hits) {
    // Exclude the token currently being typed.
    if (hit.from === word.from) continue;
    if (!hit.word.toLowerCase().startsWith(typed.toLowerCase())) continue;
    if (hit.word === typed) continue;
    counts.set(hit.word, (counts.get(hit.word) ?? 0) + 1);
    const dist = Math.abs(hit.from - cursor);
    const prev = nearest.get(hit.word);
    if (prev == null || dist < prev) nearest.set(hit.word, dist);
  }

  const ranked = [...counts.keys()].sort((a, b) => {
    const da = nearest.get(a) ?? Number.MAX_SAFE_INTEGER;
    const db = nearest.get(b) ?? Number.MAX_SAFE_INTEGER;
    if (da !== db) return da - db;
    const ca = counts.get(a) ?? 0;
    const cb = counts.get(b) ?? 0;
    if (ca !== cb) return cb - ca;
    return a.localeCompare(b);
  });

  const options: Completion[] = ranked.slice(0, MAX_CANDIDATES).map((label) => ({
    label,
    type: "text",
  }));
  if (options.length === 0) return null;
  return {
    from: word.from,
    options,
    validFor: /^[A-Za-z_][A-Za-z0-9_]*$/,
  };
}

// The model result satisfies the editor completion contract.
bufferWordSource satisfies CompletionSource;
