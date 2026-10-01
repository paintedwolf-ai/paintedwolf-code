import { Show, createSignal, onCleanup, onMount } from "solid-js";
import {
  AnchoredSurface,
  type AnchoredSide,
} from "./AnchoredSurface.tsx";

type TooltipPlacement = {
  side: AnchoredSide;
  align: "center" | "end";
};

type OpenTooltip = TooltipPlacement & {
  anchor: HTMLElement;
  label: string;
};

const TOOLTIP_SELECTOR = "[data-tip]";
const DISABLED_TOOLTIP_SELECTOR =
  ':scope > [data-tip]:disabled, :scope > [data-tip][aria-disabled="true"]';
const TOOLTIP_DELAY_MS = 350;

function tooltipTarget(target: EventTarget | null): HTMLElement | null {
  return target instanceof Element
    ? target.closest<HTMLElement>(TOOLTIP_SELECTOR)
    : null;
}

function isClipped(el: HTMLElement): boolean {
  const style = getComputedStyle(el);
  return (
    style.display === "none" ||
    style.visibility === "hidden" ||
    el.scrollWidth > el.clientWidth ||
    el.scrollHeight > el.clientHeight
  );
}

function tooltipIsUseful(anchor: HTMLElement): boolean {
  if (!("tipWhenClipped" in anchor.dataset)) return true;
  const selector = anchor.dataset.tipWhenClipped?.trim();
  const labels = selector
    ? Array.from(anchor.querySelectorAll<HTMLElement>(selector))
    : [anchor];
  return labels.some(isClipped);
}

function containsPoint(el: HTMLElement, x: number, y: number): boolean {
  const r = el.getBoundingClientRect();
  return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
}

function tooltipPlacement(anchor: HTMLElement): TooltipPlacement {
  switch (anchor.dataset.tipPos) {
    case "below":
      return { side: "bottom", align: "end" };
    case "end":
      return { side: "right", align: "center" };
    default:
      return { side: "top", align: "end" };
  }
}

/** One delegated, portaled tooltip layer for every data-tip control. */
export function TooltipHost() {
  const [open, setOpen] = createSignal<OpenTooltip | null>(null);
  let hovered: HTMLElement | null = null;
  let focused: HTMLElement | null = null;
  let showTimer: number | undefined;
  // Disabled controls are hit-tested through their parent.
  let probed: HTMLElement | null = null;
  let probeHost: Element | null = null;
  let probeCandidates: HTMLElement[] = [];

  const clearTimer = () => {
    if (showTimer === undefined) return;
    window.clearTimeout(showTimer);
    showTimer = undefined;
  };

  const reconcile = () => {
    clearTimer();
    if (hovered && !hovered.isConnected) hovered = null;
    if (focused && !focused.isConnected) focused = null;
    if (probed && !probed.isConnected) probed = null;
    const anchor = focused ?? hovered ?? probed;
    const label = anchor?.dataset.tip?.trim() ?? "";
    if (!anchor || !label || !tooltipIsUseful(anchor)) {
      setOpen(null);
      return;
    }
    if (open()?.anchor === anchor && open()?.label === label) return;
    setOpen(null);
    showTimer = window.setTimeout(() => {
      showTimer = undefined;
      if ((focused ?? hovered ?? probed) !== anchor || !anchor.isConnected) return;
      setOpen({ anchor, label, ...tooltipPlacement(anchor) });
    }, TOOLTIP_DELAY_MS);
  };

  onMount(() => {
    const setProbed = (next: HTMLElement | null) => {
      if (probed === next) return;
      probed = next;
      reconcile();
    };

    const onMouseOver = (event: MouseEvent) => {
      const anchor = tooltipTarget(event.target);
      if (!anchor || hovered === anchor) return;
      hovered = anchor;
      setProbed(null);
      reconcile();
    };
    const onMouseMove = (event: MouseEvent) => {
      if (hovered) return;
      const host = event.target instanceof Element ? event.target : null;
      if (host !== probeHost) {
        probeHost = host;
        probeCandidates = host
          ? Array.from(host.querySelectorAll<HTMLElement>(DISABLED_TOOLTIP_SELECTOR))
          : [];
      }
      if (probeCandidates.length === 0) {
        setProbed(null);
        return;
      }
      setProbed(
        probeCandidates.find((el) =>
          containsPoint(el, event.clientX, event.clientY),
        ) ?? null,
      );
    };
    const onMouseLeave = () => {
      probeHost = null;
      probeCandidates = [];
      setProbed(null);
    };
    const onMouseOut = (event: MouseEvent) => {
      const anchor = tooltipTarget(event.target);
      if (!anchor || hovered !== anchor) return;
      if (
        event.relatedTarget instanceof Node &&
        anchor.contains(event.relatedTarget)
      ) return;
      hovered = null;
      reconcile();
    };
    const onFocusIn = (event: FocusEvent) => {
      const anchor = tooltipTarget(event.target);
      if (!anchor || focused === anchor) return;
      focused = anchor;
      reconcile();
    };
    const onFocusOut = (event: FocusEvent) => {
      const anchor = tooltipTarget(event.target);
      if (!anchor || focused !== anchor) return;
      if (
        event.relatedTarget instanceof Node &&
        anchor.contains(event.relatedTarget)
      ) return;
      focused = null;
      reconcile();
    };

    document.addEventListener("mouseover", onMouseOver);
    document.addEventListener("mouseout", onMouseOut);
    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseleave", onMouseLeave);
    document.addEventListener("focusin", onFocusIn);
    document.addEventListener("focusout", onFocusOut);
    onCleanup(() => {
      clearTimer();
      document.removeEventListener("mouseover", onMouseOver);
      document.removeEventListener("mouseout", onMouseOut);
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseleave", onMouseLeave);
      document.removeEventListener("focusin", onFocusIn);
      document.removeEventListener("focusout", onFocusOut);
    });
  });

  return (
    <Show when={open()} keyed>
      {(tooltip) => (
        <AnchoredSurface
          class="den-tooltip"
          role="tooltip"
          testId="den-tooltip"
          anchor={() => tooltip.anchor}
          preferredSide={tooltip.side}
          align={tooltip.align}
          gap={7}
          height="content"
          dismissOnScroll
          dismissWhenAnchorHidden
          onDismiss={() => {
            if (hovered === tooltip.anchor) hovered = null;
            if (focused === tooltip.anchor) focused = null;
            if (probed === tooltip.anchor) probed = null;
            setOpen(null);
          }}
        >
          {tooltip.label}
        </AnchoredSurface>
      )}
    </Show>
  );
}
