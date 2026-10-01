import { getSearchQuery } from "@codemirror/search";
import type { EditorState, Text } from "@codemirror/state";
import {
  collectOccurrenceLines,
  OCCURRENCE_MAX_MARKS,
  occurrenceTargetAt,
  occurrenceTargetKey,
} from "../editor/occurrence-scan.ts";

const OVERVIEW_FIND_CAP = 2000;
const OVERVIEW_SYMBOL_HEIGHT = 1;
const OVERVIEW_DIFF_HEIGHT = 2;
const OVERVIEW_MATCH_HEIGHT = 2;
const OVERVIEW_CURRENT_HEIGHT = 3;
const OVERVIEW_AGENT_PENDING_HEIGHT = 4;
const OVERVIEW_SYMBOL_COALESCE_DISTANCE = 1;
export const OVERVIEW_WIDTH_PX = 8;

/** `add` and `del` are the shown comparison, in the gutter's two colours. */
export type OverviewKind =
  | "symbol"
  | "add"
  | "del"
  | "match"
  | "current"
  | "window"
  /** Lines an agent read or matched. */
  | "agent"
  /** Lines an agent is about to change. */
  | "agent-pending";

const OVERVIEW_KIND_PRIORITY: Record<OverviewKind, number> = {
  symbol: 1,
  add: 2,
  del: 3,
  match: 5,
  agent: 6,
  "agent-pending": 7,
  current: 8,
  window: 9,
};

const OVERVIEW_KIND_HEIGHT: Record<OverviewKind, number> = {
  symbol: OVERVIEW_SYMBOL_HEIGHT,
  add: OVERVIEW_DIFF_HEIGHT,
  del: OVERVIEW_DIFF_HEIGHT,
  match: OVERVIEW_MATCH_HEIGHT,
  current: OVERVIEW_CURRENT_HEIGHT,
  window: OVERVIEW_CURRENT_HEIGHT,
  agent: OVERVIEW_MATCH_HEIGHT,
  "agent-pending": OVERVIEW_AGENT_PENDING_HEIGHT,
};

/** Which tick families the ruler collects and paints. */
export type OverviewTickKinds = {
  symbols: boolean;
  changes: boolean;
  matches: boolean;
  cursor: boolean;
  windows: boolean;
  agents: boolean;
};

export const ALL_OVERVIEW_TICK_KINDS: OverviewTickKinds = {
  symbols: true,
  changes: true,
  matches: true,
  cursor: true,
  windows: true,
  agents: true,
};

export type OverviewTick = {
  kind: OverviewKind;
  fromLine: number;
  toLine: number;
  window?: { clientId: string; color?: string };
  /** Agent ticks paint in their chat's color. */
  color?: string;
};

export type OverviewPaintOp = {
  kind: OverviewKind;
  y: number;
  h: number;
  window?: { clientId: string; color?: string };
  color?: string;
};

export type OverviewSlices = {
  current: OverviewTick[];
  matches: OverviewTick[];
  symbols: OverviewTick[];
  diff: OverviewTick[];
  windows: OverviewTick[];
  agents: OverviewTick[];
};

export function emptyOverviewSlices(): OverviewSlices {
  return { current: [], matches: [], symbols: [], diff: [], windows: [], agents: [] };
}

export function mergeOverviewTicks(slices: OverviewSlices): OverviewTick[] {
  return [
    ...slices.symbols,
    ...slices.diff,
    ...slices.matches,
    ...slices.agents,
    ...slices.current,
    ...slices.windows,
  ];
}

/** A comparison chunk in the merge view's own coordinates. */
type OverviewDiffChunk = {
  fromA: number;
  toA: number;
  fromB: number;
  toB: number;
};

/** Changed ranges and deletion points match the line gutter. */
export function collectDiffTicks(
  chunks: readonly OverviewDiffChunk[] | null,
  doc: Text,
): OverviewTick[] {
  if (!chunks || chunks.length === 0 || doc.length === 0) return [];
  const ticks: OverviewTick[] = [];
  for (const chunk of chunks) {
    // `fromB` / `toB` may sit outside the document when the chunk ends the file.
    const head = doc.lineAt(Math.min(doc.length, chunk.fromB)).number;
    if (chunk.fromB < chunk.toB) {
      const last = doc.lineAt(Math.min(doc.length, chunk.toB - 1)).number;
      ticks.push({ kind: "add", fromLine: head, toLine: last });
    }
    if (chunk.fromA < chunk.toA) {
      ticks.push({ kind: "del", fromLine: head, toLine: head });
    }
  }
  return ticks;
}

export function collectSymbolTicks(
  symbols: readonly { line: number }[],
): OverviewTick[] {
  const ticks: OverviewTick[] = [];
  const seen = new Set<number>();
  for (const symbol of symbols) {
    if (symbol.line < 1 || seen.has(symbol.line)) continue;
    seen.add(symbol.line);
    ticks.push({ kind: "symbol", fromLine: symbol.line, toLine: symbol.line });
  }
  return ticks;
}

export function overviewSearchKey(state: EditorState): string {
  const query = getSearchQuery(state);
  if (!query.valid || query.search === "") return "";
  return `${query.search}\0${Number(query.caseSensitive)}\0${Number(query.wholeWord)}\0${Number(query.regexp)}`;
}

export function overviewMatchKey(state: EditorState): string {
  const searchKey = overviewSearchKey(state);
  if (searchKey !== "") {
    const selection = state.selection.main;
    const active = selection.empty ? "" : `${selection.from}:${selection.to}`;
    return `find:${searchKey}:${active}`;
  }
  const target = occurrenceTargetAt(state);
  return target
    ? `occurrence:${occurrenceTargetKey(target)}`
    : "";
}

export function collectCurrentTicks(state: EditorState): OverviewTick[] {
  const selection = state.selection.main;
  const doc = state.doc;
  if (selection.empty) {
    const line = doc.lineAt(selection.head).number;
    return [{ kind: "current", fromLine: line, toLine: line }];
  }
  return [
    {
      kind: "current",
      fromLine: doc.lineAt(selection.from).number,
      toLine: doc.lineAt(selection.to - 1).number,
    },
  ];
}

export function collectOccurrenceMatchTicks(state: EditorState): OverviewTick[] {
  const target = occurrenceTargetAt(state);
  if (!target) return [];
  return collectOccurrenceLines(state.doc, target, OCCURRENCE_MAX_MARKS).map(
    (line) => ({ kind: "match" as const, fromLine: line, toLine: line }),
  );
}

export function collectFindMatchTicks(state: EditorState): OverviewTick[] {
  const query = getSearchQuery(state);
  if (!query.valid || query.search === "") return [];
  const selection = state.selection.main;
  const lines = new Set<number>();
  const cursor = query.getCursor(state);
  let count = 0;
  for (;;) {
    const next = cursor.next();
    if (next.done) break;
    const { from, to } = next.value;
    count += 1;
    const isCurrent =
      !selection.empty && from === selection.from && to === selection.to;
    if (!isCurrent) lines.add(state.doc.lineAt(from).number);
    if (count >= OVERVIEW_FIND_CAP) break;
  }
  return [...lines].map((line) => ({
    kind: "match",
    fromLine: line,
    toLine: line,
  }));
}

/** Find results replace passive occurrences. */
export function collectMatchTicks(state: EditorState): OverviewTick[] {
  return overviewSearchKey(state) !== ""
    ? collectFindMatchTicks(state)
    : collectOccurrenceMatchTicks(state);
}

export function overviewHasOverflow(
  scrollHeight: number,
  clientHeight: number,
): boolean {
  return clientHeight > 0 && scrollHeight > clientHeight + 1;
}

/** Track and handle lengths and the scroll range they represent. */
export type OverviewTrackGeometry = {
  trackLength: number;
  handleLength: number;
  scrollHeight: number;
  clientHeight: number;
};

/** Derives handle length from the viewport ratio and theme minimum. */
export function overviewHandleLength(args: {
  trackLength: number;
  scrollHeight: number;
  clientHeight: number;
  minHandleLength: number;
}): number {
  const { trackLength, scrollHeight, clientHeight, minHandleLength } = args;
  if (trackLength <= 0) return 0;
  const share =
    scrollHeight > 0 ? (clientHeight / scrollHeight) * trackLength : trackLength;
  return Math.min(trackLength, Math.max(minHandleLength, share));
}

/** Maps content offsets to thumb positions, spreading the final viewport across the handle. */
export function trackYForOffset(
  offset: number,
  geometry: OverviewTrackGeometry,
): number {
  const { trackLength, handleLength, scrollHeight, clientHeight } = geometry;
  if (trackLength <= 0 || scrollHeight <= 0) return 0;
  const clamped = Math.min(Math.max(offset, 0), scrollHeight);
  const scrollRange = scrollHeight - clientHeight;
  const travel = trackLength - handleLength;
  if (scrollRange <= 0 || travel <= 0 || clientHeight <= 0) {
    return (clamped / scrollHeight) * trackLength;
  }
  if (clamped <= scrollRange) return (clamped / scrollRange) * travel;
  return travel + ((clamped - scrollRange) / clientHeight) * handleLength;
}

/** A line's extent on the track, in track pixels. */
export type OverviewLineSpan = { top: number; bottom: number };

function coalesceSymbolOps(ops: readonly OverviewPaintOp[]): OverviewPaintOp[] {
  const out: OverviewPaintOp[] = [];
  let lastPixel: number | undefined;
  for (const op of ops) {
    if (op.kind !== "symbol") {
      out.push(op);
      continue;
    }
    const pixel = Math.round(op.y);
    if (
      lastPixel !== undefined &&
      pixel - lastPixel <= OVERVIEW_SYMBOL_COALESCE_DISTANCE
    ) {
      continue;
    }
    lastPixel = pixel;
    out.push(op);
  }
  return out;
}

/** Minimum tick heights keep single-line marks visible in long files. */
export function layoutOverviewTicks(
  ticks: readonly OverviewTick[],
  args: {
    spanForLine: (line: number) => OverviewLineSpan;
    height: number;
  },
): OverviewPaintOp[] {
  const { height } = args;
  if (height <= 0 || ticks.length === 0) return [];

  const buckets = new Map<string, OverviewPaintOp>();
  for (const tick of ticks) {
    const minHeight = OVERVIEW_KIND_HEIGHT[tick.kind];
    const head = args.spanForLine(tick.fromLine);
    const bottom =
      tick.toLine > tick.fromLine
        ? args.spanForLine(tick.toLine).bottom
        : head.bottom;
    const y = Math.max(0, Math.min(head.top, height - minHeight));
    const h = Math.max(minHeight, Math.min(height - y, bottom - y));
    const key = `${tick.kind}:${tick.window?.clientId ?? ""}:${tick.color ?? ""}:${Math.round(y)}:${Math.round(h)}`;
    if (!buckets.has(key)) {
      buckets.set(key, { kind: tick.kind, y, h, ...(tick.window ? { window: tick.window } : {}), ...(tick.color ? { color: tick.color } : {}) });
    }
  }

  const ops = [...buckets.values()].sort(
    (a, b) =>
      OVERVIEW_KIND_PRIORITY[a.kind] - OVERVIEW_KIND_PRIORITY[b.kind] ||
      a.y - b.y,
  );
  return coalesceSymbolOps(ops);
}
