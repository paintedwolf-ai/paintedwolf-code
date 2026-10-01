import {
  batch,
  createMemo,
  createSignal,
  createEffect,
  onCleanup,
} from "solid-js";
import { usePresentationParticipant } from "./presentation-context.tsx";
import type { PreparationNotice } from "./presentation.ts";

export type SurfaceRevealOptions = {
  ready?: () => boolean;
  name?: string;
  participate?: boolean;
  notice?: () => PreparationNotice | null;
  /** Lets a renderable failure settle preparation. */
  faulted?: () => boolean;
  deferInitialReveal?: boolean;
};

export type SurfaceReveal = {
  ready: () => boolean;
  animate: () => boolean;
  finishAnimation: () => void;
  reset: () => void;
};

export function afterPaint(run: () => void): () => void {
  // Hidden windows may throttle animation frames.
  if (
    typeof document !== "undefined" &&
    document.visibilityState === "hidden"
  ) {
    const t = setTimeout(run, 0);
    return () => clearTimeout(t);
  }
  if (typeof requestAnimationFrame !== "function") {
    const t = setTimeout(run, 0);
    return () => clearTimeout(t);
  }
  let outer = 0;
  let inner = 0;
  // Two frames separate preparation from publication.
  outer = requestAnimationFrame(() => {
    inner = requestAnimationFrame(run);
  });
  return () => {
    cancelAnimationFrame(outer);
    cancelAnimationFrame(inner);
  };
}

/** Reveals ready content after a stable paint boundary. */
export function useSurfaceReveal(options: SurfaceRevealOptions = {}): SurfaceReveal {
  const contentReady = createMemo(() => options.ready?.() ?? true);
  // Ready content skips the initial transition unless deferred.
  const [revealed, setRevealed] = createSignal(
    !options.deferInitialReveal && contentReady(),
  );
  const [animate, setAnimate] = createSignal(false);
  const [generation, setGeneration] = createSignal(0);
  let cancelReveal: (() => void) | undefined;

  const clearReveal = () => {
    cancelReveal?.();
    cancelReveal = undefined;
  };

  const reset = () => {
    clearReveal();
    batch(() => {
      setRevealed(false);
      setAnimate(false);
      // A pending reveal is already false, so reset needs its own invalidation.
      setGeneration((value) => value + 1);
    });
  };

  createEffect(() => {
    generation();
    if (revealed()) {
      clearReveal();
      return;
    }
    if (!contentReady()) {
      clearReveal();
      return;
    }
    clearReveal();
    cancelReveal = afterPaint(() => {
      cancelReveal = undefined;
      // A child can withdraw readiness before the next paint.
      if (!contentReady()) return;
      batch(() => {
        setAnimate(true);
        setRevealed(true);
      });
    });
    onCleanup(clearReveal);
  });

  if (options.participate !== false) usePresentationParticipant(
    options.name ?? "surface",
    createMemo(() => revealed() || contentReady() || Boolean(options.faulted?.())),
    options.notice,
  );

  return { ready: revealed, animate, finishAnimation: () => setAnimate(false), reset };
}

/** Holds the mounted stage until the incoming surface is visible. */
export function useStageHandoff<T>(
  activeStage: () => T | null,
  incoming: () => { pending: boolean } | null,
  isIncomingSurface: () => boolean,
): () => T | null {
  return createMemo((previous: T | null | undefined): T | null => {
    const active = activeStage();
    if (active) return active;
    if (!isIncomingSurface() || !previous) return null;
    return incoming()?.pending !== false ? previous : null;
  });
}

/** Attributes for paint-gated stage roots. */
export function surfaceRevealDom(boot: SurfaceReveal): {
  classList: { "den-stage-boot": true; "den-enter-fade": boolean };
  "data-boot": "ready" | "pending";
  "aria-busy": boolean;
  "aria-hidden": boolean;
  inert: true | undefined;
  onAnimationEnd: (event: AnimationEvent) => void;
  onAnimationCancel: (event: AnimationEvent) => void;
} {
  const ready = boot.ready();
  const finishAnimation = (event: AnimationEvent) => {
    if (event.target === event.currentTarget && event.animationName === "den-enter-reveal") {
      boot.finishAnimation();
    }
  };
  return {
    classList: {
      "den-stage-boot": true,
      "den-enter-fade": boot.animate(),
    },
    "data-boot": ready ? "ready" : "pending",
    "aria-busy": !ready,
    "aria-hidden": !ready,
    inert: ready ? undefined : true,
    onAnimationEnd: finishAnimation,
    onAnimationCancel: finishAnimation,
  };
}
