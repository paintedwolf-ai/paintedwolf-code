import { createSignal, onCleanup } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { startPointerResize } from "../../layout/pointer-resize.ts";
import type { ResizeSession } from "../../layout/resize-session.ts";

export type ResizeAxis = "width" | "height";

export type ResizeHandleProps = {
  /** Axis, or `"auto"` to read CSS and then handle geometry. */
  axis: ResizeAxis | "auto";
  /** Pointer direction that grows the pane. */
  direction?: 1 | -1;
  size: (axis: ResizeAxis) => number;
  clamp: (value: number, axis: ResizeAxis) => number;
  /** Starts one resize session, which the handle commits or cancels. */
  onBegin: (axis: ResizeAxis, startSize: number) => ResizeSession;
  onReset?: (axis: ResizeAxis) => void;
  ariaLabel: string;
  ariaMin: (axis: ResizeAxis) => number;
  ariaMax: (axis: ResizeAxis) => number;
  /** Hover text rendered by the shared tooltip host. */
  tip?: string;
  class?: string;
  hidden?: boolean;
  /** Class applied to `<html>` while dragging. */
  rootClass?: string;
  testId?: string;
  /** Arrow-key increment in px (default 16). */
  keyStep?: number;
};

/** Keyboard and pointer resize handle with interruption cleanup. */
export function ResizeHandle(props: ResizeHandleProps) {
  const [measuredAxis, setMeasuredAxis] = createSignal<ResizeAxis>(
    props.axis === "auto" ? "width" : props.axis,
  );

  const axisOf = (el: HTMLElement): ResizeAxis => {
    if (props.axis !== "auto") return props.axis;
    const declared = getComputedStyle(el)
      .getPropertyValue("--den-resize-axis")
      .trim();
    if (declared === "width" || declared === "height") return declared;
    const rect = el.getBoundingClientRect();
    return rect.width > rect.height ? "height" : "width";
  };

  const activeAxis = () => (props.axis === "auto" ? measuredAxis() : props.axis);
  const direction = () => props.direction ?? 1;
  const keyStep = () => props.keyStep ?? 16;

  let handleEl: HTMLElement | undefined;
  let cancelActive: (() => void) | undefined;

  // Refresh the CSS-selected axis before interaction.
  const syncAxis = () => {
    if (props.axis !== "auto" || !handleEl) return;
    setMeasuredAxis(axisOf(handleEl));
  };

  const attachMeasure = (el: HTMLElement) => {
    handleEl = el;
    if (props.axis !== "auto") return;
    syncAxis();
    // Container-query layout may settle after mount.
    const settle = setTimeout(syncAxis, 0);
    onCleanup(() => clearTimeout(settle));
    if (typeof ResizeObserver === "undefined") return;
    // A split drag can flip the axis without a window resize.
    const observer = new ResizeObserver(syncAxis);
    observer.observe(el);
    onCleanup(() => observer.disconnect());
  };

  const onPointerDown = (e: PointerEvent) => {
    if (e.button !== 0) return;
    e.preventDefault();
    cancelActive?.();
    const handle = e.currentTarget as HTMLElement;
    const axis = axisOf(handle);
    setMeasuredAxis(axis);

    const start = axis === "width" ? e.clientX : e.clientY;
    const startSize = props.size(axis);
    const session = props.onBegin(axis, startSize);

    const sizeAt = (position: number) =>
      props.clamp(startSize + (position - start) * direction(), axis);
    cancelActive = startPointerResize({
      handle,
      pointerId: e.pointerId,
      startPosition: start,
      positionOf: (event) =>
        axis === "width" ? event.clientX : event.clientY,
      valueAt: sizeAt,
      session,
      rootClass: props.rootClass,
    });
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const axis = activeAxis();
    const grow = axis === "width" ? "ArrowRight" : "ArrowDown";
    const shrink = axis === "width" ? "ArrowLeft" : "ArrowUp";
    let delta = 0;
    if (e.key === grow) delta = keyStep();
    else if (e.key === shrink) delta = -keyStep();
    else if (e.key === "Enter" || e.key === " ") {
      if (!props.onReset) return;
      e.preventDefault();
      props.onReset(axis);
      return;
    } else return;
    e.preventDefault();
    const next = props.clamp(props.size(axis) + delta * direction(), axis);
    const session = props.onBegin(axis, props.size(axis));
    session.preview(next);
    session.commit();
  };

  const onDoubleClick = (e: MouseEvent) => {
    if (!props.onReset) return;
    e.preventDefault();
    e.stopPropagation();
    props.onReset(activeAxis());
  };

  onCleanup(() => cancelActive?.());

  return (
    <div
      ref={attachMeasure}
      class={cn("den-resize-handle", props.class)}
      classList={{
        "den-resize-handle--width": activeAxis() === "width",
        "den-resize-handle--height": activeAxis() === "height",
      }}
      role="separator"
      tabindex={0}
      aria-orientation={activeAxis() === "width" ? "vertical" : "horizontal"}
      aria-label={props.ariaLabel}
      aria-valuemin={props.ariaMin(activeAxis())}
      aria-valuemax={props.ariaMax(activeAxis())}
      aria-valuenow={props.size(activeAxis())}
      data-testid={props.testId}
      data-tip={props.tip}
      hidden={props.hidden}
      onPointerEnter={syncAxis}
      onFocus={syncAxis}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      onDblClick={onDoubleClick}
    />
  );
}
