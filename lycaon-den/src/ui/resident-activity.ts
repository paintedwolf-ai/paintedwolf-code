import { createEffect, createMemo, onCleanup, untrack, type Accessor } from "solid-js";
import { focusWithoutScroll } from "../platform/interaction/focus.ts";
import { useResidentInteractive, useResidentPresence } from "./resident-presence-context.tsx";
import { isPresented } from "./presented.ts";
import { afterPaint } from "./surface-reveal.ts";

type Cleanup = () => void;

/** Whether a resident surface may run background work. */
export function useResidentLive(): Accessor<boolean> {
  const presence = useResidentPresence();
  return createMemo(() => presence() !== "idle");
}

/** Stops background work while a resident surface is idle. */
export function createResidentActivity(setup: () => void | Cleanup): void {
  const live = useResidentLive();

  createEffect(() => {
    if (!live()) return;
    const cleanup = untrack(setup);
    if (cleanup) onCleanup(cleanup);
  });
}

/** Focuses a target when its resident surface activates. */
export function createResidentFocus(
  target: Accessor<HTMLElement | null | undefined>,
  afterFocus?: () => void,
): void {
  const interactive = useResidentInteractive();
  let activation = 0;
  let focusedActivation = -1;
  let focusedTarget: HTMLElement | null = null;
  let wasActive = false;

  createEffect(() => {
    const active = interactive();
    if (!active) {
      wasActive = false;
      focusedTarget = null;
      return;
    }
    if (!wasActive) {
      wasActive = true;
      activation += 1;
    }
    const requestedActivation = activation;
    // Resolve again after child refs attach.
    const initialTarget = target() ?? null;
    const initialFocus = document.activeElement;
    let cancelRetry: (() => void) | undefined;
    let observer: MutationObserver | undefined;
    let disposed = false;
    const scheduleFocus = (confirm: boolean) => {
      if (disposed || cancelRetry) return;
      cancelRetry = afterPaint(() => {
        cancelRetry = undefined;
        focusTarget(confirm);
      });
    };
    const focusTarget = (confirmAfterPaint: boolean) => {
      const element = target() ?? null;
      const activeElement = document.activeElement;
      const focusIsStable = activeElement === element;
      const focusMovedElsewhere =
        activeElement instanceof HTMLElement &&
        activeElement !== document.body &&
        activeElement !== document.documentElement &&
        activeElement.isConnected &&
        isPresented(activeElement);
      if (
        !element ||
        disposed ||
        !isPresented(element) ||
        (focusMovedElsewhere && activeElement !== initialFocus && !focusIsStable) ||
        !interactive() ||
        requestedActivation !== activation ||
        (focusedActivation === activation &&
          focusedTarget === element &&
          (focusIsStable || focusMovedElsewhere))
      ) {
        return;
      }
      if (focusWithoutScroll(element)) {
        focusedActivation = activation;
        focusedTarget = element;
        observer?.disconnect();
        afterFocus?.();
        if (confirmAfterPaint) scheduleFocus(false);
      }
    };
    if (initialTarget && typeof MutationObserver !== "undefined") {
      const root =
        initialTarget.closest(".den-resident-surface") ??
        initialTarget.parentElement ??
        document.body;
      observer = new MutationObserver(() => scheduleFocus(true));
      observer.observe(root, {
        attributes: true,
        subtree: true,
        attributeFilter: [
          "aria-hidden",
          "data-boot",
          "disabled",
          "hidden",
          "inert",
        ],
      });
    }
    // Focusing before the prepared editor paints forces layout in navigation.
    scheduleFocus(true);
    onCleanup(() => {
      disposed = true;
      cancelRetry?.();
      observer?.disconnect();
    });
  });
}
