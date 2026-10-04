// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { EditorView } from "@codemirror/view";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../../platform/scrolling/scrollport-motion.ts";
import { bindEditorScrollPaint } from "./editor-scroll-paint.ts";
import { editorScrollPosition, readEditorScrollPosition } from "./editor-scroll-position.ts";

describe("editor scroll paint", () => {
  it("measures at each input offset before the frame paints and releases the subscription", () => {
    const view = new EditorView({ doc: "one\ntwo\n", extensions: editorScrollPosition });
    Object.defineProperties(view.scrollDOM, {
      clientHeight: { value: 100 },
      scrollHeight: { value: 10_000 },
      clientWidth: { value: 100 },
      scrollWidth: { value: 10_000 },
    });
    const motion = bindScrollportMotion(view.dom, view.scrollDOM, view.scrollDOM);
    const stop = bindEditorScrollPaint(view);
    const painted: number[][] = [];
    const internals = view as unknown as { measure(): void };
    const measure = internals.measure.bind(view);
    vi.spyOn(internals, "measure").mockImplementation(() => {
      painted.push([view.scrollDOM.scrollLeft, view.scrollDOM.scrollTop]);
      measure();
    });
    const noteScrollJump = vi.spyOn(view, "noteScrollJump");
    const scrollbarScroll = vi.fn();
    view.scrollDOM.addEventListener("scroll", scrollbarScroll);
    try {
      motion.commit(500, "thumb_drag");
      motion.commit(700, "track_click", { axis: "x" });
      expect(painted).toEqual([[0, 500], [700, 500]]);
      expect(noteScrollJump).toHaveBeenCalledTimes(2);
      expect(scrollbarScroll).not.toHaveBeenCalled();
      motion.commit(100, "restore_anchor");
      expect(painted).toHaveLength(2);
      expect(readEditorScrollPosition(view)).toEqual({ top: 100, left: 700 });
      stop();
      motion.commit(900, "thumb_drag");
      expect(painted).toHaveLength(2);
    } finally {
      stop();
      unbindScrollportMotion(view.dom);
      view.destroy();
    }
  });
});

it("reuses a measured offset only while the document and geometry are clean", () => {
  const view = new EditorView({ doc: "one\ntwo\n" });
  type MeasurementView = {
    measure: () => void;
    measureScheduled: number;
    viewState: { mustMeasureContent: boolean | string };
    observer: { intersecting: boolean; syncMeasureFrame: boolean; onScrollChanged: (event: Event) => void };
  };
  const measured = view as unknown as MeasurementView;
  // Each synchronous measure claims the current frame; the next frame releases it.
  const nextFrame = () => { measured.observer.syncMeasureFrame = false; };
  cancelAnimationFrame(measured.measureScheduled);
  measured.measureScheduled = -1;
  measured.viewState.mustMeasureContent = false;
  measured.observer.intersecting = true;
  const measure = vi.spyOn(measured, "measure").mockImplementation(() => {
    measured.measureScheduled = -1;
    measured.viewState.mustMeasureContent = false;
  });
  const event = new Event("scroll");
  Object.defineProperty(event, "target", { value: view.scrollDOM });
  try {
    measured.observer.onScrollChanged(event);
    measured.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(1);
    view.dispatch({ changes: { from: 0, insert: "changed\n" } });
    // A second uncovered event in the same frame shares the next frame's measurement.
    measured.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(1);
    nextFrame();
    measured.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(2);
    nextFrame();
    measured.viewState.mustMeasureContent = true;
    measured.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(3);
    nextFrame();
    measured.measureScheduled = 0;
    measured.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(4);
  } finally {
    measure.mockRestore();
    view.destroy();
  }
});

it("coalesces scrolls inside the rendered runway but measures jumps and dirty content immediately", () => {
  const view = new EditorView({ doc: Array.from({ length: 1000 }, () => "line").join("\n") });
  const internal = view as unknown as {
    measure(): void;
    measureScheduled: number;
    viewState: {
      scrollParent: HTMLElement; scrollOffset: number; scaleY: number;
      pixelViewport: { top: number; bottom: number };
      viewportLines: Array<{ top: number; bottom: number }>;
      mustMeasureContent: boolean; lineGaps: unknown[];
    };
    observer: { intersecting: boolean; syncMeasureFrame: boolean; onScrollChanged(event: Event): void };
  };
  cancelAnimationFrame(internal.measureScheduled);
  internal.measureScheduled = -1;
  Object.assign(internal.viewState, {
    scrollParent: view.scrollDOM, scrollOffset: 0, scaleY: 1,
    pixelViewport: { top: 0, bottom: 400 },
    viewportLines: [{ top: 0, bottom: 2000 }], mustMeasureContent: false, lineGaps: [],
  });
  internal.observer.intersecting = true;
  const measure = vi.spyOn(internal, "measure").mockImplementation(() => {});
  const request = vi.spyOn(view, "requestMeasure");
  const event = new Event("scroll");
  Object.defineProperty(event, "target", { value: view.scrollDOM });
  try {
    // Establish the measured document and offset.
    internal.observer.onScrollChanged(event);
    measure.mockClear();
    internal.observer.syncMeasureFrame = false;
    for (const offset of [50, 100, 150]) {
      view.scrollDOM.scrollTop = offset;
      internal.observer.onScrollChanged(event);
    }
    expect(measure).not.toHaveBeenCalled();
    expect(request).toHaveBeenCalledTimes(3);
    const scheduled = internal.measureScheduled;
    expect(scheduled).toBeGreaterThanOrEqual(0);
    view.scrollDOM.scrollTop = 8000;
    internal.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledOnce();
    // A second jump inside the same frame waits for the next frame's measurement.
    view.scrollDOM.scrollTop = 16000;
    internal.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledOnce();
    expect(request).toHaveBeenCalledTimes(4);
    internal.observer.syncMeasureFrame = false;
    view.scrollDOM.scrollTop = 200;
    internal.viewState.mustMeasureContent = true;
    internal.observer.onScrollChanged(event);
    expect(measure).toHaveBeenCalledTimes(2);
  } finally {
    measure.mockRestore();
    request.mockRestore();
    view.destroy();
  }
});
