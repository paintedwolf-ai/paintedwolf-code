import { Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import {
  FIRST_TIME_TIPS,
  FIRST_TIME_TIP_ROLLOUT,
  type FirstTimeTipId,
  type FirstTimeTipPlacement,
} from "../../first-time-tips/first-time-tips-catalog.ts";
import {
  clearFirstTimeTipRequest,
  requestedFirstTimeTips,
} from "../../first-time-tips/first-time-tips-service.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { isPresented } from "../../ui/presented.ts";
import {
  dismissFirstTimeTip,
  firstTimeTipsEnabledPref,
  firstTimeTipsPrefsReady,
  isFirstTimeTipDismissed,
  saveFirstTimeTipsEnabled,
} from "../../settings/system/first-time-tips-prefs.ts";
import {
  isShellLayoutUnstable,
  onShellLayoutSettled,
} from "../../shell/shell-layout-busy.ts";
import { subscribeAnyScrollActivity } from "../../platform/scrolling/scroll-activity.ts";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import { focusableElements } from "../../platform/interaction/focus-trap.ts";

type PopoutSize = { width: number; height: number };
type Position = PopoutSize & {
  left: number;
  top: number;
  placement: FirstTimeTipPlacement;
  arrowOffset: number;
};

type Viewport = { width: number; height: number };

// The gap keeps the arrow point against the target.
const POPOUT_GAP_PX = 10;
const VIEWPORT_MARGIN_PX = 8;
const ARROW_MARGIN_PX = 18;
const ESTIMATED_POPOUT_SIZE: PopoutSize = { width: 336, height: 180 };
const FIRST_TIME_TIP_ANCHOR_SELECTOR = "[data-first-time-tip-anchor]";

function samePosition(
  previous: Position | null,
  next: Position,
): boolean {
  return (
    previous !== null &&
    previous.left === next.left &&
    previous.top === next.top &&
    previous.width === next.width &&
    previous.height === next.height &&
    previous.placement === next.placement &&
    previous.arrowOffset === next.arrowOffset
  );
}

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(value, max));
}

function placementOrder(
  preferred: FirstTimeTipPlacement,
): readonly FirstTimeTipPlacement[] {
  const opposite: Record<FirstTimeTipPlacement, FirstTimeTipPlacement> = {
    top: "bottom",
    right: "left",
    bottom: "top",
    left: "right",
  };
  const perpendicular: Record<FirstTimeTipPlacement, readonly FirstTimeTipPlacement[]> = {
    top: ["right", "left"],
    right: ["bottom", "top"],
    bottom: ["right", "left"],
    left: ["bottom", "top"],
  };
  return [preferred, ...perpendicular[preferred], opposite[preferred]];
}

function candidatePosition(
  anchor: DOMRect,
  popout: PopoutSize,
  placement: FirstTimeTipPlacement,
): Pick<Position, "left" | "top"> {
  switch (placement) {
    case "top":
      return {
        left: anchor.left + anchor.width / 2 - popout.width / 2,
        top: anchor.top - POPOUT_GAP_PX - popout.height,
      };
    case "right":
      return {
        left: anchor.right + POPOUT_GAP_PX,
        top: anchor.top + anchor.height / 2 - popout.height / 2,
      };
    case "bottom":
      return {
        left: anchor.left + anchor.width / 2 - popout.width / 2,
        top: anchor.bottom + POPOUT_GAP_PX,
      };
    case "left":
      return {
        left: anchor.left - POPOUT_GAP_PX - popout.width,
        top: anchor.top + anchor.height / 2 - popout.height / 2,
      };
  }
}

function fitsViewport(
  candidate: Pick<Position, "left" | "top">,
  popout: PopoutSize,
  viewport: Viewport,
): boolean {
  return (
    candidate.left >= VIEWPORT_MARGIN_PX &&
    candidate.top >= VIEWPORT_MARGIN_PX &&
    candidate.left + popout.width <= viewport.width - VIEWPORT_MARGIN_PX &&
    candidate.top + popout.height <= viewport.height - VIEWPORT_MARGIN_PX
  );
}

const HELD_SURFACE_SELECTOR = [
  ".den-exit-fade",
  '[data-boot="pending"]',
  '.den-stage-boot:not([data-boot="ready"])',
  '[data-presentation="preparing"]',
  '[aria-busy="true"]',
  '[data-retained="true"]',
].join(", ");

const WORKSPACE_VEIL_ACTIVE_SELECTOR =
  ".den-shell-workspace-veil:not(.den-shell-workspace-veil--hidden)";

function isTipAnchorHeld(anchor: HTMLElement): boolean {
  if (anchor.closest(HELD_SURFACE_SELECTOR) !== null) {
    return true;
  }
  const doc =
    anchor.ownerDocument ??
    (typeof document !== "undefined" ? document : null);
  return doc?.querySelector(WORKSPACE_VEIL_ACTIVE_SELECTOR) != null;
}

function visibleTipAnchor(anchorId: FirstTimeTipId): HTMLElement | null {
  const anchors = document.querySelectorAll<HTMLElement>(
    `[data-first-time-tip-anchor="${anchorId}"]`,
  );
  for (const anchor of anchors) {
    if (!isPresented(anchor) || isTipAnchorHeld(anchor)) {
      continue;
    }
    const rect = anchor.getBoundingClientRect();
    if (
      rect.width > 0 &&
      rect.height > 0 &&
      rect.right > 0 &&
      rect.bottom > 0 &&
      rect.left < window.innerWidth &&
      rect.top < window.innerHeight
    ) {
      return anchor;
    }
  }
  return null;
}

function containsFirstTimeTipAnchor(node: Node): boolean {
  return (
    node instanceof Element &&
    (node.matches(FIRST_TIME_TIP_ANCHOR_SELECTOR) ||
      node.querySelector(FIRST_TIME_TIP_ANCHOR_SELECTOR) !== null)
  );
}

function isStabilityOrAnchorMutation(node: Node): boolean {
  if (!(node instanceof Element)) return false;
  return (
    containsFirstTimeTipAnchor(node) ||
    node.matches(".den-shell-workspace-veil, .den-shell-workspace-veil *") ||
    node.querySelector(".den-shell-workspace-veil") !== null
  );
}

/** Editor scrolling does not move chrome anchors. */
export function scrollCanMoveFirstTimeTipAnchor(
  target: EventTarget | null,
): boolean {
  return !(target instanceof Element && target.closest(".cm-editor") !== null);
}

function createFrameCoalescer(run: () => void): {
  schedule: () => void;
  cancel: () => void;
} {
  let frame = 0;
  return {
    schedule() {
      if (frame !== 0) return;
      if (typeof requestAnimationFrame !== "function") {
        run();
        return;
      }
      frame = requestAnimationFrame(() => {
        frame = 0;
        run();
      });
    },
    cancel() {
      if (frame === 0) return;
      cancelAnimationFrame(frame);
      frame = 0;
    },
  };
}

export function resolveFirstTimeTipPosition(
  anchor: DOMRect,
  popout: PopoutSize,
  viewport: Viewport,
  preferred: FirstTimeTipPlacement,
): Position {
  const placement = placementOrder(preferred).find((candidate) =>
    fitsViewport(candidatePosition(anchor, popout, candidate), popout, viewport),
  ) ?? preferred;
  const candidate = candidatePosition(anchor, popout, placement);
  const left = clamp(
    candidate.left,
    VIEWPORT_MARGIN_PX,
    Math.max(VIEWPORT_MARGIN_PX, viewport.width - popout.width - VIEWPORT_MARGIN_PX),
  );
  const top = clamp(
    candidate.top,
    VIEWPORT_MARGIN_PX,
    Math.max(VIEWPORT_MARGIN_PX, viewport.height - popout.height - VIEWPORT_MARGIN_PX),
  );
  const across = placement === "top" || placement === "bottom";
  const anchorCenter = across
    ? anchor.left + anchor.width / 2
    : anchor.top + anchor.height / 2;
  const popoutExtent = across ? popout.width : popout.height;
  const origin = across ? left : top;

  return {
    left,
    top,
    placement,
    arrowOffset: clamp(
      anchorCenter - origin,
      ARROW_MARGIN_PX,
      popoutExtent - ARROW_MARGIN_PX,
    ),
    ...popout,
  };
}

export function FirstTimeTipsLayer() {
  const [position, setPosition] = createSignal<Position | null>(null);
  const [tipElement, setTipElement] = createSignal<HTMLElement | null>(null);
  const [viewportVersion, setViewportVersion] = createSignal(0);
  // Scrolls that can carry an anchor; the tip waits offscreen until they settle.
  const [anchorScrolls, setAnchorScrolls] = createSignal(0);
  const activeId = createMemo<FirstTimeTipId | null>(() => {
    void viewportVersion();
    if (!firstTimeTipsPrefsReady() || !firstTimeTipsEnabledPref()) return null;
    const requested = requestedFirstTimeTips();
    return FIRST_TIME_TIP_ROLLOUT.find(
      (candidate) =>
        requested.includes(candidate) &&
        !isFirstTimeTipDismissed(candidate) &&
        visibleTipAnchor(candidate) !== null,
    ) ?? null;
  });

  onMount(() => {
    const invalidateViewport = () => {
      if (isShellLayoutUnstable()) return;
      if (!firstTimeTipsPrefsReady() || !firstTimeTipsEnabledPref()) return;
      setViewportVersion((version) => version + 1);
    };
    invalidateViewport();
    const coalescedInvalidate = createFrameCoalescer(invalidateViewport);
    window.addEventListener("resize", coalescedInvalidate.schedule);
    const stopViewportSettle = onShellLayoutSettled(invalidateViewport);
    const stopScrollActivity = subscribeAnyScrollActivity((phase, target) => {
      if (!scrollCanMoveFirstTimeTipAnchor(target)) return;
      if (phase === "start") {
        setAnchorScrolls((count) => count + 1);
        return;
      }
      setAnchorScrolls((count) => Math.max(0, count - 1));
      invalidateViewport();
    });
    const anchorObserver =
      typeof MutationObserver === "undefined"
        ? undefined
        : new MutationObserver((records) => {
            for (const record of records) {
              if (
                record.type === "attributes" &&
                isStabilityOrAnchorMutation(record.target)
              ) {
                invalidateViewport();
                return;
              }
              if (
                record.type === "childList" &&
                [...record.addedNodes, ...record.removedNodes].some(
                  isStabilityOrAnchorMutation,
                )
              ) {
                invalidateViewport();
                return;
              }
            }
          });
    anchorObserver?.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: [
        "aria-busy",
        "aria-hidden",
        "class",
        "data-boot",
        "data-first-time-tip-anchor",
        "data-presentation",
        "data-retained",
        "hidden",
        "inert",
        "style",
      ],
    });
    onCleanup(() => {
      window.removeEventListener("resize", coalescedInvalidate.schedule);
      coalescedInvalidate.cancel();
      stopViewportSettle();
      stopScrollActivity();
      anchorObserver?.disconnect();
    });
  });

  createEffect(() => {
    void viewportVersion();
    const id = activeId();
    if (!id) {
      setPosition(null);
      return;
    }
    const tip = FIRST_TIME_TIPS[id];

    const updatePosition = () => {
      if (isShellLayoutUnstable()) return;
      const target = visibleTipAnchor(id);
      if (!target) {
        setPosition(null);
        return;
      }
      const rect = target.getBoundingClientRect();
      const panel = tipElement();
      const popout = {
        width: Math.min(
          panel?.offsetWidth || ESTIMATED_POPOUT_SIZE.width,
          window.innerWidth - VIEWPORT_MARGIN_PX * 2,
        ),
        height: panel?.offsetHeight || ESTIMATED_POPOUT_SIZE.height,
      };
      const next = resolveFirstTimeTipPosition(
        rect,
        popout,
        { width: window.innerWidth, height: window.innerHeight },
        tip.preferredPlacement,
      );
      // The panel ref triggers one post-mount measurement.
      // Stable positions keep the keyed popout mounted.
      setPosition((previous) => (samePosition(previous, next) ? previous : next));
    };

    const coalescedPosition = createFrameCoalescer(updatePosition);
    queueMicrotask(updatePosition);
    const stopPositionSettle = onShellLayoutSettled(updatePosition);
    const stopScrollActivity = subscribeAnyScrollActivity((phase, target) => {
      if (phase === "settle" && scrollCanMoveFirstTimeTipAnchor(target)) {
        updatePosition();
      }
    });
    window.addEventListener("resize", coalescedPosition.schedule);
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(coalescedPosition.schedule);
    observer?.observe(document.documentElement);
    const target = visibleTipAnchor(id);
    if (target) observer?.observe(target);
    const panel = tipElement();
    if (panel) observer?.observe(panel);
    onCleanup(() => {
      window.removeEventListener("resize", coalescedPosition.schedule);
      coalescedPosition.cancel();
      stopPositionSettle();
      stopScrollActivity();
      observer?.disconnect();
    });
  });

  const dismiss = (id: FirstTimeTipId) => {
    clearFirstTimeTipRequest(id);
    void dismissFirstTimeTip(id);
  };

  const disableTips = () => {
    void saveFirstTimeTipsEnabled(false);
  };

  createAnchoredPopoverFocus(
    () => activeId() !== null,
    tipElement,
    {
      trigger: () => {
        const id = activeId();
        const anchor = id ? visibleTipAnchor(id) : null;
        const active = document.activeElement;
        if (!anchor) return null;
        if (active instanceof HTMLElement && anchor.contains(active)) {
          return active;
        }
        return focusableElements(anchor)[0] ?? anchor;
      },
      initialFocus: () =>
        tipElement()?.querySelector<HTMLElement>("[data-testid='first-time-tip-dismiss']"),
      onEscape: () => {
        const id = activeId();
        if (id) dismiss(id);
      },
    },
  );

  return (
    <Show when={activeId()} keyed>
      {(id) => {
        const tip = FIRST_TIME_TIPS[id];
        return (
          <Show when={position()}>
            {(tipPosition) => (
              <ResidentPortal mount={document.body}>
                <section
                  class="den-first-time-tip den-stage-enter-fade"
                  classList={{
                    [`den-first-time-tip--${tipPosition().placement}`]: true,
                    "den-first-time-tip--scroll-held": anchorScrolls() > 0,
                  }}
                  data-testid={`first-time-tip-${id}`}
                  role="dialog"
                  aria-labelledby={`first-time-tip-title-${id}`}
                  aria-describedby={`first-time-tip-body-${id}`}
                  style={{
                    left: `${tipPosition().left}px`,
                    top: `${tipPosition().top}px`,
                  }}
                  ref={setTipElement}
                >
                  <span
                    class="den-first-time-tip__arrow"
                    data-testid="first-time-tip-arrow"
                    data-placement={tipPosition().placement}
                    aria-hidden="true"
                    style={{
                      "--den-first-time-tip-arrow-offset": `${tipPosition().arrowOffset}px`,
                    }}
                  />
                  <strong
                    id={`first-time-tip-title-${id}`}
                    class="den-first-time-tip__title"
                    {...chromeProps()}
                  >
                    {tip.title}
                  </strong>
                  <p id={`first-time-tip-body-${id}`} class="den-first-time-tip__body">
                    {tip.body}
                  </p>
                  <div class="den-first-time-tip__actions">
                    <DenButton
                      variant="ghost"
                      class="den-first-time-tip__disable"
                      data-testid="first-time-tip-disable"
                      onClick={disableTips}
                    >
                      Disable tips
                    </DenButton>
                    <DenButton
                      variant="primary"
                      compact
                      data-testid="first-time-tip-dismiss"
                      onClick={() => dismiss(id)}
                    >
                      Got it
                    </DenButton>
                  </div>
                </section>
              </ResidentPortal>
            )}
          </Show>
        );
      }}
    </Show>
  );
}
