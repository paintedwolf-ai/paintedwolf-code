import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { onCleanup, createEffect, on, type JSX } from "solid-js";
import { ResidentPortal } from "./ResidentPortal.tsx";
import { focusRetainingSelection } from "../../platform/interaction/selection-lease.ts";
import { isPresented } from "../../ui/presented.ts";

export type AnchoredSide = "top" | "right" | "bottom" | "left";
type AnchoredAlign = "start" | "center" | "end";
type AnchoredPoint = { x: number; y: number };
type AnchoredTarget = Element | AnchoredRect | AnchoredPoint;

type AnchoredRect = {
  top: number;
  right: number;
  bottom: number;
  left: number;
  width: number;
  height: number;
};

type AnchoredPlacement = {
  side: AnchoredSide;
  left: number;
  top: number;
  maxWidth: number;
  maxHeight: number;
};

type PlacementOptions = {
  preferredSide?: AnchoredSide;
  align?: AnchoredAlign;
  gap?: number;
  /** `viewport` bounds height; `content` allows viewport overflow. */
  height?: "content" | "viewport";
};

type Props = PlacementOptions & {
  anchor: () => AnchoredTarget | null | undefined;
  children: JSX.Element;
  class?: string;
  id?: string;
  role?: JSX.HTMLAttributes<HTMLDivElement>["role"];
  tabIndex?: number;
  ariaLabel?: string;
  ariaActiveDescendant?: string;
  testId?: string;
  chrome?: boolean;
  width?: "anchor" | "min-anchor";
  overflow?: "auto" | "hidden";
  minWidth?: number;
  dismissOnScroll?: boolean;
  dismissOnEscape?: boolean;
  /** Custom menus manage placement focus and selection restoration themselves. */
  menuFocus?: "automatic" | "managed";
  /** Hidden anchors dismiss the surface. */
  dismissWhenAnchorHidden?: boolean;
  onDismiss?: () => void;
  onEscape?: () => void;
  onPositioned?: (element: HTMLDivElement, placement: AnchoredPlacement) => void;
  ref?: (element: HTMLDivElement) => void;
  onKeyDown?: JSX.EventHandlerUnion<HTMLDivElement, KeyboardEvent>;
  onMouseDown?: JSX.EventHandlerUnion<HTMLDivElement, MouseEvent>;
  onContextMenu?: JSX.EventHandlerUnion<HTMLDivElement, MouseEvent>;
  onMouseEnter?: JSX.EventHandlerUnion<HTMLDivElement, MouseEvent>;
  onMouseLeave?: JSX.EventHandlerUnion<HTMLDivElement, MouseEvent>;
};

const DEFAULT_PADDING = 8;
const DEFAULT_GAP = 4;
let nextSurfaceId = 0;
const MENU_ITEM_SELECTOR =
  '[role="menuitem"],[role="menuitemcheckbox"],[role="menuitemradio"]';

function enabledMenuItems(surface: HTMLElement): HTMLElement[] {
  return [...surface.querySelectorAll<HTMLElement>(MENU_ITEM_SELECTOR)].filter(
    (item) =>
      !item.matches(":disabled") &&
      item.getAttribute("aria-disabled") !== "true" &&
      isPresented(item),
  );
}

export function anchoredSurfaceViewport(): {
  top: number;
  right: number;
  bottom: number;
  left: number;
} {
  return {
    top: DEFAULT_PADDING,
    right: window.innerWidth - DEFAULT_PADDING,
    bottom: window.innerHeight - DEFAULT_PADDING,
    left: DEFAULT_PADDING,
  };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), Math.max(min, max));
}

function opposite(side: AnchoredSide): AnchoredSide {
  switch (side) {
    case "top":
      return "bottom";
    case "right":
      return "left";
    case "bottom":
      return "top";
    case "left":
      return "right";
  }
}

function available(
  side: AnchoredSide,
  anchor: AnchoredRect,
  viewport: { width: number; height: number },
  gap: number,
  padding: number,
): number {
  switch (side) {
    case "top":
      return anchor.top - gap - padding;
    case "right":
      return viewport.width - anchor.right - gap - padding;
    case "bottom":
      return viewport.height - anchor.bottom - gap - padding;
    case "left":
      return anchor.left - gap - padding;
  }
}

function alignedStart(
  align: AnchoredAlign,
  start: number,
  end: number,
  surfaceSize: number,
): number {
  if (align === "end") return end - surfaceSize;
  if (align === "center") return start + (end - start - surfaceSize) / 2;
  return start;
}

export function placeAnchoredSurface(
  anchor: AnchoredRect,
  surface: { width: number; height: number },
  viewport: { width: number; height: number },
  options: PlacementOptions = {},
): AnchoredPlacement {
  const padding = DEFAULT_PADDING;
  const gap = options.gap ?? DEFAULT_GAP;
  const preferred = options.preferredSide ?? "bottom";
  const alternate = opposite(preferred);
  const primarySize =
    preferred === "top" || preferred === "bottom"
      ? surface.height
      : surface.width;
  const preferredRoom = Math.max(
    0,
    available(preferred, anchor, viewport, gap, padding),
  );
  const alternateRoom = Math.max(
    0,
    available(alternate, anchor, viewport, gap, padding),
  );
  const side =
    primarySize > preferredRoom && alternateRoom > preferredRoom
      ? alternate
      : preferred;
  const room = side === preferred ? preferredRoom : alternateRoom;
  const viewportWidth = Math.max(0, viewport.width - padding * 2);
  const viewportHeight = Math.max(0, viewport.height - padding * 2);
  // Rounding size up and available space down prevents subpixel overflow.
  const maxWidth = Math.min(
    Math.ceil(surface.width),
    Math.floor(side === "left" || side === "right" ? room : viewportWidth),
  );
  const maxHeight = options.height === "content"
    ? Math.ceil(surface.height)
    : options.height === "viewport"
      ? Math.min(Math.ceil(surface.height), Math.floor(viewportHeight))
      : Math.min(
        Math.ceil(surface.height),
        Math.floor(side === "top" || side === "bottom" ? room : viewportHeight),
      );
  const align = options.align ?? "start";
  let left: number;
  let top: number;

  if (side === "top" || side === "bottom") {
    left = alignedStart(align, anchor.left, anchor.right, maxWidth);
    left = clamp(left, padding, viewport.width - padding - maxWidth);
    top = side === "bottom" ? anchor.bottom + gap : anchor.top - gap - maxHeight;
  } else {
    top = alignedStart(align, anchor.top, anchor.bottom, maxHeight);
    top = clamp(top, padding, viewport.height - padding - maxHeight);
    left = side === "right" ? anchor.right + gap : anchor.left - gap - maxWidth;
  }

  return {
    side,
    left: Math.round(clamp(left, padding, viewport.width - padding - maxWidth)),
    top: Math.round(clamp(top, padding, viewport.height - padding - maxHeight)),
    maxWidth: Math.max(0, maxWidth),
    maxHeight: Math.max(0, maxHeight),
  };
}

function targetRect(target: AnchoredTarget): AnchoredRect {
  if (target instanceof Element) return target.getBoundingClientRect();
  if ("width" in target) return target;
  return {
    top: target.y,
    right: target.x,
    bottom: target.y,
    left: target.x,
    width: 0,
    height: 0,
  };
}

// Element anchors use containment; virtual anchors use scroller bounds.
function scrollMovesAnchor(target: EventTarget | null, anchor: AnchoredTarget): boolean {
  if (!(target instanceof Element)) return true;
  if (anchor instanceof Element) return target.contains(anchor);
  const rect = targetRect(anchor);
  const box = target.getBoundingClientRect();
  return (
    rect.right >= box.left &&
    rect.left <= box.right &&
    rect.bottom >= box.top &&
    rect.top <= box.bottom
  );
}

function surfaceContainsTarget(surface: HTMLElement, target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  let ancestor = target.closest<HTMLElement>("[data-den-anchored-surface]");
  while (ancestor) {
    if (ancestor === surface) return true;
    const parentId = ancestor.dataset.denAnchoredParent;
    ancestor = parentId
      ? document.querySelector<HTMLElement>(
          `[data-den-anchored-surface="${parentId}"]`,
        )
      : null;
  }
  return false;
}

function isTopmostSurface(surface: HTMLElement): boolean {
  const surfaces = document.querySelectorAll<HTMLElement>(
    "[data-den-anchored-surface]",
  );
  return surfaces.item(surfaces.length - 1) === surface;
}

export function AnchoredSurface(props: Props) {
  const interactive = useResidentInteractive();
  const surfaceId = `den-anchored-${++nextSurfaceId}`;
  let surfaceEl: HTMLDivElement | undefined;
  let frame = 0;
  let resizeObserver: ResizeObserver | undefined;
  let contentObserver: MutationObserver | undefined;
  let anchorObserver: MutationObserver | undefined;
  let placedWidth = -1;
  let placedHeight = -1;

  // Collapsed panes retain mounted controls.
  const anchorLost = (target: AnchoredTarget | null | undefined) =>
    target instanceof Element &&
    (!target.isConnected ||
      (Boolean(props.dismissWhenAnchorHidden) && !isPresented(target)));

  const update = () => {
    if (!interactive()) return;
    const element = surfaceEl;
    const target = props.anchor();
    if (!element || !target) return;
    if (anchorLost(target)) {
      props.onDismiss?.();
      return;
    }
    const anchor = targetRect(target);
    if (target instanceof Element) {
      const measured = anchor.width > 0 || anchor.height > 0;
      const outside =
        anchor.bottom <= 0 ||
        anchor.top >= window.innerHeight ||
        anchor.right <= 0 ||
        anchor.left >= window.innerWidth;
      if (measured && outside) {
        props.onDismiss?.();
        return;
      }
    }

    element.style.width = "";
    element.style.minWidth = "";
    element.style.maxWidth = "";
    element.style.maxHeight = "";
    const viewportWidth = Math.max(0, window.innerWidth - 2 * DEFAULT_PADDING);
    if (props.width === "anchor") {
      element.style.width = `${Math.min(anchor.width, viewportWidth)}px`;
    }
    if (props.width === "min-anchor" || props.minWidth !== undefined) {
      const minWidth = Math.max(
        props.width === "min-anchor" ? anchor.width : 0,
        props.minWidth ?? 0,
      );
      element.style.minWidth = `${Math.min(minWidth, viewportWidth)}px`;
    }

    const measured = element.getBoundingClientRect();
    const placement = placeAnchoredSurface(
      anchor,
      { width: measured.width, height: measured.height },
      { width: window.innerWidth, height: window.innerHeight },
      props,
    );
    element.style.left = `${placement.left}px`;
    element.style.top = `${placement.top}px`;
    element.style.right = "auto";
    element.style.bottom = "auto";
    element.style.boxSizing = "border-box";
    element.style.maxWidth = `${placement.maxWidth}px`;
    element.style.maxHeight = props.height === "content"
      ? "none"
      : `${placement.maxHeight}px`;
    element.style.overflowX = "hidden";
    element.style.overflowY = props.height === "content"
      ? "visible"
      : props.overflow ?? "auto";
    element.style.opacity = "1";
    element.dataset.side = placement.side;
    // An unclipped surface keeps its measured size, so only a clip needs a second read.
    const clipped = placement.maxWidth < measured.width;
    const placed = clipped ? element.getBoundingClientRect() : measured;
    placedWidth = clipped ? placed.width : measured.width;
    placedHeight = props.height === "content" || !clipped
      ? placed.height
      : Math.min(placed.height, placement.maxHeight);
    props.onPositioned?.(element, placement);
  };

  const scheduleUpdate = () => {
    if (typeof requestAnimationFrame === "undefined") {
      queueMicrotask(update);
      return;
    }
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(update);
  };

  createEffect(on(() => interactive(), (active) => {
    if (!active) return;
    const element = surfaceEl;
    if (!element) return;
    const target = props.anchor();
    if (target instanceof Element) {
      const parent = target.closest<HTMLElement>("[data-den-anchored-surface]");
      if (parent) element.dataset.denAnchoredParent = parent.dataset.denAnchoredSurface;
    }
    queueMicrotask(update);

    const onScroll = (event: Event) => {
      if (surfaceContainsTarget(element, event.target)) return;
      const anchor = props.anchor();
      if (anchor && !scrollMovesAnchor(event.target, anchor)) return;
      if (props.dismissOnScroll) props.onDismiss?.();
      else scheduleUpdate();
    };
    const dismissOutside = (event: Event) => {
      const anchor = props.anchor();
      if (anchor instanceof Element && anchor.contains(event.target as Node)) return;
      if (surfaceContainsTarget(element, event.target)) return;
      props.onDismiss?.();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        props.dismissOnEscape === false ||
        event.key !== "Escape" ||
        event.defaultPrevented ||
        !isTopmostSurface(element)
      )
        return;
      event.preventDefault();
      event.stopPropagation();
      (props.onEscape ?? props.onDismiss)?.();
    };
    const onMenuKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || props.role !== "menu") return;
      if (event.key === "Escape") {
        if (props.dismissOnEscape === false) return;
        event.preventDefault();
        event.stopPropagation();
        (props.onEscape ?? props.onDismiss)?.();
        const anchor = props.anchor();
        if (props.menuFocus !== "managed" && anchor instanceof HTMLElement) {
          queueMicrotask(() => anchor.focus({ preventScroll: true }));
        }
        return;
      }

      if (props.onKeyDown) return;
      const items = enabledMenuItems(element);
      if (items.length === 0) return;
      const active = document.activeElement instanceof Element
        ? document.activeElement.closest<HTMLElement>(MENU_ITEM_SELECTOR)
        : null;
      const at = Math.max(0, active ? items.indexOf(active) : 0);
      let next = -1;
      if (event.key === "ArrowDown") next = (at + 1) % items.length;
      else if (event.key === "ArrowUp") next = (at - 1 + items.length) % items.length;
      else if (event.key === "Home") next = 0;
      else if (event.key === "End") next = items.length - 1;
      else if (
        event.key.length === 1 &&
        !event.metaKey &&
        !event.ctrlKey &&
        !event.altKey
      ) {
        const needle = event.key.toLocaleLowerCase();
        for (let offset = 1; offset <= items.length; offset += 1) {
          const candidate = items[(at + offset) % items.length];
          if (
            candidate &&
            (candidate.textContent ?? "").trim().toLocaleLowerCase().startsWith(needle)
          ) {
            next = (at + offset) % items.length;
            break;
          }
        }
      }
      if (next < 0) return;
      event.preventDefault();
      items[next]?.focus({ preventScroll: true });
    };
    const onBlur = () => props.onDismiss?.();
    window.addEventListener("resize", scheduleUpdate);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("blur", onBlur);
    // Embedded webviews may emit either event for a mouse press.
    document.addEventListener("pointerdown", dismissOutside, true);
    document.addEventListener("mousedown", dismissOutside, true);
    document.addEventListener("keydown", onKeyDown);
    if (props.role === "menu") {
      element.addEventListener("keydown", onMenuKeyDown);
      queueMicrotask(() => {
        if (props.menuFocus === "managed" || !interactive() || !element.isConnected || element.contains(document.activeElement)) return;
        const first = enabledMenuItems(element)[0];
        if (first) focusRetainingSelection(first, { preventScroll: true });
        else {
          element.tabIndex = -1;
          element.focus({ preventScroll: true });
        }
      });
    }
    if (typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver((entries) => {
        const changed = entries.some((entry) => {
          if (entry.target !== element) return true;
          const rect = element.getBoundingClientRect();
          return (
            Math.abs(rect.width - placedWidth) > 0.5 ||
            Math.abs(rect.height - placedHeight) > 0.5
          );
        });
        if (changed) scheduleUpdate();
      });
      resizeObserver.observe(element);
      if (target instanceof Element) {
        let observed: Element | null = target;
        while (observed && observed !== document.body) {
          resizeObserver.observe(observed);
          observed = observed.parentElement;
        }
      }
    }
    if (typeof MutationObserver !== "undefined") {
      // Max-height can hide content changes from the resize observer.
      contentObserver = new MutationObserver(scheduleUpdate);
      contentObserver.observe(element, {
        childList: true,
        subtree: true,
        characterData: true,
      });
      if (target instanceof Element) {
        anchorObserver = new MutationObserver(() => {
          if (anchorLost(props.anchor())) props.onDismiss?.();
        });
        anchorObserver.observe(document.body, {
          childList: true,
          subtree: true,
          // An attributeFilter alongside attributes: false throws.
          ...(props.dismissWhenAnchorHidden
            ? { attributeFilter: ["hidden", "inert", "aria-hidden"] }
            : {}),
        });
      }
    }
    onCleanup(() => {
      cancelAnimationFrame(frame);
      resizeObserver?.disconnect();
      contentObserver?.disconnect();
      anchorObserver?.disconnect();
      window.removeEventListener("resize", scheduleUpdate);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("blur", onBlur);
      document.removeEventListener("pointerdown", dismissOutside, true);
      document.removeEventListener("mousedown", dismissOutside, true);
      document.removeEventListener("keydown", onKeyDown);
      element.removeEventListener("keydown", onMenuKeyDown);
    });
  }));

  return (
    <ResidentPortal mount={document.body}>
      <div
        ref={(element) => {
          surfaceEl = element;
          props.ref?.(element);
        }}
        id={props.id}
        class={props.class}
        role={props.role}
        tabindex={props.tabIndex}
        aria-label={props.ariaLabel}
        aria-activedescendant={props.ariaActiveDescendant}
        data-testid={props.testId}
        data-den-chrome={props.chrome ? "" : undefined}
        data-den-anchored-surface={surfaceId}
        style={{
          position: "fixed",
          opacity: "0",
          "z-index": "var(--den-z-anchored-surface)",
        }}
        onKeyDown={props.onKeyDown}
        onMouseDown={props.onMouseDown}
        onContextMenu={props.onContextMenu}
        onMouseEnter={props.onMouseEnter}
        onMouseLeave={props.onMouseLeave}
      >
        {props.children}
      </div>
    </ResidentPortal>
  );
}
