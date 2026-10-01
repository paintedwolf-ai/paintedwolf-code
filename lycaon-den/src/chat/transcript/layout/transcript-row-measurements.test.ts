// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createTranscriptRowMeasurements } from "./transcript-row-measurements.ts";
import { flushScrollportFrameForTests } from "../../../platform/scrolling/scrollport-frame.ts";

const disposals: (() => void)[] = [];
afterEach(() => { for (const stop of disposals.splice(0)) stop(); vi.unstubAllGlobals(); document.body.replaceChildren(); });
function fixture() {
  let deliver!: ResizeObserverCallback;
  const observe = vi.fn();
  const constructed = vi.fn();
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: ResizeObserverCallback) { constructed(); deliver = callback; }
    observe = observe;
    unobserve() {}
    disconnect() {}
  });
  const viewport = document.createElement("div");
  const rows = [0, 1, 2].map((index) => {
    const row = document.createElement("div");
    row.dataset.index = String(index);
    row.dataset.key = `row-${index}`;
    viewport.append(row);
    return row;
  });
  document.body.append(viewport);
  let generation = "a";
  const trace: string[] = [];
  const read = vi.fn((element: Element, entry?: ResizeObserverEntry) => {
    trace.push(`read-${(element as HTMLElement).dataset.index}`);
    return entry?.borderBoxSize[0]?.blockSize ?? 100;
  });
  const publish = vi.fn(() => { trace.push("publish"); });
  const measurements = createTranscriptRowMeasurements({
    viewport: () => viewport,
    identity: (element) => ({ key: (element as HTMLElement).dataset.key!, index: Number((element as HTMLElement).dataset.index) }),
    generation: () => generation, read, publish,
  });
  disposals.push(measurements.dispose);
  const resize = (...targets: HTMLElement[]) => deliver(targets.map((target) => ({ target,
    borderBoxSize: [{ blockSize: 110.125, inlineSize: 500 }] } as unknown as ResizeObserverEntry)), {} as ResizeObserver);
  return { measurements, viewport, rows, trace, read, publish, observe, constructed, resize, changeGeneration: () => { generation = "b"; } };
}
describe("transcript row measurement coordinator", () => {
  it("deduplicates dirty rows and reads every box before publishing", () => {
    const f = fixture();
    expect(f.constructed).not.toHaveBeenCalled();
    for (const row of f.rows) { f.measurements.queue(row); f.measurements.queue(row); }
    flushScrollportFrameForTests(f.viewport);
    expect(f.trace).toEqual(["read-0", "read-1", "read-2", "publish"]);
    expect(f.constructed).toHaveBeenCalledOnce();
    expect(f.observe).toHaveBeenCalledTimes(3);
    expect(f.observe).toHaveBeenCalledWith(f.rows[0], { box: "border-box" });
  });
  it("uses delivered border boxes immediately and retires queued reads", () => {
    const f = fixture();
    f.rows.forEach(f.measurements.queue);
    f.resize(...f.rows);
    expect(f.publish).toHaveBeenCalledExactlyOnceWith([0, 1, 2].map((index) => ({ index, size: 110.125 })));
    flushScrollportFrameForTests(f.viewport);
    expect(f.read).toHaveBeenCalledTimes(3);
  });
  it("does not attribute an old observation to another row or layout", () => {
    const f = fixture();
    f.measurements.queue(f.rows[0]!);
    f.rows[0]!.dataset.key = "replacement";
    f.resize(f.rows[0]!);
    expect(f.read).not.toHaveBeenCalled();
    flushScrollportFrameForTests(f.viewport);
    expect(f.read).toHaveBeenLastCalledWith(f.rows[0], undefined);
    f.read.mockClear();
    f.changeGeneration();
    f.resize(f.rows[0]!);
    expect(f.read).not.toHaveBeenCalled();
    flushScrollportFrameForTests(f.viewport);
    expect(f.read).toHaveBeenCalledOnce();
  });
  it("drops disconnected rows and cancels all pending work on disposal", () => {
    const f = fixture();
    f.measurements.queue(f.rows[0]!);
    f.rows[0]!.remove();
    flushScrollportFrameForTests(f.viewport);
    expect(f.read).not.toHaveBeenCalled();
    f.measurements.queue(f.rows[1]!);
    f.measurements.dispose();
    flushScrollportFrameForTests(f.viewport);
    f.resize(f.rows[1]!);
    expect(f.read).not.toHaveBeenCalled();
  });
});
