import type {
  FindingLevel,
  SecurityFinding,
  SecurityFindingLocation,
  SourceAttributionInterval,
} from "../../../api/types.ts";

export type AttributionContributor = {
  sessionId: string;
  turn: number;
  toolCallId: string;
  ts: string;
};

export type AttributionMark = { startLine: number; endLine: number; contributors: AttributionContributor[] };

export type FindingLineMark = {
  line: number;
  level: FindingLevel;
  findings: SecurityFinding[];
};

const LEVEL_RANK: Record<FindingLevel, number> = {
  critical: 5,
  high: 4,
  medium: 3,
  low: 2,
  info: 1,
  unknown: 0,
};

function attributionKey(iv: {
  sessionId: string;
  turn: number;
  toolCallId: string;
}): string {
  return `${iv.sessionId}\0${iv.turn}\0${iv.toolCallId}`;
}

/** Partition overlapping line spans without choosing one chat over another. */
export function mergeAttributionIntervals(intervals: readonly SourceAttributionInterval[]): AttributionMark[] {
  const events = new Map<number, Array<{ author: AttributionContributor; delta: number }>>();
  for (const iv of intervals) {
    if (iv.start_line < 1 || iv.end_line < iv.start_line) continue;
    const author = { sessionId: (iv.session_id ?? "").trim(), turn: iv.turn,
      toolCallId: (iv.tool_call_id ?? "").trim(), ts: iv.recorded_at };
    for (const [line, delta] of [[iv.start_line, 1], [iv.end_line + 1, -1]] as const) {
      events.set(line, [...(events.get(line) ?? []), { author, delta }]);
    }
  }
  const out: AttributionMark[] = [];
  const active = new Map<string, { author: AttributionContributor; count: number }>();
  const lines = [...events.keys()].sort((a, b) => a - b);
  for (let i = 0; i < lines.length - 1; i++) {
    const line = lines[i]!;
    for (const event of events.get(line) ?? []) {
      const key = attributionKey(event.author);
      const count = (active.get(key)?.count ?? 0) + event.delta;
      if (count === 0) active.delete(key);
      else active.set(key, { author: event.author, count });
    }
    const contributors = [...active.values()].map((value) => value.author)
      .sort((a, b) => attributionKey(a).localeCompare(attributionKey(b)));
    if (!contributors.length) continue;
    const endLine = lines[i + 1]! - 1, prior = out[out.length - 1];
    if (prior && prior.endLine + 1 === line && sameAttributionContributors(prior.contributors, contributors)) prior.endLine = endLine;
    else out.push({ startLine: line, endLine, contributors });
  }
  return out;
}

export function sameAttributionContributors(a: readonly AttributionContributor[], b: readonly AttributionContributor[]): boolean {
  return a.length === b.length && a.every((author, i) => attributionKey(author) === attributionKey(b[i]!) && author.ts === b[i]!.ts);
}

/** Find the provenance interval covering a line. */
export function attributionForLine(
  marks: readonly AttributionMark[],
  line: number,
): AttributionMark | null {
  let lo = 0;
  let hi = marks.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const mark = marks[mid]!;
    if (line < mark.startLine) hi = mid - 1;
    else if (line > mark.endLine) lo = mid + 1;
    else return mark;
  }
  return null;
}

function highestFindingLevel(
  findings: readonly SecurityFinding[],
): FindingLevel | null {
  let best: FindingLevel | null = null;
  let rank = -1;
  for (const f of findings) {
    const r = LEVEL_RANK[f.level];
    if (r > rank) {
      rank = r;
      best = f.level;
    }
  }
  return best;
}

/** One finding paired with the location of it that lands in the open buffer. */
export type FindingLocationHit = {
  finding: SecurityFinding;
  location: SecurityFindingLocation;
  line: number;
};

/** Findings use the host's normalized root-relative paths. */
export function findingLocationsIn(
  path: string,
  findings: readonly SecurityFinding[],
): FindingLocationHit[] {
  const norm = path.replace(/\\/g, "/").replace(/^\.\//, "");
  const hits: FindingLocationHit[] = [];
  for (const finding of findings) {
    for (const location of finding.locations ?? []) {
      if (!location.uri || location.uri !== norm) continue;
      const line = location.start_line ?? 1;
      if (line < 1) continue;
      hits.push({ finding, location, line });
    }
  }
  return hits;
}

/** Group findings that touch `path` by start line; highest severity wins glyph. */
export function findingsByLine(
  path: string,
  findings: readonly SecurityFinding[],
): FindingLineMark[] {
  const byLine = new Map<number, SecurityFinding[]>();
  for (const { finding, line } of findingLocationsIn(path, findings)) {
    const list = byLine.get(line) ?? [];
    list.push(finding);
    byLine.set(line, list);
  }
  const marks: FindingLineMark[] = [];
  for (const [line, list] of byLine) {
    const level = highestFindingLevel(list);
    if (!level) continue;
    marks.push({ line, level, findings: list });
  }
  marks.sort((a, b) => a.line - b.line);
  return marks;
}

export function findingGlyph(level: FindingLevel): string {
  switch (level) {
    case "critical":
      return "◆";
    case "high":
      return "▲";
    case "medium":
      return "●";
    case "low":
      return "○";
    case "info":
      return "◌";
    case "unknown":
      return "?";
  }
}

export function findingLevelLabel(level: FindingLevel): string {
  switch (level) {
    case "critical":
      return "Critical";
    case "high":
      return "High";
    case "medium":
      return "Medium";
    case "low":
      return "Low";
    case "info":
      return "Info";
    case "unknown":
      return "Unknown";
  }
}
