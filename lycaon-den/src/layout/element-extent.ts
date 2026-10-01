import {
  createMemo,
  createRenderEffect,
  createSignal,
  onCleanup,
  type Accessor,
} from "solid-js";
import { observeSharedContentBox } from "./shared-resize-observer.ts";
/** Observed content-box extent of one element, in CSS pixels. */
export type ElementExtent = {
  width: Accessor<number | undefined>;
  height: Accessor<number | undefined>;
};

/** Tracks a content box without triggering layout reads on demand. */
export function observeElementExtent(
  element: Accessor<HTMLElement | undefined>,
): ElementExtent {
  const [box, setBox] = createSignal<{ width: number; height: number }>();

  createRenderEffect(() => {
    const el = element();
    setBox(undefined);
    if (!el) return;
    if (typeof ResizeObserver === "undefined") {
      setBox({ width: el.clientWidth, height: el.clientHeight });
      return;
    }
    onCleanup(observeSharedContentBox(el, (next) => setBox(next)));
  });

  return { width: createMemo(() => box()?.width), height: createMemo(() => box()?.height) };
}
