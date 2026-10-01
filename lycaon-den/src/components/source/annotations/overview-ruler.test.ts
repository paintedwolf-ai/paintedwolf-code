// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { search, SearchQuery, setSearchQuery } from "@codemirror/search";
import { EditorState, StateEffect } from "@codemirror/state";
import { EditorView, type ViewUpdate } from "@codemirror/view";
import {
  applyEditorDisplayPrefs,
  createSourceEditorState,
  type EditorDisplayPrefs,
} from "../editor/codemirror-theme.ts";
import { verticalScrollbarChromeFor } from "../../../platform/scrolling/themed-scrollbars.ts";
import { applyEditorScopeDiff } from "../diff/scope-diff.ts";
import {
  overviewRuler,
  overviewRulerPlugin,
  overviewSymbolMarks,
  setOverviewSymbolMarks,
} from "./overview-ruler.ts";
import {
  ALL_OVERVIEW_TICK_KINDS,
  type OverviewTickKinds,
} from "./overview-ruler-model.ts";

import { overviewWindowSelections, setOverviewWindowSelections } from "./overview-window-selections.ts";

const NO_TICKS: OverviewTickKinds = {
  symbols: false,
  changes: false,
  matches: false,
  cursor: false,
  windows: false,
  agents: false,
};

function displayPrefs(scrollbarTicks: OverviewTickKinds): EditorDisplayPrefs {
  return {
    wordWrap: false,
    lineNumbers: true,
    fontSize: 13,
    fontFamily: "default",
    lineHeight: 1.5,
    indentGuides: true,
    whitespace: false,
    scrollbarTicks,
    indent: { style: "spaces", width: 4 },
  };
}

function flushPaint(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

function mount(doc: string, kinds: OverviewTickKinds = ALL_OVERVIEW_TICK_KINDS) {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({
    parent,
    state: EditorState.create({
      doc,
      selection: { anchor: 0 },
      extensions: [overviewSymbolMarks, overviewRuler(kinds), search()],
    }),
  });
}

function layOut(el: Element, metrics: Record<string, number>): void {
  for (const [name, value] of Object.entries(metrics)) {
    Object.defineProperty(el, name, { value, configurable: true });
  }
}

describe("overview ruler", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
    vi.unstubAllGlobals();
  });

  it("mounts a decorative canvas with current focus and other matches", async () => {
    view = mount("alpha\nbeta\nalpha\n");
    const canvas = view.dom.querySelector("[data-testid='files-overview-ruler']");
    expect(canvas).toBeInstanceOf(HTMLCanvasElement);
    expect(canvas?.getAttribute("aria-hidden")).toBe("true");
    const plugin = view.plugin(overviewRulerPlugin);
    expect(plugin?.ticks).toEqual([
      { kind: "match", fromLine: 3, toLine: 3 },
      { kind: "current", fromLine: 1, toLine: 1 },
    ]);

    await flushPaint();
    view.destroy();
    view = undefined;
    expect(document.querySelector("[data-testid='files-overview-ruler']")).toBeNull();
  });

  it("coalesces geometry updates without rebuilding ticks", async () => {
    view = mount("alpha\nbeta\nalpha\n");
    await flushPaint();
    const plugin = view.plugin(overviewRulerPlugin)!;
    const ticks = plugin.ticks;
    const matches = plugin.slices.matches;
    let paint: IdleRequestCallback | undefined;
    const requestIdle = vi.fn((callback: IdleRequestCallback) => {
      paint = callback;
      return 1;
    });
    vi.stubGlobal("requestIdleCallback", requestIdle);
    vi.stubGlobal("cancelIdleCallback", vi.fn());
    const update = {
      state: view.state,
      transactions: [],
      docChanged: false,
      selectionSet: false,
      geometryChanged: true,
    } as unknown as ViewUpdate;

    for (let index = 0; index < 10; index++) plugin.update(update);

    expect(requestIdle).toHaveBeenCalledOnce();
    expect(plugin.ticks).toBe(ticks);
    expect(plugin.slices.matches).toBe(matches);
    paint!({ didTimeout: false, timeRemaining: () => 50 });
    expect(plugin.ticks).toBe(ticks);
    expect(plugin.slices.matches).toBe(matches);
  });

  it("rebuilds passive matches only when the caret target changes", () => {
    view = mount("alpha beta alpha\n");
    const plugin = view.plugin(overviewRulerPlugin)!;
    const first = plugin.slices.matches;
    view.dispatch({ selection: { anchor: 1 } });
    expect(plugin.slices.matches).toBe(first);
    view.dispatch({ selection: { anchor: 6 } });
    expect(plugin.slices.matches).toEqual([]);
  });

  it("maps a selection and its other literal matches", () => {
    view = mount("cat\nconcatenate\ncat\n");
    const plugin = view.plugin(overviewRulerPlugin)!;
    view.dispatch({ selection: { anchor: 0, head: 3 } });
    expect(plugin.slices.current).toEqual([
      { kind: "current", fromLine: 1, toLine: 1 },
    ]);
    expect(plugin.slices.matches).toEqual([
      { kind: "match", fromLine: 2, toLine: 2 },
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
  });

  it("lets Find replace passive matches without adding another visual kind", () => {
    view = mount("foo\nbar\nfoo\nbar\n");
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.slices.matches).toEqual([
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);

    view.dispatch({
      effects: setSearchQuery.of(new SearchQuery({ search: "bar", literal: true })),
    });
    expect(plugin.slices.matches).toEqual([
      { kind: "match", fromLine: 2, toLine: 2 },
      { kind: "match", fromLine: 4, toLine: 4 },
    ]);

    view.dispatch({ selection: { anchor: 4, head: 7 } });
    expect(plugin.slices.current).toEqual([
      { kind: "current", fromLine: 2, toLine: 2 },
    ]);
    expect(plugin.slices.matches).toEqual([
      { kind: "match", fromLine: 4, toLine: 4 },
    ]);
    expect(new Set(plugin.ticks.map((tick) => tick.kind))).toEqual(
      new Set(["current", "match"]),
    );
  });

  it("uses one symbol style and one tick per declaration line", () => {
    view = mount("a\nb\nc\n");
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.slices.symbols).toEqual([]);
    setOverviewSymbolMarks(view, [
      { name: "Box", kind: "class", line: 1 },
      { name: "Size", kind: "method", line: 3 },
      { name: "Again", kind: "constant", line: 3 },
    ]);
    expect(plugin.slices.symbols).toEqual([
      { kind: "symbol", fromLine: 1, toLine: 1 },
      { kind: "symbol", fromLine: 3, toLine: 3 },
    ]);
  });

  it("shows the applied comparison as add and delete ticks in one lane", () => {
    view = new EditorView({
      parent: document.body,
      state: createSourceEditorState({ surface: "files",
        doc: "one\ntwo\nthree\nfour\n",
        editable: true,
      }),
    });
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.slices.diff).toEqual([]);

    applyEditorScopeDiff(view, { original: "one\nzwei\nthree\nfour\nfive\n" });
    expect(plugin.slices.diff).toEqual([
      { kind: "add", fromLine: 2, toLine: 2 },
      { kind: "del", fromLine: 2, toLine: 2 },
      { kind: "del", fromLine: 5, toLine: 5 },
    ]);
    expect(plugin.ticks).toContainEqual({ kind: "add", fromLine: 2, toLine: 2 });

    // Moving the caret leaves the comparison slice untouched.
    const held = plugin.slices.diff;
    view.dispatch({ selection: { anchor: 4 } });
    expect(plugin.slices.diff).toBe(held);

    // An edit re-maps the ticks onto the new lines.
    view.dispatch({ changes: { from: 0, insert: "zero\n" } });
    expect(plugin.slices.diff).toContainEqual({
      kind: "add",
      fromLine: 1,
      toLine: 1,
    });

    applyEditorScopeDiff(view, null);
    expect(plugin.slices.diff).toEqual([]);
    expect(plugin.ticks.some((tick) => tick.kind === "add" || tick.kind === "del")).toBe(
      false,
    );
  });

  it("stays hidden without a scrollbar track to annotate", async () => {
    view = mount("a\nb\nc\n");
    await flushPaint();
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.canvas.hidden).toBe(true);
    expect(plugin.ops).toEqual([]);
  });

  it("places ticks on the scrollbar track through the thumb's travel", async () => {
    // Handle size follows the viewport ratio; scroll range includes editor padding.
    const lines = Array.from({ length: 200 }, (_, i) => `line ${i + 1}`);
    view = new EditorView({
      parent: document.body,
      state: createSourceEditorState({ surface: "files", doc: lines.join("\n"), editable: true }),
    });
    const plugin = view.plugin(overviewRulerPlugin)!;
    Object.defineProperty(plugin.canvas, "getContext", { value: () => null });
    // Scrollbar attachment joins the editor's first measurement write phase.
    await vi.waitFor(() => expect(verticalScrollbarChromeFor(view!.dom)).toBeDefined());
    const chrome = verticalScrollbarChromeFor(view.dom)!;
    const clientHeight = 200;
    layOut(view.scrollDOM, { clientHeight });
    layOut(chrome.track, { offsetHeight: 100, offsetTop: 2 });
    const from = view.state.doc.line(190).from;
    view.dispatch({ selection: { anchor: from, head: view.state.doc.length } });
    await flushPaint();

    const scrollHeight = view.contentHeight;
    const scrollRange = scrollHeight - clientHeight;
    const handle = (clientHeight / scrollHeight) * 100;
    const travel = 100 - handle;
    const expectY = (offset: number) =>
      offset <= scrollRange
        ? (offset / scrollRange) * travel
        : travel + ((offset - scrollRange) / clientHeight) * handle;
    const pad = view.documentPadding.top;
    const head = view.lineBlockAt(from);
    const last = view.lineBlockAt(view.state.doc.length);

    expect(plugin.canvas.hidden).toBe(false);
    expect(plugin.canvas.style.top).toBe("2px");
    expect(plugin.canvas.style.height).toBe("100px");
    const tick = plugin.ops.find((op) => op.kind === "current")!;
    expect(tick.y).toBeCloseTo(expectY(pad + head.top));
    expect(tick.y + tick.h).toBeCloseTo(expectY(pad + last.top + last.height));
    // The space past the end keeps the last line off the bottom of the track.
    expect(tick.y + tick.h).toBeLessThan(100 - 1);
  });

  it("follows the measured document height, so ticks stay under the thumb", async () => {
    // Tick positions follow measured content height.
    const lines = Array.from({ length: 400 }, (_, i) => `line ${i + 1}`);
    view = new EditorView({
      parent: document.body,
      state: createSourceEditorState({ surface: "files", doc: lines.join("\n"), editable: true }),
    });
    const plugin = view.plugin(overviewRulerPlugin)!;
    Object.defineProperty(plugin.canvas, "getContext", { value: () => null });
    // Scrollbar attachment joins the editor's first measurement write phase.
    await vi.waitFor(() => expect(verticalScrollbarChromeFor(view!.dom)).toBeDefined());
    const chrome = verticalScrollbarChromeFor(view.dom)!;
    layOut(view.scrollDOM, { clientHeight: 200 });
    layOut(chrome.track, { offsetHeight: 100, offsetTop: 0 });
    setOverviewSymbolMarks(view, [{ name: "Deep", kind: "function", line: 380 }]);
    await flushPaint();
    const [before] = plugin.ops.filter((op) => op.kind === "symbol");
    expect(before).toBeDefined();

    // Measuring the rest of the file makes it taller.
    Object.defineProperty(view, "contentHeight", {
      value: view.contentHeight * 2,
      configurable: true,
    });
    view.dispatch({ selection: { anchor: 0 } });
    await flushPaint();
    const [after] = plugin.ops.filter((op) => op.kind === "symbol");
    expect(after!.y).toBeLessThan(before!.y);
  });

  it("collects only the enabled tick kinds", () => {
    view = mount("alpha\nbeta\nalpha\n", { ...NO_TICKS, symbols: true });
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.ticks).toEqual([]);
    setOverviewSymbolMarks(view, [{ name: "Box", kind: "class", line: 2 }]);
    view.dispatch({ selection: { anchor: 6 } });
    expect(plugin.ticks).toEqual([{ kind: "symbol", fromLine: 2, toLine: 2 }]);
  });

  it("retains window selections while their category is off and removes departed windows", () => {
    view = new EditorView({ parent: document.body, state: createSourceEditorState({ surface: "files", doc: "one\ntwo\nthree", editable: true }) });
    view.dispatch({ effects: StateEffect.appendConfig.of(overviewWindowSelections) });
    const selections = [{ clientId: "window:2", anchor: 4, head: 13, color: "#3377aa" }];
    view.dispatch({ effects: setOverviewWindowSelections.of(selections) });
    expect(view.plugin(overviewRulerPlugin)!.slices.windows).toEqual([
      { kind: "window", fromLine: 2, toLine: 3, window: { clientId: "window:2", color: "#3377aa" } },
    ]);
    applyEditorDisplayPrefs(view, displayPrefs({ ...ALL_OVERVIEW_TICK_KINDS, windows: false }));
    expect(view.plugin(overviewRulerPlugin)!.slices.windows).toEqual([]);
    applyEditorDisplayPrefs(view, displayPrefs(NO_TICKS));
    view.dispatch({ effects: setOverviewWindowSelections.of([{ ...selections[0]!, head: 7 }]) });
    applyEditorDisplayPrefs(view, displayPrefs({ ...NO_TICKS, windows: true }));
    expect(view.plugin(overviewRulerPlugin)!.ticks).toEqual([
      { kind: "window", fromLine: 2, toLine: 2, window: { clientId: "window:2", color: "#3377aa" } },
    ]);
    view.dispatch({ effects: setOverviewWindowSelections.of([]) });
    expect(view.plugin(overviewRulerPlugin)!.ticks).toEqual([]);
  });

  it("paints overlapping window colors in separate columns and repaints the current identity", async () => {
    view = new EditorView({ parent: document.body, state: createSourceEditorState({ surface: "files",
      doc: Array.from({ length: 200 }, (_, i) => `line ${i}`).join("\n"), editable: true,
      scrollbarTicks: { ...NO_TICKS, cursor: true, windows: true },
    }) });
    view.dispatch({ effects: StateEffect.appendConfig.of(overviewWindowSelections) });
    const plugin = view.plugin(overviewRulerPlugin)!;
    const painted: { color: string; x: number; width: number }[] = [];
    const context = { canvas: plugin.canvas, setTransform: vi.fn(), clearRect: vi.fn(), globalAlpha: 1, fillStyle: "",
      fillRect(x: number, _y: number, width: number) { painted.push({ color: this.fillStyle, x, width }); },
    };
    Object.defineProperty(plugin.canvas, "getContext", { value: () => context });
    view.dom.style.setProperty("--den-current-window-caret", "#5588aa");
    // Scrollbar attachment joins the editor's first measurement write phase.
    await vi.waitFor(() => expect(verticalScrollbarChromeFor(view!.dom)).toBeDefined());
    const chrome = verticalScrollbarChromeFor(view.dom)!;
    layOut(view.scrollDOM, { clientHeight: 200 });
    layOut(chrome.track, { offsetHeight: 100, offsetTop: 0 });
    const anchor = view.state.doc.line(10).from, head = view.state.doc.line(20).to;
    view.dispatch({ effects: setOverviewWindowSelections.of([
      { clientId: "two", anchor, head, color: "#aa7755" },
      { clientId: "three", anchor, head, color: "#7755aa" },
    ]) });
    await flushPaint();
    expect(painted).toContainEqual({ color: "#5588aa", x: 0, width: 8 });
    expect(painted).toContainEqual({ color: "#7755aa", x: 0, width: 4 });
    expect(painted).toContainEqual({ color: "#aa7755", x: 4, width: 4 });
    painted.length = 0;
    view.dom.style.setProperty("--den-current-window-caret", "#6699bb");
    document.documentElement.style.setProperty("--den-current-window-caret", "#6699bb");
    await vi.waitFor(() => expect(painted).toContainEqual({ color: "#6699bb", x: 0, width: 8 }));
    document.documentElement.style.removeProperty("--den-current-window-caret");
  });

  it("mounts nothing when every tick kind is off", () => {
    view = new EditorView({
      parent: document.body,
      state: createSourceEditorState({ surface: "files",
        doc: "alpha\nbeta\nalpha\n",
        editable: true,
        scrollbarTicks: NO_TICKS,
      }),
    });
    expect(view.plugin(overviewRulerPlugin)).toBeNull();
    expect(view.dom.querySelector("[data-testid='files-overview-ruler']")).toBeNull();
  });

  it("follows display prefs live and keeps symbol lines across a remount", () => {
    view = new EditorView({
      parent: document.body,
      state: createSourceEditorState({ surface: "files",
        doc: "alpha\nbeta\nalpha\n",
        editable: true,
      }),
    });
    setOverviewSymbolMarks(view, [{ name: "Box", kind: "class", line: 2 }]);

    applyEditorDisplayPrefs(view, displayPrefs({ ...ALL_OVERVIEW_TICK_KINDS, matches: false }));
    const plugin = view.plugin(overviewRulerPlugin)!;
    expect(plugin.slices.matches).toEqual([]);
    expect(plugin.slices.symbols).toEqual([
      { kind: "symbol", fromLine: 2, toLine: 2 },
    ]);

    applyEditorDisplayPrefs(view, displayPrefs(NO_TICKS));
    expect(view.plugin(overviewRulerPlugin)).toBeNull();

    applyEditorDisplayPrefs(view, displayPrefs(ALL_OVERVIEW_TICK_KINDS));
    const remounted = view.plugin(overviewRulerPlugin)!;
    expect(remounted.slices.symbols).toEqual([
      { kind: "symbol", fromLine: 2, toLine: 2 },
    ]);
    expect(remounted.slices.matches).toEqual([
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
  });

  it("rides the full source editor and stays off long-line simplified buffers", () => {
    const normal = new EditorView({
      state: createSourceEditorState({ surface: "files",
        doc: "alpha\nbeta\nalpha\n",
        editable: true,
      }),
    });
    try {
      expect(normal.plugin(overviewRulerPlugin)).toBeTruthy();
    } finally {
      normal.destroy();
    }

    const simplified = new EditorView({
      state: createSourceEditorState({ surface: "files", doc: "x".repeat(10_001), editable: true }),
    });
    try {
      expect(simplified.plugin(overviewRulerPlugin)).toBeNull();
      expect(
        simplified.dom.querySelector("[data-testid='files-overview-ruler']"),
      ).toBeNull();
    } finally {
      simplified.destroy();
    }
  });
});
