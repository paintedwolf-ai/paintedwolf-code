import type { ContextMenuAnchor } from "./ContextMenu.tsx";

const OVERFLOW_GAP = 6;

/**
 * Right-click / Shift+F10 → ContextMenu. stopPropagation so the Files-stage
 * listener does not also open.
 */
export function bindChromeContextMenu(
  open: (anchor: ContextMenuAnchor) => void,
): {
  onContextMenu: (e: MouseEvent) => void;
  onKeyDown: (e: KeyboardEvent) => void;
} {
  return {
    onContextMenu: (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (targetDisabled(e.currentTarget)) return;
      open({ x: e.clientX, y: e.clientY });
    },
    onKeyDown: (e) => {
      if (!isContextMenuKey(e)) return;
      e.preventDefault();
      e.stopPropagation();
      const t = e.currentTarget;
      if (!(t instanceof HTMLElement) || targetDisabled(t)) return;
      const rect = t.getBoundingClientRect();
      open({ x: rect.left, y: rect.bottom });
    },
  };
}

function isContextMenuKey(event: KeyboardEvent): boolean {
  return event.key === "ContextMenu" || (event.key === "F10" && event.shiftKey);
}

/** Keyboard requests use the same target resolver as pointer context menus. */
export function dispatchKeyboardContextMenu(event: KeyboardEvent): void {
  if (event.defaultPrevented || !isContextMenuKey(event)) return;
  const target = event.target;
  if (!(target instanceof HTMLElement) || targetDisabled(target)) return;
  const rect = target.getBoundingClientRect();
  event.preventDefault();
  target.dispatchEvent(new MouseEvent("contextmenu", {
    bubbles: true,
    cancelable: true,
    button: 0,
    clientX: rect.left,
    clientY: rect.bottom,
  }));
}

/** Right edge of a right-aligned trigger. Pair with ContextMenu `align="end"`. */
export function overflowMenuAnchor(
  el: HTMLElement | undefined | null,
): ContextMenuAnchor | null {
  if (!el) return null;
  const rect = el.getBoundingClientRect();
  return { x: rect.right, y: rect.bottom + OVERFLOW_GAP };
}

function targetDisabled(target: EventTarget | null): boolean {
  return target instanceof HTMLButtonElement && target.disabled;
}
