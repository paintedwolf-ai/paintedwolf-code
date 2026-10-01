import {
  Show,
  createMemo,
  createSignal,
  onCleanup,
  createEffect,
  onMount,
} from "solid-js";
import { Portal } from "solid-js/web";
import {
  editorWalkControlsPlacement,
  saveEditorWalkControlsLocation,
} from "../../settings/editor/editor-prefs.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import {
  leaveWalk,
  stepWalk,
  subscribeWalk,
  walkNewStepCount,
  walkNewTurnCount,
  walkState,
  walkToLatest,
  walkToStart,
} from "./walk-store.ts";

import { surfaceRevealDom, type SurfaceReveal } from "../../ui/surface-reveal.ts";

type Props = {
  projectId: string;
  presentation: SurfaceReveal;
  host?: HTMLElement;
  dockMount?: HTMLElement;
};

type Point = { x: number; y: number };

type Drag = {
  pointerId: number;
  origin: Point;
  pointer: Point;
  hostBounds: DOMRect;
  dockEdge: number;
};

const KEYBOARD_STEP_PX = 8;
const KEYBOARD_LARGE_STEP_PX = 24;
const EDGE_GAP_PX = 10;
const DOCK_EDGE_PX = 28;

export function WalkTransport(props: Props) {
  const [tick, setTick] = createSignal(0);
  const [position, setPosition] = createSignal<Point | null>(null);
  const [docked, setDocked] = createSignal(false);
  const [dockTarget, setDockTarget] = createSignal(false);
  const [dockInset, setDockInset] = createSignal(0);
  const [dragging, setDragging] = createSignal(false);
  let controllerEl: HTMLDivElement | undefined;
  let drag: Drag | null = null;
  let resizeFrame: number | undefined;
  let resizeObserver: ResizeObserver | undefined;
  let anchor!: HTMLSpanElement;
  const [floatingHost, setFloatingHost] = createSignal<HTMLElement>();
  onMount(() => setFloatingHost(props.host ?? anchor.parentElement ?? undefined));

  const state = createMemo(() => {
    void tick();
    return walkState(props.projectId);
  });
  const at = createMemo(() => state().at);
  const count = createMemo(() => state().walk.steps.length);
  const newSteps = createMemo(() => {
    void tick();
    return walkNewStepCount(props.projectId);
  });
  const refreshLabel = createMemo(() => {
    void tick();
    const steps = newSteps();
    if (!steps) return "No new walk steps";
    const turns = walkNewTurnCount(props.projectId);
    return `Show ${steps} new ${steps === 1 ? "step" : "steps"}` +
      (turns ? ` · ${turns} new ${turns === 1 ? "turn" : "turns"}` : "");
  });

  const revealAttrs = () => surfaceRevealDom(props.presentation);
  createEffect(() => {
    if (state().active) return;
    setDockTarget(false);
    setDragging(false);
    drag = null;
  });
  onCleanup(subscribeWalk((id) => {
    if (id === props.projectId.trim()) setTick((value) => value + 1);
  }));

  const host = () => props.host ?? floatingHost() ?? null;

  const clamped = (point: Point): Point => {
    const parent = host();
    const control = controllerEl?.firstElementChild as HTMLElement | undefined;
    if (!parent || !control) return point;
    return {
      x: Math.max(
        EDGE_GAP_PX,
        Math.min(
          parent.clientWidth - control.offsetWidth - EDGE_GAP_PX,
          point.x,
        ),
      ),
      y: Math.max(
        EDGE_GAP_PX,
        Math.min(
          parent.clientHeight - control.offsetHeight - EDGE_GAP_PX,
          point.y,
        ),
      ),
    };
  };

  createEffect(() => {
    const saved = editorWalkControlsPlacement();
    setDocked(saved.placement === "docked");
    setPosition(saved.placement === "floating" ? saved.position ?? null : null);
  });

  const saveLocation = () => {
    const held = position();
    void saveEditorWalkControlsLocation(docked()
      ? { placement: "docked" }
      : { placement: "floating", ...(held ? { position: held } : {}) });
  };
  const restoreLocation = () => {
    const saved = editorWalkControlsPlacement();
    setDocked(saved.placement === "docked");
    setPosition(saved.placement === "floating" && saved.position ? clamped(saved.position) : null);
  };

  const localPosition = (): Point => {
    const parent = host();
    const control = controllerEl?.firstElementChild as HTMLElement | undefined;
    if (!parent || !control) return { x: EDGE_GAP_PX, y: EDGE_GAP_PX };
    const parentRect = parent.getBoundingClientRect();
    const controlRect = control.getBoundingClientRect();
    return {
      x: controlRect.left - parentRect.left,
      y: controlRect.top - parentRect.top,
    };
  };

  const attachController = (element: HTMLDivElement) => {
    controllerEl = element;
    resizeObserver?.disconnect();
    queueMicrotask(() => {
      const held = position();
      if (controllerEl === element && held) setPosition(clamped(held));
    });
    if (typeof ResizeObserver === "undefined") return;
    resizeObserver = new ResizeObserver(() => {
      if (resizeFrame !== undefined) return;
      resizeFrame = requestAnimationFrame(() => {
        resizeFrame = undefined;
        const parent = host();
        if (drag && parent) {
          drag.hostBounds = parent.getBoundingClientRect();
          drag.dockEdge = props.dockMount?.getBoundingClientRect().bottom ?? drag.hostBounds.bottom;
          setDockInset(drag.hostBounds.bottom - drag.dockEdge);
        }
        const held = position();
        if (!held) return;
        const next = clamped(held);
        if (next.x !== held.x || next.y !== held.y) setPosition(next);
      });
    });
    const parent = host();
    if (parent) resizeObserver.observe(parent);
    if (element.firstElementChild) resizeObserver.observe(element.firstElementChild);
  };

  const onGripPointerDown = (event: PointerEvent) => {
    const parent = host();
    if (event.button !== 0 || !parent) return;
    event.preventDefault();
    const hostBounds = parent.getBoundingClientRect();
    const dockEdge = props.dockMount?.getBoundingClientRect().bottom ?? hostBounds.bottom;
    setDockInset(hostBounds.bottom - dockEdge);
    const origin = clamped(docked() ? localPosition() : position() ?? localPosition());
    setDocked(false);
    setPosition(origin);
    drag = {
      pointerId: event.pointerId,
      origin,
      pointer: { x: event.clientX, y: event.clientY },
      hostBounds,
      dockEdge,
    };
    setDragging(true);
    const target = event.currentTarget as HTMLButtonElement;
    queueMicrotask(() => { if (drag?.pointerId === event.pointerId) target.setPointerCapture?.(event.pointerId); });
  };

  const onGripPointerMove = (event: PointerEvent) => {
    if (!drag || drag.pointerId !== event.pointerId) return;
    event.preventDefault();
    const bounds = drag.hostBounds;
    setDockTarget(event.clientX >= bounds.left && event.clientX <= bounds.right
      && Math.abs(event.clientY - drag.dockEdge) <= DOCK_EDGE_PX);
    setDocked(false);
    setPosition(
      clamped({
        x: drag.origin.x + event.clientX - drag.pointer.x,
        y: drag.origin.y + event.clientY - drag.pointer.y,
      }),
    );
  };

  const finishDrag = (event: PointerEvent) => {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const completed = event.type === "pointerup";
    const dock = completed && dockTarget();
    drag = null;
    setDragging(false);
    setDockTarget(false);
    const target = event.currentTarget as HTMLButtonElement;
    target.releasePointerCapture?.(event.pointerId);
    if (dock) {
      setDocked(true);
      setPosition(null);
      queueMicrotask(() => target.focus({ preventScroll: true }));
    }
    if (completed) saveLocation();
    else restoreLocation();
  };

  const onGripKeyDown = (event: KeyboardEvent) => {
    const retainFocus = () => {
      const target = event.currentTarget as HTMLButtonElement;
      queueMicrotask(() => target.focus({ preventScroll: true }));
    };
    if (event.key === "Home") {
      event.preventDefault();
      setDocked(false);
      setPosition(null);
      saveLocation();
      retainFocus();
      return;
    }
    if (event.key === "End") {
      event.preventDefault();
      setDocked(true);
      setPosition(null);
      saveLocation();
      retainFocus();
      return;
    }
    const direction =
      event.key === "ArrowLeft"
        ? { x: -1, y: 0 }
        : event.key === "ArrowRight"
          ? { x: 1, y: 0 }
          : event.key === "ArrowUp"
            ? { x: 0, y: -1 }
            : event.key === "ArrowDown"
              ? { x: 0, y: 1 }
              : null;
    if (!direction) return;
    event.preventDefault();
    const step = event.shiftKey ? KEYBOARD_LARGE_STEP_PX : KEYBOARD_STEP_PX;
    const held = position() ?? localPosition();
    setDocked(false);
    setPosition(
      clamped({ x: held.x + direction.x * step, y: held.y + direction.y * step }),
    );
    saveLocation();
    retainFocus();
  };

  const customPosition = createMemo(() => {
    if (docked()) return undefined;
    const held = position();
    return held
      ? {
          left: `${held.x}px`,
          top: `${held.y}px`,
        }
      : undefined;
  });

  onCleanup(() => {
    if (resizeFrame !== undefined) cancelAnimationFrame(resizeFrame);
    resizeObserver?.disconnect();
  });

  return (
    <>
      <span ref={anchor} style={{ display: "none" }} aria-hidden="true" />
      <Show when={floatingHost()}>
        <Portal mount={docked() ? props.dockMount ?? floatingHost() : floatingHost()} ref={element => { element.style.display = "contents"; }}>
          <Show when={state().active}>
            <Show when={dockTarget()}><div class="den-walk-dock-target" style={{ bottom: `${dockInset()}px` }} aria-hidden="true" /></Show>
            <div
              class="den-walk-transport"
              {...revealAttrs()}
              ref={attachController}
              classList={{
                ...revealAttrs().classList,
                "den-walk-transport--positioned": position() !== null,
                "den-walk-transport--docked": docked(),
                "den-walk-transport--dragging": dragging(),
              }}
              style={customPosition()}
              data-testid="walk-transport"
            >
              <div
                class="den-walk-transport__unit"
                role="group"
                aria-label="Walk controls"
                onKeyDown={(event) => {
                  if (event.key === "Escape") {
                    event.preventDefault();
                    leaveWalk(props.projectId);
                  }
                }}
              >
                <button
                  type="button"
                  class="den-walk-transport__grip"
                  data-testid="walk-transport-grip"
                  aria-label="Move walk controls"
                  aria-description="Drag to move; drop at the bottom edge to dock. End docks; Home returns to the floating position."
                  aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight Home End"
                  onPointerDown={onGripPointerDown}
                  onPointerMove={onGripPointerMove}
                  onPointerUp={finishDrag}
                  onPointerCancel={finishDrag}
                  onLostPointerCapture={() => {
                    if (!drag) return;
                    drag = null;
                    setDragging(false);
                    setDockTarget(false);
                    restoreLocation();
                  }}
                  onKeyDown={onGripKeyDown}
                >
                  <ThemeIcon slot="grip" size={14} />
                </button>
                <span class="den-walk-transport__divider" aria-hidden="true" />
                <button
                  type="button"
                  class="den-walk-transport__button"
                  data-testid="walk-transport-first"
                  data-tip="First step"
                  data-tip-pos="above"
                  aria-label="First step"
                  disabled={at() <= 0}
                  onClick={() => walkToStart(props.projectId)}
                >
                  <ThemeIcon slot="walk-first" size={14} />
                </button>
                <button
                  type="button"
                  class="den-walk-transport__button"
                  data-testid="walk-transport-prev"
                  data-tip="Previous step"
                  data-tip-pos="above"
                  aria-label="Previous step"
                  disabled={at() <= 0}
                  onClick={() => stepWalk(props.projectId, -1)}
                >
                  <ThemeIcon slot="walk-previous" size={14} />
                </button>
                <button
                  type="button"
                  class="den-walk-transport__button"
                  data-testid="walk-transport-next"
                  data-tip="Next step"
                  data-tip-pos="above"
                  aria-label="Next step"
                  disabled={count() === 0 || at() >= count() - 1}
                  onClick={() => stepWalk(props.projectId, 1)}
                >
                  <ThemeIcon slot="walk-next" size={14} />
                </button>
                <button
                  type="button"
                  class="den-walk-transport__button"
                  data-testid="walk-transport-last"
                  data-tip="Latest step"
                  data-tip-pos="above"
                  aria-label="Latest step"
                  disabled={count() === 0 || at() >= count() - 1}
                  onClick={() => walkToLatest(props.projectId)}
                >
                  <ThemeIcon slot="walk-latest" size={14} />
                </button>
                <button
                  type="button"
                  class="den-walk-transport__button den-walk-transport__refresh"
                  data-testid="walk-transport-refresh"
                  data-tip={refreshLabel()}
                  data-tip-pos="above"
                  aria-label={refreshLabel()}
                  disabled={newSteps() === 0}
                  onClick={() => walkToLatest(props.projectId)}
                >
                  <ThemeIcon slot="walk-refresh" size={14} />
                </button>
                <span class="den-walk-transport__divider" aria-hidden="true" />
                <button
                  type="button"
                  class="den-walk-transport__button den-walk-transport__close"
                  data-testid="walk-transport-close"
                  data-tip="Close walk (Esc)"
                  data-tip-pos="above"
                  aria-label="Close walk"
                  onClick={() => leaveWalk(props.projectId)}
                >
                  <ThemeIcon slot="dismiss" size={13} />
                </button>
              </div>
            </div>
          </Show>
        </Portal>
      </Show>
    </>
  );
}
