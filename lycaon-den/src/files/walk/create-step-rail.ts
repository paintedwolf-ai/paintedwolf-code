import { createEffect, createMemo, createSignal, on, onCleanup, untrack, type Accessor } from "solid-js";
import { glideScrollLeft } from "../../platform/scrolling/scrollport-reveal.ts";
import { STEP_RAIL_EDGE, stepRailLayout, stepRailPosition, stepRailReveal, stepRailWindow } from "./step-rail-layout.ts";

/** Browsing the rail leaves the selected walk step unchanged. */
export function createStepRail(options: {
  count: Accessor<number>;
  at: Accessor<number>;
  selection: Accessor<string>;
  focused: Accessor<number | null>;
  onScroll: () => void;
}) {
  const [width, setWidth] = createSignal(0);
  const [offset, setOffset] = createSignal(0);
  const layout = createMemo(() => stepRailLayout(options.count(), width()));
  const window = createMemo(() => stepRailWindow(layout(), offset()));
  const indices = createMemo(() => {
    const { first, end } = window();
    const visible = new Set(Array.from({ length: end - first }, (_, i) => first + i));
    // Keep the sequential tab stop and any held focus mounted while browsing.
    for (const index of [options.at(), options.focused()]) {
      if (index !== null && index >= 0 && index < options.count()) visible.add(index);
    }
    return [...visible].sort((a, b) => a - b);
  });
  let element: HTMLDivElement | undefined;
  let observer: ResizeObserver | undefined;
  let frame: number | undefined;
  let previous = layout();

  let cancelGlide: (() => void) | undefined;
  const scrollTo = (left: number, animate = false) => {
    const rail = element;
    if (!rail) return;
    const target = Math.max(0, Math.min(layout().maxScroll, left));
    cancelGlide?.();
    cancelGlide = undefined;
    const publish = (next: number) => {
      rail.scrollLeft = next;
      setOffset(next);
    };
    if (animate) cancelGlide = glideScrollLeft(rail, target, publish);
    else publish(target);
  };
  const reveal = (index = options.at(), animate = true) => {
    if (index < 0 || width() <= 0) return;
    const left = element?.scrollLeft ?? offset();
    const target = stepRailReveal(layout(), left, index);
    // A distant jump lands immediately; adjacent steps can ease into view.
    scrollTo(target, animate && Math.abs(target - left) < width());
  };
  createEffect(on(layout, (next) => {
    const left = untrack(offset);
    const selectedX = stepRailPosition(previous, untrack(options.at));
    const wasVisible = selectedX >= left && selectedX <= left + previous.width;
    const anchor = previous.pitch > 0 ? (left - STEP_RAIL_EDGE) / previous.pitch : 0;
    scrollTo(previous.maxScroll > 0 ? STEP_RAIL_EDGE + anchor * next.pitch : 0);
    if (previous.width === 0 || (next.width !== previous.width && wasVisible)) reveal(untrack(options.at), false);
    previous = next;
  }));
  const selection = createMemo(options.selection);
  createEffect(on(selection, () => reveal()));

  const attach = (el: HTMLDivElement) => {
    element = el;
    setWidth(el.clientWidth);
    observer?.disconnect();
    if (typeof ResizeObserver === "undefined") return;
    observer = new ResizeObserver(() => {
      if (frame !== undefined) return;
      frame = requestAnimationFrame(() => {
        frame = undefined;
        setWidth(el.clientWidth);
      });
    });
    observer.observe(el);
  };
  onCleanup(() => {
    cancelGlide?.();
    observer?.disconnect();
    if (frame !== undefined) cancelAnimationFrame(frame);
  });
  return {
    attach, layout, window, indices, offset, reveal,
    position: (index: number) => stepRailPosition(layout(), index),
    onScroll: () => {
      // A resize can clamp native scrollLeft before its observer runs.
      if (element?.clientWidth !== width()) return;
      setOffset(Math.max(0, Math.min(layout().maxScroll, element?.scrollLeft ?? 0)));
      options.onScroll();
    },
    browse: (direction: number) => scrollTo((element?.scrollLeft ?? offset()) + direction * Math.max(layout().pitch, width() * 0.75), true),
    seek: (clientX: number) => {
      if (!element || layout().pitch <= 0) return 0;
      return Math.max(0, Math.min(options.count() - 1, Math.round(
        (clientX - element.getBoundingClientRect().left + element.scrollLeft - STEP_RAIL_EDGE) / layout().pitch,
      )));
    },
  };
}
