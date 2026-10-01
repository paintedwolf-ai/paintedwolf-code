// @vitest-environment jsdom
import { EditorView, GutterMarker, gutter, drawSelection, RectangleMarker } from "@codemirror/view";
import { EditorSelection, type Extension } from "@codemirror/state";
import { afterEach, describe, expect, it } from "vitest";

type Range = { from: number; to: number };
type ViewportInternals = {
  measure(): void;
  viewState: {
    pixelViewport: { left: number; right: number; top: number; bottom: number };
    scrollDelta: number;
    scrollJump: boolean;
    scrollSettled: boolean;
    getViewport(bias: number, scrollTarget: null): Range;
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
