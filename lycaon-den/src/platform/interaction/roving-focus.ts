import { createEffect, onCleanup, onMount, type Accessor } from "solid-js";
import { isPresented } from "../../ui/presented.ts";

export type RovingFocusOptions = {
  /** Selector for the items that share the widget's single tab stop. */
  items: string;
  /** Item holding the tab stop at rest; defaults to the first enabled item. */
  restingItem?: (items: HTMLElement[]) => HTMLElement | undefined;
  /** Tablists select what focus lands on; toolbars only move focus. */
  selectionFollowsFocus?: boolean;
  /** Keys the widget answers beyond navigation, keyed by `KeyboardEvent.key`. */
  keys?: Record<string, (item: HTMLElement) => void>;
  /** Holds the tab stop sync while the widget is not interactive. */
  active?: Accessor<boolean>;
};

function allItems(container: HTMLElement, selector: string): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(selector)];
}

function isEnabled(item: HTMLElement): boolean {
  return (
    !item.matches(":disabled") &&
    item.getAttribute("aria-disabled") !== "true" &&
    isPresented(item)
  );
}

function enabledItems(container: HTMLElement, selector: string): HTMLElement[] {
  return allItems(container, selector).filter(isEnabled);
}

function syncTabStops(container: HTMLElement, options: RovingFocusOptions): void {
  const items = allItems(container, options.items);
  const enabled = items.filter(isEnabled);
  // Preserve the current tab stop when list membership changes.
  const resting =
    options.restingItem?.(enabled) ??
    enabled.find((item) => item.tabIndex === 0) ??
    enabled[0];
  for (const item of items) item.tabIndex = item === resting ? 0 : -1;
}

/** Gives a widget one tab stop and arrow-key movement between its items. */
export function createRovingFocus(
  container: Accessor<HTMLElement | null | undefined>,
  options: RovingFocusOptions,
): void {
  const active = options.active ?? (() => true);

  createEffect(() => {
    if (!active()) return;
    queueMicrotask(() => {
      const element = container();
      if (element) syncTabStops(element, options);
    });
  });

  onMount(() => {
    const element = container();
    if (!element) return;

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
      const target = event.target instanceof Element
        ? event.target.closest<HTMLElement>(options.items)
        : null;
      if (!target || !element.contains(target)) return;

      const items = enabledItems(element, options.items);
      if (items.length === 0) return;
      const at = Math.max(0, items.indexOf(target));
      const vertical = element.getAttribute("aria-orientation") === "vertical";
      let next = -1;
      if (event.key === (vertical ? "ArrowDown" : "ArrowRight")) {
        next = (at + 1) % items.length;
      } else if (event.key === (vertical ? "ArrowUp" : "ArrowLeft")) {
        next = (at - 1 + items.length) % items.length;
      } else if (event.key === "Home") {
        next = 0;
      } else if (event.key === "End") {
        next = items.length - 1;
      }
      if (next < 0) {
        const answer = options.keys?.[event.key];
        if (!answer) return;
        event.preventDefault();
        answer(target);
        return;
      }

      const item = items[next];
      if (!item) return;
      event.preventDefault();
      for (const candidate of items) candidate.tabIndex = candidate === item ? 0 : -1;
      item.focus();
      if (options.selectionFollowsFocus) item.click();
    };

    if (active()) syncTabStops(element, options);
    element.addEventListener("keydown", onKeyDown);
    const observer = typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(() => {
          if (active()) syncTabStops(element, options);
        });
    observer?.observe(element, {
      attributes: true,
      attributeFilter: ["aria-selected", "aria-disabled", "disabled", "hidden"],
      childList: true,
      subtree: true,
    });
    onCleanup(() => {
      observer?.disconnect();
      element.removeEventListener("keydown", onKeyDown);
    });
  });
}

/** Roving focus for a tablist: the selected tab rests, focus selects. */
export function createTablistKeyboard(
  container: Accessor<HTMLElement | null | undefined>,
  active: Accessor<boolean> = () => true,
): void {
  createRovingFocus(container, {
    items: '[role="tab"]',
    restingItem: (tabs) =>
      tabs.find((tab) => tab.getAttribute("aria-selected") === "true"),
    selectionFollowsFocus: true,
    active,
  });
}
