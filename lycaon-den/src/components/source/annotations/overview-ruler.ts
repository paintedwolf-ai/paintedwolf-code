import { setSearchQuery } from "@codemirror/search";
import {
  Facet,
  StateEffect,
  StateField,
  type EditorState,
  type Extension,
} from "@codemirror/state";
import { EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import type { ScrollbarElements } from "overlayscrollbars";
import type { SourceSymbol } from "../../../api/types.ts";
import { verticalScrollbarChromeFor } from "../../../platform/scrolling/themed-scrollbars.ts";
import { watchWindowIdentityPaint } from "../../../contributions/palette-cache.ts";
import { onActiveThemeChange } from "../../../settings/appearance/appearance-prefs.ts";
import { editorScopeDiffChunks, type ScopeDiffChunks } from "../diff/scope-diff.ts";
import {
  ALL_OVERVIEW_TICK_KINDS,
  collectCurrentTicks,
  collectDiffTicks,
  collectMatchTicks,
  collectSymbolTicks,
  emptyOverviewSlices,
  layoutOverviewTicks,
  mergeOverviewTicks,
  overviewHandleLength,
  overviewHasOverflow,
  overviewMatchKey,
  OVERVIEW_WIDTH_PX,
  trackYForOffset,
  type OverviewKind,
  type OverviewLineSpan,
  type OverviewPaintOp,
  type OverviewSlices,
  type OverviewTick,
  type OverviewTickKinds,
  type OverviewTrackGeometry,
} from "./overview-ruler-model.ts";

import { collectWindowSelectionTicks, getOverviewWindowSelections } from "./overview-window-selections.ts";
import { collectAgentTicks, getOverviewAgentMarks } from "./overview-agent-marks.ts";

const overviewTickKinds = Facet.define<OverviewTickKinds, OverviewTickKinds>({
  combine: (values) => values[0] ?? ALL_OVERVIEW_TICK_KINDS,
});

/** Read-only projections supply host-computed changes in displayed coordinates. */
export const overviewDiffTicks = Facet.define<readonly OverviewTick[], readonly OverviewTick[] | null>({
  combine: values => values[0] ?? null,
});

type OverviewSymbolMark = { line: number };

const setOverviewSymbols = StateEffect.define<readonly OverviewSymbolMark[]>();
const EMPTY_OVERVIEW_SYMBOLS: readonly OverviewSymbolMark[] = [];

/** Holds symbol positions while their scrollbar category is disabled. */
export const overviewSymbolMarks = StateField.define<readonly OverviewSymbolMark[]>({
  create: () => EMPTY_OVERVIEW_SYMBOLS,
  update(value, transaction) {
    for (const effect of transaction.effects) {
      if (effect.is(setOverviewSymbols)) return effect.value;
    }
    return value;
  },
});

function getOverviewSymbolMarks(
  state: EditorState,
): readonly OverviewSymbolMark[] {
  return state.field(overviewSymbolMarks, false) ?? EMPTY_OVERVIEW_SYMBOLS;
}

export function setOverviewSymbolMarks(
  view: EditorView,
  symbols: readonly SourceSymbol[],
): void {
  const marks =
    symbols.length === 0
      ? EMPTY_OVERVIEW_SYMBOLS
      : symbols.map((symbol) => ({ line: symbol.line }));
  view.dispatch({ effects: setOverviewSymbols.of(marks) });
}

const OVERVIEW_CLASS = "cm-den-overview-ruler";

type OverviewPalette = Record<OverviewKind, string>;

// Symbols occupy the left lane, matches the center, and changes the right.
const LANE: Record<OverviewKind, { x: number; w: number; alpha: number }> = {
  symbol: { x: 0, w: 2, alpha: 0.28 },
  add: { x: 6, w: 2, alpha: 0.9 },
  del: { x: 6, w: 2, alpha: 0.9 },
  match: { x: 2, w: 4, alpha: 0.55 },
  current: { x: 0, w: 8, alpha: 0.3 },
  window: { x: 0, w: 8, alpha: 0.3 },
  // Agent marks share the match lane, muted.
  agent: { x: 2, w: 4, alpha: 0.5 },
  "agent-pending": { x: 2, w: 4, alpha: 0.6 },
};

// A solid edge marks the start of each selection span.
const CURRENT_EDGE = { h: 2, alpha: 0.85 };

function cssVar(css: CSSStyleDeclaration, name: string, fallback: string): string {
  return css.getPropertyValue(name).trim() || fallback;
}

function readOverviewPalette(el: Element): OverviewPalette {
  const css = getComputedStyle(el);
  const accent = cssVar(css, "--den-accent-signal", "#5b8def");
  return {
    symbol: cssVar(css, "--den-text-muted", "#8a8a8a"),
    add: cssVar(css, "--den-diff-add-mark", "#4caf50"),
    del: cssVar(css, "--den-diff-delete-mark", "#e05252"),
    match: accent,
    current: cssVar(css, "--den-current-window-caret", accent),
    window: accent,
    agent: cssVar(css, "--den-agent-color-main", "#8a8a8a"),
    "agent-pending": cssVar(css, "--den-agent-color-main", "#8a8a8a"),
  };
}

function paintOverviewOps(
  ctx: CanvasRenderingContext2D,
  ops: readonly OverviewPaintOp[],
  palette: OverviewPalette,
  dpr: number,
): void {
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, OVERVIEW_WIDTH_PX, ctx.canvas.height / dpr);
  const windowIds = [...new Set(ops.flatMap(op => op.window ? [op.window.clientId] : []))].sort();
  for (const op of ops) {
    const lane = { ...LANE[op.kind] };
    if (op.window) {
      lane.w /= windowIds.length;
      lane.x += windowIds.indexOf(op.window.clientId) * lane.w;
    }
    ctx.globalAlpha = lane.alpha;
    const color = op.window?.color ?? op.color ?? palette[op.kind];
    if (op.kind === "agent-pending") {
      // Pending changes are outlined, like their dashed ghost in the editor.
      ctx.strokeStyle = color;
      ctx.lineWidth = 1;
      ctx.strokeRect(lane.x + 0.5, op.y + 0.5, lane.w - 1, Math.max(1, op.h - 1));
      continue;
    }
    ctx.fillStyle = color;
    ctx.fillRect(lane.x, op.y, lane.w, op.h);
    if (op.kind === "current" || op.kind === "window") {
      ctx.globalAlpha = CURRENT_EDGE.alpha;
      ctx.fillRect(lane.x, op.y, lane.w, Math.min(CURRENT_EDGE.h, op.h));
    }
  }
  ctx.globalAlpha = 1;
}

/** Schedules idle work with a deadline and cancellation. */
function scheduleIdle(run: () => void, timeoutMs: number): () => void {
  if (typeof requestIdleCallback === "function") {
    const handle = requestIdleCallback(run, { timeout: timeoutMs });
    return () => cancelIdleCallback(handle);
  }
  const handle = requestAnimationFrame(run);
  return () => cancelAnimationFrame(handle);
}

const OVERVIEW_SYNC_SCAN_MAX_CHARS = 64 * 1024;
const OVERVIEW_MATCH_SCAN_TIMEOUT_MS = 500;
/** Line-height refinement while scrolling waits for a quiet moment. */
const OVERVIEW_GEOMETRY_PAINT_TIMEOUT_MS = 250;

/** The track the handle rides, placed within the editor element. */
type OverviewTrackBox = {
  top: number;
  height: number;
  geometry: OverviewTrackGeometry;
};

class OverviewRuler {
  readonly canvas: HTMLCanvasElement;
  ticks: OverviewTick[] = [];
  slices: OverviewSlices = emptyOverviewSlices();
  /** The last painted layout, in track pixels. */
  ops: OverviewPaintOp[] = [];
  private kinds: OverviewTickKinds;
  private matchKey = "";
  private symbolsRef: unknown = null;
  private windowsRef: unknown = null;
  private agentsRef: unknown = null;
  private chunksRef: ScopeDiffChunks = null;
  private diffTicksRef: readonly OverviewTick[] | null = null;
  private paintPending = false;
  private disposed = false;
  private cancelGeometryPaint: (() => void) | undefined;
  private cancelMatchScan: (() => void) | undefined;
  private placedTop = Number.NaN;
  private placedHeight = Number.NaN;
  private palette: OverviewPalette | undefined;
  private minHandleLength: number | undefined;
  private readonly chromeObserver: ResizeObserver | undefined;
  private observedScrollbar: HTMLElement | undefined;
  private readonly stopTheme: () => void;
  private readonly stopWindowPaint: () => void;
  private readonly view: EditorView;

  constructor(view: EditorView) {
    this.view = view;
    this.canvas = document.createElement("canvas");
    this.canvas.className = OVERVIEW_CLASS;
    this.canvas.setAttribute("aria-hidden", "true");
    this.canvas.dataset.testid = "files-overview-ruler";
    this.canvas.hidden = true;
    view.dom.appendChild(this.canvas);
    this.kinds = view.state.facet(overviewTickKinds);
    if (this.rebuildImmediate()) this.scheduleMatchRebuild();
    if (typeof ResizeObserver === "function") {
      // The track follows the editor box.
      this.chromeObserver = new ResizeObserver(() => this.scheduleGeometryPaint());
    }
    this.stopWindowPaint = watchWindowIdentityPaint(view.dom.ownerDocument.documentElement, () => {
      this.palette = undefined;
      this.schedulePaint();
    });
    this.stopTheme = onActiveThemeChange(() => {
      this.palette = undefined;
      this.minHandleLength = undefined;
      this.schedulePaint();
    });
    this.schedulePaint();
  }

  update(update: ViewUpdate): void {
    const kinds = update.state.facet(overviewTickKinds);
    if (kinds !== this.kinds) {
      this.kinds = kinds;
      this.cancelMatchRebuild();
      if (this.rebuildImmediate()) this.scheduleMatchRebuild();
      this.schedulePaint();
      return;
    }
    const symbols = getOverviewSymbolMarks(update.state);
    const symbolsDirty = symbols !== this.symbolsRef;
    const windows = getOverviewWindowSelections(update.state);
    const windowsDirty = windows !== this.windowsRef;
    const agents = getOverviewAgentMarks(update.state);
    const agentsDirty = agents !== this.agentsRef;
    const chunks = editorScopeDiffChunks(update.state);
    // Chunk offsets are document positions, so an edit re-maps them too.
    const chunksDirty =
      chunks !== this.chunksRef || update.state.facet(overviewDiffTicks) !== this.diffTicksRef || (update.docChanged && chunks !== null);
    const searchDirty = update.transactions.some((transaction) =>
      transaction.effects.some((effect) => effect.is(setSearchQuery)),
    );
    if (
      !update.docChanged &&
      !update.selectionSet &&
      !symbolsDirty &&
      !windowsDirty &&
      !agentsDirty &&
      !chunksDirty &&
      !searchDirty
    ) {
      // Measured line heights move ticks without changing them.
      if (update.geometryChanged) this.scheduleGeometryPaint();
      return;
    }

    let mergedDirty = false;
    if (windowsDirty) {
      this.windowsRef = windows;
      if (kinds.windows) {
        this.slices.windows = collectWindowSelectionTicks(update.state);
        mergedDirty = true;
      }
    }
    if (agentsDirty) {
      this.agentsRef = agents;
      if (kinds.agents) {
        this.slices.agents = collectAgentTicks(update.state);
        mergedDirty = true;
      }
    }
    if (kinds.cursor && (update.docChanged || update.selectionSet)) {
      this.slices.current = collectCurrentTicks(update.state);
      mergedDirty = true;
    }
    if (symbolsDirty) {
      this.symbolsRef = symbols;
      if (kinds.symbols) {
        this.slices.symbols = collectSymbolTicks(symbols);
        mergedDirty = true;
      }
    }
    if (chunksDirty) {
      this.chunksRef = chunks;
      this.diffTicksRef = update.state.facet(overviewDiffTicks);
      if (kinds.changes) {
        this.slices.diff = [...(this.diffTicksRef ?? collectDiffTicks(chunks, update.state.doc))];
        mergedDirty = true;
      }
    }

    if (kinds.matches && (update.docChanged || update.selectionSet || searchDirty)) {
      const nextKey = overviewMatchKey(update.state);
      const matchesDirty =
        update.docChanged || searchDirty || nextKey !== this.matchKey;
      if (matchesDirty) {
        this.matchKey = nextKey;
        this.cancelMatchRebuild();
        if (nextKey === "") {
          this.slices.matches = [];
        } else if (update.state.doc.length <= OVERVIEW_SYNC_SCAN_MAX_CHARS) {
          this.slices.matches = collectMatchTicks(update.state);
        } else {
          this.slices.matches = [];
          this.scheduleMatchRebuild();
        }
        mergedDirty = true;
      }
    }

    if (mergedDirty) {
      this.ticks = mergeOverviewTicks(this.slices);
      this.schedulePaint();
    }
  }

  destroy(): void {
    this.disposed = true;
    this.cancelGeometryPaint?.();
    this.cancelMatchRebuild();
    this.chromeObserver?.disconnect();
    this.stopTheme();
    this.stopWindowPaint();
    this.canvas.remove();
  }

  /** Reports whether match scanning was deferred. */
  private rebuildImmediate(): boolean {
    const { state } = this.view;
    const { kinds } = this;
    this.matchKey = kinds.matches ? overviewMatchKey(state) : "";
    const deferMatches =
      this.matchKey !== "" && state.doc.length > OVERVIEW_SYNC_SCAN_MAX_CHARS;
    const symbols = getOverviewSymbolMarks(state);
    this.symbolsRef = symbols;
    this.windowsRef = getOverviewWindowSelections(state);
    this.agentsRef = getOverviewAgentMarks(state);
    const chunks = editorScopeDiffChunks(state);
    this.chunksRef = chunks;
    this.diffTicksRef = state.facet(overviewDiffTicks);
    this.slices = {
      current: kinds.cursor ? collectCurrentTicks(state) : [],
      matches: kinds.matches && !deferMatches ? collectMatchTicks(state) : [],
      symbols: kinds.symbols ? collectSymbolTicks(symbols) : [],
      diff: kinds.changes ? [...(this.diffTicksRef ?? collectDiffTicks(chunks, state.doc))] : [],
      windows: kinds.windows ? collectWindowSelectionTicks(state) : [],
      agents: kinds.agents ? collectAgentTicks(state) : [],
    };
    this.ticks = mergeOverviewTicks(this.slices);
    return deferMatches;
  }

  private scheduleMatchRebuild(): void {
    this.cancelMatchRebuild();
    this.cancelMatchScan = scheduleIdle(() => {
      this.cancelMatchScan = undefined;
      const { state } = this.view;
      this.matchKey = overviewMatchKey(state);
      this.slices.matches = collectMatchTicks(state);
      this.ticks = mergeOverviewTicks(this.slices);
      this.schedulePaint();
    }, OVERVIEW_MATCH_SCAN_TIMEOUT_MS);
  }

  private cancelMatchRebuild(): void {
    this.cancelMatchScan?.();
    this.cancelMatchScan = undefined;
  }

  private schedulePaint(): void {
    if (this.paintPending || this.disposed) return;
    this.cancelGeometryPaint?.();
    this.cancelGeometryPaint = undefined;
    this.paintPending = true;
    // Ruler geometry shares the editor measurement pass.
    this.view.requestMeasure({
      key: this,
      read: () => {
        const track = this.measureTrack();
        const ops = track && overviewHasOverflow(track.geometry.scrollHeight, track.geometry.clientHeight)
          ? layoutOverviewTicks(this.ticks, {
            height: track.height,
            spanForLine: (line) => this.spanForLine(line, track.geometry),
          }) : [];
        this.palette ??= readOverviewPalette(this.view.dom);
        return { track, ops, dpr: this.view.dom.ownerDocument.defaultView?.devicePixelRatio || 1 };
      },
      write: ({ track, ops, dpr }) => {
        this.paintPending = false;
        if (!this.disposed) this.paintTrack(track, ops, dpr);
      },
    });
  }

  private scheduleGeometryPaint(): void {
    if (this.paintPending || this.cancelGeometryPaint) {
      return;
    }
    this.cancelGeometryPaint = scheduleIdle(() => {
      this.cancelGeometryPaint = undefined;
      this.schedulePaint();
    }, OVERVIEW_GEOMETRY_PAINT_TIMEOUT_MS);
  }

  private observeChrome(chrome: ScrollbarElements | undefined): void {
    if (chrome?.scrollbar === this.observedScrollbar) return;
    this.chromeObserver?.disconnect();
    this.observedScrollbar = chrome?.scrollbar;
    this.minHandleLength = undefined;
    if (!chrome || !this.chromeObserver) return;
    this.chromeObserver.observe(chrome.scrollbar);
  }

  private measureTrack(): OverviewTrackBox | undefined {
    const chrome = verticalScrollbarChromeFor(this.view.dom);
    this.observeChrome(chrome);
    if (!chrome) return undefined;
    const trackLength = chrome.track.offsetHeight;
    if (trackLength <= 0) return undefined;
    const { view } = this;
    const scrollHeight = view.contentHeight;
    const clientHeight = view.scrollDOM.clientHeight;
    this.minHandleLength ??=
      Number.parseFloat(getComputedStyle(chrome.handle).minHeight) || 0;
    return {
      top: chrome.scrollbar.offsetTop + chrome.track.offsetTop,
      height: trackLength,
      geometry: {
        trackLength,
        handleLength: overviewHandleLength({
          trackLength,
          scrollHeight,
          clientHeight,
          minHandleLength: this.minHandleLength,
        }),
        scrollHeight,
        clientHeight,
      },
    };
  }

  private spanForLine(
    line: number,
    geometry: OverviewTrackGeometry,
  ): OverviewLineSpan {
    const { view } = this;
    const doc = view.state.doc;
    const clamped = Math.min(Math.max(line, 1), doc.lines);
    const block = view.lineBlockAt(doc.line(clamped).from);
    const top = view.documentPadding.top + block.top;
    return {
      top: trackYForOffset(top, geometry),
      bottom: trackYForOffset(top + block.height, geometry),
    };
  }

  private place(track: OverviewTrackBox): void {
    if (track.top !== this.placedTop) {
      this.placedTop = track.top;
      this.canvas.style.top = `${track.top}px`;
    }
    if (track.height !== this.placedHeight) {
      this.placedHeight = track.height;
      this.canvas.style.height = `${track.height}px`;
    }
  }

  private paintTrack(track: OverviewTrackBox | undefined, ops: OverviewPaintOp[], dpr: number): void {
    const { scrollHeight, clientHeight } = track?.geometry ?? {
      scrollHeight: 0,
      clientHeight: 0,
    };
    if (!track || !overviewHasOverflow(scrollHeight, clientHeight)) {
      this.ops = [];
      this.canvas.hidden = true;
      return;
    }
    this.ops = ops;

    this.canvas.hidden = false;
    this.place(track);
    const nextW = Math.max(1, Math.round(OVERVIEW_WIDTH_PX * dpr));
    const nextH = Math.max(1, Math.round(track.height * dpr));
    if (this.canvas.width !== nextW) this.canvas.width = nextW;
    if (this.canvas.height !== nextH) this.canvas.height = nextH;
    const ctx = this.canvas.getContext("2d");
    if (!ctx) return;
    if (this.palette) paintOverviewOps(ctx, this.ops, this.palette, dpr);
  }
}

const overviewRulerTheme = EditorView.baseTheme({
  [`.${OVERVIEW_CLASS}`]: {
    position: "absolute",
    right: "0",
    width: `${OVERVIEW_WIDTH_PX}px`,
    pointerEvents: "none",
    zIndex: "0",
  },
});

export const overviewRulerPlugin = ViewPlugin.fromClass(OverviewRuler);

const NO_OVERVIEW_RULER: Extension = [];
const overviewRulerByKinds = new Map<string, Extension>();

/** Caches ruler extensions by enabled categories; an empty set mounts nothing. */
export function overviewRuler(kinds: OverviewTickKinds): Extension {
  const { symbols, changes, matches, cursor, windows, agents } = kinds;
  if (!symbols && !changes && !matches && !cursor && !windows && !agents) return NO_OVERVIEW_RULER;
  const key = [symbols, changes, matches, cursor, windows, agents].map(Number).join("");
  const held = overviewRulerByKinds.get(key);
  if (held) return held;
  const ext: Extension = [
    overviewTickKinds.of({ symbols, changes, matches, cursor, windows, agents }),
    overviewRulerTheme,
    overviewRulerPlugin,
  ];
  overviewRulerByKinds.set(key, ext);
  return ext;
}
