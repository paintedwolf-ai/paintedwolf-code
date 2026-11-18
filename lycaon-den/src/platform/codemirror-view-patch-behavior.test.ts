// @vitest-environment jsdom
import { EditorView, GutterMarker, gutter, drawSelection, RectangleMarker } from "@codemirror/view";
import { EditorSelection, type Extension } from "@codemirror/state";
import { afterEach, describe, expect, it, vi } from "vitest";

type Range = { from: number; to: number };
type ViewportInternals = {
  measure(): void;
  measureScheduled: number;
  observer: { intersecting: boolean; onScrollChanged(event: { target: EventTarget }): void };
  viewState: {
    pixelViewport: { left: number; right: number; top: number; bottom: number };
    printing: boolean;
    scrollDelta: number;
    scrollJump: boolean;
    scrollSettled: boolean;
    getViewport(bias: number, scrollTarget: null): Range;
    viewportIsAppropriate(viewport: Range, bias: number): boolean;
    viewportRests(viewport: Range): boolean;
    restingViewport(base: Range): Range;
  };
};

class TextMarker extends GutterMarker {
  constructor(readonly text: string) {
    super();
  }
  eq(other: TextMarker): boolean {
    return other.text === this.text;
  }
  toDOM(): HTMLElement {
    const span = document.createElement("span");
    span.textContent = this.text;
    return span;
  }
}

const lineText = gutter({
  class: "probe-gutter",
  lineMarker: (view, line) => new TextMarker(view.state.sliceDoc(line.from, line.to)),
  lineMarkerChange: (update) => update.docChanged,
});

let view: EditorView | undefined;
afterEach(() => {
  view?.destroy();
  view = undefined;
  document.body.replaceChildren();
});

function mount(doc: string, extensions: Extension = [lineText]): EditorView {
  return new EditorView({ doc, extensions, parent: document.body.appendChild(document.createElement("div")) });
}

const cells = (target: EditorView) => [...target.dom.querySelectorAll<HTMLElement>(".probe-gutter .cm-gutterElement span")];

describe("CodeMirror viewport margin", () => {
  function renderedLines(target: EditorView, delta: number, jump: boolean): number {
    const { viewState } = target as unknown as ViewportInternals;
    viewState.pixelViewport = { left: 0, right: 800, top: 20_000, bottom: 20_600 };
    viewState.scrollDelta = delta;
    viewState.scrollJump = jump;
    const viewport = viewState.getViewport(0, null);
    const doc = target.state.doc;
    return doc.lineAt(viewport.to).number - doc.lineAt(viewport.from).number + 1;
  }

  it("keeps a speed-scaled lead for continuous motion however far a frame moved", () => {
    view = mount(Array.from({ length: 5_000 }, (_, i) => `line ${i}`).join("\n"), []);
    // A slow frame moves a fling more than half the visible area.
    const fling = renderedLines(view, 450, false);
    const jump = renderedLines(view, 450, true);
    expect(fling).toBeGreaterThan(jump * 3);
    // A small scrollbar step still glides.
    expect(renderedLines(view, 100, true)).toBe(renderedLines(view, 100, false));
    // The lead leans toward the motion.
    const { viewState } = view as unknown as ViewportInternals;
    viewState.scrollDelta = 450;
    viewState.scrollJump = false;
    const leaning = viewState.getViewport(0, null);
    expect(view.lineBlockAt(leaning.to).bottom - 20_600).toBeGreaterThan(20_000 - view.lineBlockAt(leaning.from).top);
  });

  it("swaps while the rendered lead still covers three frames of travel", () => {
    view = mount(Array.from({ length: 5_000 }, (_, i) => `line ${i}`).join("\n"), []);
    const { viewState } = view as unknown as ViewportInternals;
    viewState.pixelViewport = { left: 0, right: 800, top: 20_000, bottom: 20_600 };
    // The rendered range ends between the unpatched cover cap (250px) and the patched one (600px).
    const lineEndingBelow = (height: number) => {
      let block = view!.lineBlockAt(0);
      while (block.bottom < height) block = view!.lineBlockAt(block.to + 1);
      return block;
    };
    const viewport = { from: lineEndingBelow(19_500).from, to: lineEndingBelow(21_000).to };
    expect(view.lineBlockAt(viewport.to).bottom).toBeLessThan(21_200);
    expect(viewState.viewportIsAppropriate(viewport, 250)).toBe(true);
    expect(viewState.viewportIsAppropriate(viewport, 900)).toBe(false);
  });

  it("rests with the base margin on both sides, keeping a lead the motion left", () => {
    view = mount(Array.from({ length: 5_000 }, (_, i) => `line ${i}`).join("\n"), []);
    const { viewState } = view as unknown as ViewportInternals;
    viewState.pixelViewport = { left: 0, right: 800, top: 20_000, bottom: 20_600 };
    const edges = (range: Range) => ({ top: view!.lineBlockAt(range.from).top, bottom: view!.lineBlockAt(range.to).bottom });

    viewState.scrollDelta = 450;
    viewState.scrollJump = true;
    const jumped = viewState.getViewport(0, null);
    expect(viewState.viewportRests(jumped)).toBe(false);
    const rested = viewState.restingViewport(jumped);
    expect(edges(rested).top).toBeLessThanOrEqual(19_000);
    expect(edges(rested).bottom).toBeGreaterThanOrEqual(21_600);
    expect(viewState.viewportRests(rested)).toBe(true);

    viewState.scrollJump = false;
    const glided = viewState.getViewport(0, null);
    const restedGlide = viewState.restingViewport(glided);
    expect(restedGlide.to).toBe(glided.to);
    expect(edges(restedGlide).top).toBeLessThanOrEqual(19_000);
  });

  it("marks a settle for the next measure", () => {
    view = mount("one\ntwo", []);
    const internals = view as unknown as ViewportInternals;
    view.noteScrollSettled();
    expect(internals.viewState.scrollSettled).toBe(true);
    internals.measure();
    expect(internals.viewState.scrollSettled).toBe(false);
  });

  it("reports each measure's duration and whether it replaced the viewport", () => {
    const timings: [number, boolean][] = [];
    view = mount("one\ntwo", [EditorView.measureTiming.of((ms, swapped) => timings.push([ms, swapped]))]);
    (view as unknown as ViewportInternals).measure();
    expect(timings.at(-1)?.[0]).toBeGreaterThanOrEqual(0);
    expect(timings.at(-1)?.[1]).toBe(false);
  });

  it("clears the jump mark with the measure that consumes it", () => {
    view = mount("one\ntwo", []);
    const internals = view as unknown as ViewportInternals;
    const { viewState } = internals;
    view.noteScrollJump();
    expect(viewState.scrollJump).toBe(true);
    internals.measure();
    expect(viewState.scrollJump).toBe(false);
  });
});

describe("CodeMirror scroll measurement", () => {
  function scrolled(target: EditorView, timings: number[]) {
    const internals = target as unknown as ViewportInternals;
    internals.observer.intersecting = true;
    let top = 0;
    Object.defineProperty(target.scrollDOM, "scrollTop", { get: () => top, configurable: true });
    return {
      internals,
      scrollTo(next: number) {
        top = next;
        internals.observer.onScrollChanged({ target: target.scrollDOM });
        return timings.length;
      },
    };
  }

  /** jsdom lays nothing out; give the content a page-sized box so a measure has geometry to read. */
  function layOut(target: EditorView): void {
    const page = { x: 0, y: 0, left: 0, top: 0, right: 800, bottom: 40_000, width: 800, height: 40_000, toJSON: () => ({}) };
    target.contentDOM.getBoundingClientRect = () => page;
  }

  it("measures once per scroll offset", () => {
    const timings: number[] = [];
    view = mount("one\ntwo", [EditorView.measureTiming.of((ms) => timings.push(ms))]);
    const { scrollTo } = scrolled(view, timings);
    const first = scrollTo(0);
    expect(first).toBeGreaterThan(0);
    // The native scroll event after a pre-paint notification has nothing new to measure.
    expect(scrollTo(0)).toBe(first);
  });

  it("measures synchronously at most once per animation frame", () => {
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => frames.push(callback));
    try {
      const timings: number[] = [];
      view = mount("one\ntwo", [EditorView.measureTiming.of((ms) => timings.push(ms))]);
      const { internals, scrollTo } = scrolled(view, timings);
      const first = scrollTo(10);
      // A later scroll in the same frame shares the next frame's measurement.
      expect(scrollTo(20)).toBe(first);
      expect(internals.measureScheduled).toBeGreaterThan(-1);
      frames.splice(0).forEach((frame) => frame(0));
      expect(scrollTo(30)).toBeGreaterThan(first);
    } finally {
      raf.mockRestore();
    }
  });

  it("reads the whole document only while printing", () => {
    view = mount("one\ntwo", []);
    const internals = view as unknown as ViewportInternals;
    layOut(view);
    internals.measure();
    expect(internals.viewState.pixelViewport.bottom).toBeLessThanOrEqual(window.innerHeight);
    internals.viewState.printing = true;
    internals.measure();
    expect(internals.viewState.pixelViewport.bottom).toBeGreaterThan(39_000);
  });
});

describe("CodeMirror gutter cells", () => {
  it("keep each line's marker DOM through an edit above it", () => {
    view = mount("a\nb\nc");
    const before = cells(view);
    view.dispatch({ changes: { from: 0, insert: "z\n" } });
    const after = cells(view);
    expect(after.map((cell) => cell.textContent)).toEqual(["z", "a", "b", "c"]);
    after.slice(1).forEach((cell, i) => expect(cell).toBe(before[i]));
  });

  it("keep the lines a moved viewport still shows", () => {
    view = mount(Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join("\n"));
    const number = (cell: HTMLElement) => Number(cell.textContent!.slice(5));
    const before = cells(view);
    const last = number(before.at(-1)!);
    const kept = before.at(-5)!;
    view.dispatch({ effects: EditorView.scrollIntoView(view.state.doc.line(last + 10).from) });
    const after = cells(view);
    expect(number(after.at(-1)!)).toBeGreaterThanOrEqual(last + 10);
    expect(after).toContain(kept);
    expect(after.map(number)).toEqual(after.map((_, i) => number(after[0]!) + i));
  });
});

describe("CodeMirror cursor presentation", () => {
  it("suppresses empty cursors when drawCursor is false", () => {
    view = mount("hello world", [drawSelection({ drawCursor: false })]);
    view.dispatch({ selection: EditorSelection.cursor(0) });
    const cursors = view.dom.querySelectorAll(".cm-cursor");
    expect(cursors.length).toBe(0);
  });

  it("disables cursor blinking animation when cursorBlinkRate is 0", () => {
    view = mount("hello world", [drawSelection({ drawCursor: true, cursorBlinkRate: 0 })]);
    const cursorLayer = view.dom.querySelector<HTMLElement>(".cm-cursorLayer");
    expect(cursorLayer?.style.animationName).toBe("none");
  });

  it("clamps empty cursor height to defaultLineHeight when measured pos is tall", () => {
    view = mount("hello world", [drawSelection({ drawCursor: true })]);
    view.coordsAtPos = () => ({
      left: 10,
      right: 10,
      top: 0,
      bottom: 150,
    });
    const markers = RectangleMarker.forRange(view, "cm-cursor", EditorSelection.cursor(0));
    expect(markers).toHaveLength(1);
    expect((markers[0] as unknown as { height: number }).height).toBe(view.defaultLineHeight);
  });
});
