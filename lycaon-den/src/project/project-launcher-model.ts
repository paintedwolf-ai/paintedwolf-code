import type { ProjectSummary } from "./project-summary.ts";

/**
 * The launcher renders at most this many rows; past it a count row invites a
 * narrower query. Keyboard navigation cycles the rendered rows only.
 */
export const LAUNCHER_RENDER_CAP = 50;

/**
 * Width of the tightest window in `text` containing `q` as a subsequence, or
 * null when `q` is not a subsequence at all. Tighter windows read as better
 * matches ("lyc" hits "lycaon" before "layout sync").
 */
function subsequenceSpan(text: string, q: string): number | null {
  let best: number | null = null;
  for (let start = 0; start <= text.length - q.length; start++) {
    if (text[start] !== q[0]) continue;
    let ti = start;
    let qi = 0;
    while (ti < text.length && qi < q.length) {
      if (text[ti] === q[qi]) qi++;
      ti++;
    }
    if (qi < q.length) break; // no full match from here or any later start
    const span = ti - start;
    if (best == null || span < best) best = span;
    if (best === q.length) break; // contiguous — cannot do better
  }
  return best;
}

/**
 * Filter and rank projects for the launcher. Empty query keeps the caller's
 * order (recent-first). Otherwise: name substring beats name subsequence beats
 * folder-path substring; ties keep recency order.
 */
export function filterProjects(
  summaries: ProjectSummary[],
  query: string,
): ProjectSummary[] {
  const q = query.trim().toLowerCase();
  if (!q) return summaries.slice();
  const ranked: {
    summary: ProjectSummary;
    rank: number;
    score: number;
    index: number;
  }[] = [];
  summaries.forEach((summary, index) => {
    const name = summary.displayName.toLowerCase();
    const at = name.indexOf(q);
    if (at >= 0) {
      ranked.push({ summary, rank: 0, score: at, index });
      return;
    }
    const span = subsequenceSpan(name, q);
    if (span != null) {
      ranked.push({ summary, rank: 1, score: span, index });
      return;
    }
    if (summary.folders.some((folder) => folder.toLowerCase().includes(q))) {
      ranked.push({ summary, rank: 2, score: 0, index });
    }
  });
  ranked.sort(
    (a, b) => a.rank - b.rank || a.score - b.score || a.index - b.index,
  );
  return ranked.map((r) => r.summary);
}

export function nextIndex(current: number, len: number, dir: 1 | -1): number {
  if (len <= 0) return -1;
  const base = current < 0 ? (dir > 0 ? -1 : 0) : current;
  return (base + dir + len) % len;
}
