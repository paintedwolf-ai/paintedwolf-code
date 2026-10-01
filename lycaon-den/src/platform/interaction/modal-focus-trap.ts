import { createEffect, onCleanup, type Accessor } from "solid-js";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import {
  activateFocusTrap,
  focusableElements,
  type FocusTrapOptions,
} from "./focus-trap.ts";
import {
  claimOverlayScope,
  claimShortcutBoundary,
} from "../../shortcuts/dispatcher.ts";

export type SolidTrapOptions = Omit<FocusTrapOptions, "inertTarget"> & {
  inertTarget?: Accessor<HTMLElement | HTMLElement[] | null | undefined>;
  /** Whether deactivation returns focus to the opener. */
  restoreFocus?: Accessor<boolean>;
};

type AnchoredPopoverFocusOptions = {
  trigger: Accessor<HTMLElement | null | undefined>;
  initialFocus?: Accessor<HTMLElement | null | undefined>;
  onEscape: () => void;
};

function createFocusTrap(
  open: Accessor<boolean>,
  container: Accessor<HTMLElement | undefined | null>,
  options: SolidTrapOptions,
): void {
  const interactive = useResidentInteractive();
  createEffect(() => {
    if (!open() || !interactive()) return;
    const element = container();
    if (!element) return;
    const handle = activateFocusTrap(element, {
      onEscape: options.onEscape,
      returnFocus: options.returnFocus,
      initialFocus: options.initialFocus,
      autoFocus: options.autoFocus,
      inertTarget: options.inertTarget?.() ?? null,
    });
    onCleanup(() => handle.deactivate({
      restoreFocus: interactive() && (options.restoreFocus?.() ?? true),
    }));
  });
}

/** Publishes overlay shortcut scope while a surface is open. */
export function createOverlayShortcutScope(open: Accessor<boolean>): void {
  const interactive = useResidentInteractive();
  createEffect(() => {
    if (!open() || !interactive()) return;
    const release = claimOverlayScope();
    onCleanup(release);
  });
}

/** Controls overlay shortcut scope and focus. */
export function createOverlayScopeFocusTrap(
  open: Accessor<boolean>,
  container: Accessor<HTMLElement | undefined | null>,
  options: SolidTrapOptions = {},
): void {
  createOverlayShortcutScope(open);
  createFocusTrap(open, container, options);
}

/** Manages focus for a non-modal anchored popover. */
export function createAnchoredPopoverFocus(
  open: Accessor<boolean>,
  container: Accessor<HTMLElement | null | undefined>,
  options: AnchoredPopoverFocusOptions,
): void {
  const interactive = useResidentInteractive();
  createEffect(() => {
    if (!open() || !interactive()) return;
    const element = container();
    if (!element) return;
    const trigger = options.trigger();
    let active = true;
    const focusInitial = () => {
      if (!active) return;
      const preferred = options.initialFocus?.();
      if (preferred && element.contains(preferred)) {
        preferred.focus({ preventScroll: true });
        return;
      }
      const first = focusableElements(element)[0];
      if (first) {
        first.focus({ preventScroll: true });
        return;
      }
      element.tabIndex = -1;
      element.focus({ preventScroll: true });
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      options.onEscape();
    };

    queueMicrotask(focusInitial);
    document.addEventListener("keydown", onKeyDown, true);
    onCleanup(() => {
      active = false;
      document.removeEventListener("keydown", onKeyDown, true);
      if (interactive() && trigger?.isConnected) trigger.focus({ preventScroll: true });
    });
  });
}

/** A standalone modal controls focus, background inertness, and app shortcuts. */
export function createModalFocusTrap(
  open: Accessor<boolean>,
  container: Accessor<HTMLElement | undefined | null>,
  options: SolidTrapOptions = {},
): void {
  const interactive = useResidentInteractive();
  const inertTarget =
    options.inertTarget ??
    (() => {
      const appRoot = document.getElementById("root");
      const modal = container();
      return appRoot && modal && !appRoot.contains(modal) ? appRoot : null;
    });
  createFocusTrap(open, container, { ...options, inertTarget });
  createEffect(() => {
    if (!open() || !interactive() || !container()) return;
    const release = claimShortcutBoundary();
    onCleanup(release);
  });
}

export function shellChromeInertTargets(): HTMLElement[] {
  return [
    ...document.querySelectorAll<HTMLElement>(
      ".den-shell-aside, .den-shell-stage",
    ),
  ];
}
