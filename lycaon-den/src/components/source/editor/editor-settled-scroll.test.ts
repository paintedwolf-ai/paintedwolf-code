import { EditorState, type StateEffect } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { keepPositionInView, preserveScrollAnchor, SETTLED_SCROLL_REPEATS } from "./editor-settled-scroll.ts";

type Measure = { read: (view: EditorView) => number | null; write: (value: number | null, view: EditorView) => void };

const PORT = { top: 0, bottom: 600, height: 600 };

/**
 * A view whose target sits at `targetAt(scrollTop)` in document coordinates,
 * standing in for an editor whose height estimates move with the drawn window.
 */
function fakeView(targetAt: (scrollTop: number) => number | null) {
  const measures: Measure[] = [];
  const dispatched: StateEffect<unknown>[][] = [];
  const scrollDOM = { scrollTop: 0, getBoundingClientRect: () => PORT };
  const view = {
    state: EditorState.create({ doc: "x".repeat(100) }),
    dom: { isConnected: true },
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

  it("does nothing once the view is gone", () => {
    const fake = fakeView(() => 100_000);
    (fake.view.dom as { isConnected: boolean }).isConnected = false;
    keepPositionInView(fake.view, () => 0);
    settle(fake);
    expect(fake.measures).toHaveLength(0);
    expect(fake.scrollDOM.scrollTop).toBe(0);
  });
});

/** A view whose position `p` sits at `layout.height * p` in document coordinates. */
function reflowingView(head: number, scrollTop: number) {
  const layout = { height: 20 };
  const fake = fakeView(() => null);
  fake.scrollDOM.scrollTop = scrollTop;
  const view = fake.view as unknown as Record<string, unknown>;
  view.state = EditorState.create({ doc: "x".repeat(100), selection: { anchor: head } });
  view.contentDOM = { getBoundingClientRect: () => ({ left: 40 }) };
  view.coordsAtPos = (pos: number) => {
    const top = layout.height * pos - fake.scrollDOM.scrollTop;
    return { top, bottom: top + layout.height, left: 40, right: 50 };
  };
  view.posAtCoords = ({ y }: { y: number }) => Math.floor((y + fake.scrollDOM.scrollTop) / layout.height);
  const at = (pos: number) => layout.height * pos - fake.scrollDOM.scrollTop;
  return { ...fake, layout, at };
}

describe("preserving the reader's place across a reflow", () => {
  it("keeps the text at the top edge when the selection head is off screen", () => {
    // The head is on the first line, far above the window.
    const fake = reflowingView(0, 910);
    preserveScrollAnchor(fake.view, () => { fake.layout.height = 40; });
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

  it("leaves a view at the top where it is", () => {
    const fake = reflowingView(50, 0);
    let applied = false;
    preserveScrollAnchor(fake.view, () => { applied = true; fake.layout.height = 40; });
    expect(applied).toBe(true);
    expect(frames).toHaveLength(0);
    expect(fake.scrollDOM.scrollTop).toBe(0);
  });
});
