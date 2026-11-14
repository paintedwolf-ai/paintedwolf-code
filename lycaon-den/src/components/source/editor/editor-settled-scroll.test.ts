import { EditorState, type StateEffect } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isSettlingScroll, keepPositionInView, preserveScrollAnchor, SETTLED_SCROLL_REPEATS } from "./editor-settled-scroll.ts";

type Measure = { read: (view: EditorView) => number | null; write: (value: number | null, view: EditorView) => void };

const PORT = { top: 0, bottom: 600, height: 600, left: 0, right: 800 };

/**
 * A view whose target sits at `targetAt(scrollTop)` in document coordinates,
 * standing in for an editor whose height estimates move with the drawn window.
 */
function fakeView(targetAt: (scrollTop: number) => number | null) {
  const measures: Measure[] = [];
  const dispatched: StateEffect<unknown>[][] = [];
  const listeners = new Map<string, Set<() => void>>();
  const scrollDOM = {
    scrollTop: 0,
    scrollLeft: 0,
    getBoundingClientRect: () => PORT,
    addEventListener: (type: string, fn: () => void) => { listeners.set(type, (listeners.get(type) ?? new Set()).add(fn)); },
    removeEventListener: (type: string, fn: () => void) => { listeners.get(type)?.delete(fn); },
    /** Delivers reader input to the listeners. */
    fire: (type: string) => { for (const fn of [...(listeners.get(type) ?? [])]) fn(); },
  };
  const view = {
    state: EditorState.create({ doc: "x".repeat(100) }),
    dom: { isConnected: true },
    // Every position is drawn unless a test narrows this.
    viewport: { from: 0, to: 100 },
    defaultLineHeight: 20,
    scrollDOM,
    plugin: () => null,
    requestMeasure: (measure: Measure) => void measures.push(measure),
    coordsAtPos: () => {
      const at = targetAt(scrollDOM.scrollTop);
      return at === null ? null : { top: at - scrollDOM.scrollTop, bottom: at - scrollDOM.scrollTop + 20, left: 0, right: 10 };
    },
    dispatch: (spec: { effects: StateEffect<unknown> | StateEffect<unknown>[] }) => {
      dispatched.push(([] as StateEffect<unknown>[]).concat(spec.effects));
    },
  } as unknown as EditorView;
  return { view, measures, dispatched, scrollDOM };
}

let frames: Array<() => void> = [];

beforeEach(() => {
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
    frames.push(callback);
    return frames.length;
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Runs frames and their measures until none remain; returns the frames run. */
function settle(fake: ReturnType<typeof fakeView>, limit = 500): number {
  let count = 0;
  while (frames.length > 0 && count < limit) {
    const [frame] = frames.splice(0, 1);
    frame?.();
    for (let measure = fake.measures.shift(); measure; measure = fake.measures.shift()) {
      measure.write(measure.read(fake.view), fake.view);
    }
    count += 1;
  }
  return count;
}

describe("keeping a position in view", () => {
  it("stops after the target stays visible for two frames", () => {
    const fake = fakeView(() => 100);
    keepPositionInView(fake.view, () => 0);
    expect(settle(fake)).toBe(2);
    expect(fake.scrollDOM.scrollTop).toBe(0);
  });

  it("converges when moving the window shifts the estimate back and forth", () => {
    // The estimate overshoots in proportion to the distance from the drawn window.
    const truth = 50_000;
    const targetAt = (scrollTop: number) => truth + 0.6 * (truth - 150 - scrollTop);
    const fake = fakeView(targetAt);
    keepPositionInView(fake.view, () => 0);
    settle(fake);
    const at = targetAt(fake.scrollDOM.scrollTop) - fake.scrollDOM.scrollTop;
    expect(at).toBeGreaterThan(-20);
    expect(at).toBeLessThan(PORT.bottom);
  });

  it("asks the editor to scroll, after the measure, when the target has no coordinates", async () => {
    let known = false;
    const fake = fakeView(() => (known ? 100 : null));
    keepPositionInView(fake.view, () => 7);
    frames.splice(0, 1)[0]?.();
    const [measure] = fake.measures.splice(0, 1);
    measure?.write(measure.read(fake.view), fake.view);
    expect(fake.dispatched).toHaveLength(0);
    await Promise.resolve();
    expect(fake.dispatched).toHaveLength(1);
    known = true;
    settle(fake);
    expect(fake.dispatched).toHaveLength(1);
  });

  it("gives up after a fixed number of corrections", () => {
    // The target always reads a screen ahead of wherever the window is.
    const fake = fakeView((scrollTop) => scrollTop + 5_000);
    keepPositionInView(fake.view, () => 0);
    settle(fake);
    expect(frames).toHaveLength(0);
    let corrections = 0;
    let last = -1;
    const probe = fakeView((scrollTop) => {
      if (scrollTop !== last) corrections += 1;
      last = scrollTop;
      return scrollTop + 5_000;
    });
    keepPositionInView(probe.view, () => 0);
    settle(probe);
    expect(corrections).toBeLessThanOrEqual(SETTLED_SCROLL_REPEATS + 1);
  });

  it("keeps correcting while the editor is still measuring, within a frame cap", () => {
    let corrections = 0;
    let last = -1;
    const probe = fakeView((scrollTop) => {
      if (scrollTop !== last) corrections += 1;
      last = scrollTop;
      return scrollTop + 5_000;
    });
    // Each frame's measurement revises the document height.
    let height = 10_000;
    Object.defineProperty(probe.scrollDOM, "scrollHeight", { get: () => (height += 100) });
    keepPositionInView(probe.view, () => 0);
    const run = settle(probe, 2_000);
    expect(corrections).toBeGreaterThan(SETTLED_SCROLL_REPEATS + 1);
    expect(frames).toHaveLength(0);
    expect(run).toBeLessThan(2_000);
  });

  it("does nothing once the view is gone", () => {
    const fake = fakeView(() => 100_000);
    (fake.view.dom as { isConnected: boolean }).isConnected = false;
    keepPositionInView(fake.view, () => 0);
    settle(fake);
    expect(fake.measures).toHaveLength(0);
    expect(fake.scrollDOM.scrollTop).toBe(0);
  });
});

/**
 * A view whose position `p` sits at `layout.height * p` in document coordinates.
 * With `scrollLeft`, the content starts that far left of the viewport and a
 * probe left of the viewport reads position 0.
 */
function reflowingView(head: number, scrollTop: number, scrollLeft = 0) {
  // `x` moves every position sideways, as when a line stops wrapping.
  const layout = { height: 20, x: 0 };
  const fake = fakeView(() => null);
  fake.scrollDOM.scrollTop = scrollTop;
  fake.scrollDOM.scrollLeft = scrollLeft;
  const view = fake.view as unknown as Record<string, unknown>;
  view.state = EditorState.create({ doc: "x".repeat(100), selection: { anchor: head } });
  view.contentDOM = { getBoundingClientRect: () => ({ left: 40 - scrollLeft }) };
  view.coordsAtPos = (pos: number) => {
    const top = layout.height * pos - fake.scrollDOM.scrollTop;
    const left = 40 + layout.x - fake.scrollDOM.scrollLeft;
    return { top, bottom: top + layout.height, left, right: left + 10 };
  };
  view.posAtCoords = ({ x, y }: { x: number; y: number }) =>
    x < PORT.left ? 0 : Math.floor((y + fake.scrollDOM.scrollTop) / layout.height);
  const at = (pos: number) => layout.height * pos - fake.scrollDOM.scrollTop;
  return { ...fake, layout, at };
}

describe("preserving the reader's place across a reflow", () => {
  it("keeps the text at the top edge when the selection head is off screen", () => {
    // The head is on the first line, far above the window.
    const fake = reflowingView(0, 910);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    // One scroll request, ahead of the editor's own anchoring.
    expect(fake.dispatched).toHaveLength(1);
    settle(fake);
    expect(fake.at(45)).toBeLessThanOrEqual(0);
    expect(fake.at(45)).toBeGreaterThan(-40);
    expect(fake.at(46)).toBeGreaterThan(0);
  });

  it("keeps a visible selection head at the same distance from the top", () => {
    const fake = reflowingView(50, 900);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    settle(fake);
    expect(Math.abs(fake.at(50) - 100)).toBeLessThan(1);
  });

  it("keeps a selection head that is partly above the top edge", () => {
    const fake = reflowingView(50, 1010);
    expect(fake.at(50)).toBe(-10);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    settle(fake);
    expect(Math.abs(fake.at(50) + 10)).toBeLessThan(1);
  });

  it("keeps a visible selection head in a view at the top", () => {
    const fake = reflowingView(10, 0);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    settle(fake);
    expect(Math.abs(fake.at(10) - 200)).toBeLessThan(1);
  });

  it("reads the top edge inside the viewport when the content is scrolled sideways", () => {
    const fake = reflowingView(0, 910, 500);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    settle(fake);
    expect(fake.at(45)).toBeLessThanOrEqual(0);
    expect(fake.at(46)).toBeGreaterThan(0);
  });

  it("ends an earlier settle when a later one starts", () => {
    const fake = reflowingView(0, 0);
    // The first settle chases a position far below; the second's target is already in view.
    keepPositionInView(fake.view, () => 90);
    keepPositionInView(fake.view, () => 5);
    expect(isSettlingScroll(fake.view)).toBe(true);
    settle(fake);
    expect(fake.scrollDOM.scrollTop).toBe(0);
    expect(isSettlingScroll(fake.view)).toBe(false);
  });

  it("keeps the text in view when scrolled sideways at the top of the document", () => {
    const fake = reflowingView(90, 0, 500);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    expect(fake.dispatched).toHaveLength(1);
  });

  it("holds a position at its distance from the left edge too", () => {
    const fake = reflowingView(10, 100);
    preserveScrollAnchor(fake.view, () => { fake.layout.x = 5_000; });
    settle(fake);
    expect(Math.abs(40 + fake.layout.x - fake.scrollDOM.scrollLeft - 40)).toBeLessThan(1);
    expect(Math.abs(fake.at(10) - 100)).toBeLessThan(1);
  });

  it("stops correcting at a scroll bound", () => {
    // The head sits 200px down at the top of the document; after the reflow it is
    // 100px down, and the view cannot scroll above 0 to hold 200px.
    const fake = reflowingView(10, 0);
    const clamp = fake.scrollDOM as { scrollTop: number };
    let top = 0;
    Object.defineProperty(clamp, "scrollTop", { get: () => top, set: (v: number) => { top = Math.max(0, v); }, configurable: true });
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 10; });
    expect(settle(fake)).toBeLessThan(10);
    expect(top).toBe(0);
  });

  it("keeps the first change's anchor for a change made before it settles", () => {
    // Wrapping, then Pretty: the second change arrives before the first is measured.
    const fake = reflowingView(0, 910);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 10; });
    settle(fake);
    // Position 45 sat 10px above the top edge before either change.
    expect(Math.abs(fake.at(45) + 10)).toBeLessThan(1);
  });

  it("ends a correction when the reader scrolls", () => {
    const fake = reflowingView(0, 910);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
    expect(isSettlingScroll(fake.view)).toBe(true);
    fake.scrollDOM.fire("wheel");
    expect(isSettlingScroll(fake.view)).toBe(false);
    const top = fake.scrollDOM.scrollTop;
    settle(fake);
    expect(fake.scrollDOM.scrollTop).toBe(top);
  });

  it("navigates to an undrawn position by its block's height estimate", () => {
    // One 100-position line block, 2,000px tall. The editor reports undrawn text
    // at the block's top; the anchor at position 50 belongs at 1,000px.
    const fake = reflowingView(50, 900);
    const view = fake.view as unknown as Record<string, unknown>;
    const drawn = () => {
      const top = fake.scrollDOM.scrollTop;
      return { from: Math.floor(top / 20), to: Math.ceil((top + 600) / 20) };
    };
    Object.defineProperty(view, "viewport", { get: drawn });
    view.documentTop = -fake.scrollDOM.scrollTop;
    Object.defineProperty(view, "documentTop", { get: () => -fake.scrollDOM.scrollTop });
    view.lineBlockAt = () => ({ from: 0, length: 100, top: 0, height: 100 * fake.layout.height });
    const coords = view.coordsAtPos as (pos: number) => { top: number };
    view.coordsAtPos = (pos: number) => {
      const { from, to } = drawn();
      return pos >= from && pos <= to ? coords(pos) : { top: -fake.scrollDOM.scrollTop, bottom: 20 - fake.scrollDOM.scrollTop, left: 40, right: 50 };
    };
    preserveScrollAnchor(fake.view, () => { fake.scrollDOM.scrollTop = 0; });
    settle(fake);
    expect(Math.abs(fake.at(50) - 100)).toBeLessThan(1);
  });

  it("leaves a view at the top where it is", () => {
    const fake = reflowingView(50, 0);
    let applied = false;
    preserveScrollAnchor(fake.view, () => { applied = true; fake.layout.height = 40; });
    expect(applied).toBe(true);
    expect(frames).toHaveLength(0);
    expect(fake.scrollDOM.scrollTop).toBe(0);
  });
});
