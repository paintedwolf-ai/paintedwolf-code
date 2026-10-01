// @vitest-environment jsdom
import { createEffect, createRoot, createSignal } from "solid-js";
import { applyProductTextScale, applyTextScale, resetTextScaleForTests } from "../../../platform/desktop/accessibility-text-size.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createTranscriptGeometry } from "./transcript-geometry.ts";
import { DEFAULT_TRANSCRIPT_SPACING, setTranscriptSpacing } from "./transcript-spacing.ts";
import type { MeasuredTranscriptVirtualizer } from "./transcript-virtualizer.ts";

const resize = vi.hoisted(() => ({ listener: undefined as ((box: { width: number; height: number }) => void) | undefined }));
vi.mock("../../../layout/shared-resize-observer.ts", () => ({
  observeSharedContentBox: (_element: Element, listener: (box: { width: number; height: number }) => void) => {
    resize.listener = listener;
    return () => { resize.listener = undefined; };
  },
}));
const disposals: (() => void)[] = [];
afterEach(() => {
  for (const stop of disposals.splice(0)) stop();
  resetTextScaleForTests();
  setTranscriptSpacing(DEFAULT_TRANSCRIPT_SPACING);
  vi.restoreAllMocks();
  document.body.replaceChildren();
});
function fixture() {
  const root = document.createElement("section");
  root.style.fontSize = "14px";
  document.body.append(root);
  const geometry = createRoot((dispose) => {
    disposals.push(dispose);
    return createTranscriptGeometry({ viewport: () => null, root: () => root, enabled: () => true });
  });
  return { root, geometry };
}
describe("transcript column geometry", () => {
  it("publishes one coherent geometry for accessibility and product scale changes at fixed width", async () => {
    const f = fixture();
    resize.listener!({ width: 600, height: 800 });
    await Promise.resolve();
    const snapshots: { key?: string; body?: number }[] = [];
    createRoot((dispose) => {
      disposals.push(dispose);
      createEffect(() => snapshots.push({ key: f.geometry.key(), body: f.geometry.metrics()?.bodyPx }));
    });
    const initial = f.geometry.key();
    applyTextScale(1.25);
    f.root.style.fontSize = "17.5px";
    expect(f.geometry.key()).toBe(initial);
    await Promise.resolve();
    expect(snapshots).toHaveLength(2);
    expect(snapshots[1]?.body).toBe(17.5);
    expect(snapshots[1]?.key).not.toBe(initial);
    applyProductTextScale(1.2);
    f.root.style.fontSize = "21px";
    expect(f.geometry.key()).toBe(snapshots[1]?.key);
    await Promise.resolve();
    expect(snapshots).toHaveLength(3);
    expect(snapshots[2]?.body).toBe(21);
    expect(snapshots[2]?.key).not.toBe(snapshots[1]?.key);
  });
  it("does not schedule new reads when measured row objects retain their index", () => {
    const queueMeasurement = vi.fn();
    const forgetMeasurement = vi.fn();
    const row = document.createElement("div");
    const [item, setItem] = createSignal({ index: 4, size: 100 });
    createRoot((dispose) => {
      disposals.push(dispose);
      const geometry = createTranscriptGeometry({ viewport: () => null, root: () => undefined, enabled: () => true });
      geometry.attach({ queueMeasurement, forgetMeasurement, remeasureMounted: vi.fn() } as unknown as MeasuredTranscriptVirtualizer);
      geometry.bindRow(row, () => item().index);
    });
    expect(queueMeasurement).toHaveBeenCalledTimes(1);
    for (let n = 0; n < 100; n++) setItem({ index: 4, size: 101 + n });
    expect(queueMeasurement).toHaveBeenCalledTimes(1);
    setItem({ index: 5, size: 200 });
    expect(queueMeasurement).toHaveBeenCalledTimes(2);
    expect(row.dataset.index).toBe("5");
    disposals.pop()!();
    expect(forgetMeasurement).toHaveBeenCalledExactlyOnceWith(row);
  });
  it("does not read styles again when only transcript height changes", () => {
    const f = fixture();
    const style = vi.spyOn(window, "getComputedStyle");
    resize.listener!({ width: 600, height: 800 });
    const key = f.geometry.key();
    const reads = style.mock.calls.length;
    expect(reads).toBe(2);
    for (let n = 0; n < 100; n++) resize.listener!({ width: 600, height: 801 + n });
    expect(style).toHaveBeenCalledTimes(reads);
    expect(f.geometry.key()).toBe(key);
    resize.listener!({ width: 500, height: 1000 });
    expect(f.geometry.key()).not.toBe(key);
    expect(style).toHaveBeenCalledTimes(reads + 2);
  });
  it("retains content metrics for gap changes and refreshes them for interior spacing", () => {
    const f = fixture();
    resize.listener!({ width: 600, height: 800 });
    const metrics = f.geometry.metrics();
    const key = f.geometry.key();
    setTranscriptSpacing({ ...DEFAULT_TRANSCRIPT_SPACING, rowGap: 25 });
    expect(f.geometry.metrics()).toBe(metrics);
    expect(f.geometry.key()).toBe(key);
    setTranscriptSpacing({ ...DEFAULT_TRANSCRIPT_SPACING, paragraphGap: 1.5 });
    expect(f.geometry.metrics()).not.toBe(metrics);
    expect(f.geometry.metrics()?.spacing?.paragraphGap).toBe(1.5);
    expect(f.geometry.key()).not.toBe(key);
  });
});
