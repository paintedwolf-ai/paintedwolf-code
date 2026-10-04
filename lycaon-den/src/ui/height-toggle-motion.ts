import { observeSharedContentBox } from "../layout/shared-resize-observer.ts";
import { prefersReducedMotion } from "../platform/interaction/reduced-motion.ts";
import { scrollportMotionContaining } from "../platform/scrolling/scrollport-motion.ts";

const HEIGHT_TOGGLE_MS = 240;
const HEIGHT_TOGGLE_EASING = "cubic-bezier(0.4, 0, 0.2, 1)";

export type HeightMotionTiming = { duration: number; easing: string };

/** Shared timing keeps adjacent surfaces aligned with disclosures. */
export const HEIGHT_TOGGLE_TIMING: HeightMotionTiming = {
  duration: HEIGHT_TOGGLE_MS,
  easing: HEIGHT_TOGGLE_EASING,
};

/** Subpixel layout noise is ignored. */
const HEIGHT_EPSILON_PX = 0.5;

const heightToggleAnimations = new WeakMap<
  HTMLElement,
  {
    animation: Animation;
    opening: boolean;
    /** Settles a toggle a reversal replaced; the replacement keeps the lock and body state. */
    supersede: () => void;
  }
>();

export type HeightToggleDirection = "open" | "close";

export function isHeightToggleAnimating(shell: HTMLElement): boolean {
  return heightToggleAnimations.has(shell);
}

/** Target open state for an in-flight toggle animation; undefined when settled. */
export function heightToggleTargetFor(shell: HTMLElement): boolean | undefined {
  return heightToggleAnimations.get(shell)?.opening;
}

/** In-flight toggles reverse direction. */
export function heightToggleDirectionFor(
  shell: HTMLElement,
  isOpen: boolean,
): HeightToggleDirection {
  const previous = heightToggleAnimations.get(shell);
  if (previous) return previous.opening ? "close" : "open";
  return isOpen ? "close" : "open";
}

interface HeightToggleController {
  direction: HeightToggleDirection;
  showBody: () => void;
  hideBody: () => void;
  closeBody: () => void;
  resetBodyVisibility: () => void;
  collapsedHeight: () => number;
  bodyStillOpen: () => boolean;
  opacity?: HeightToggleOpacity;
  duration?: number;
  easing?: string;
  onSettled?: () => void;
}

interface HeightToggleOpacity {
  start: number;
  end: number;
  window: [number, number];
}

function heightKeyframes(
  startHeight: number,
  endHeight: number,
  opacity: HeightToggleOpacity,
): Keyframe[] {
  const [fadeFrom, fadeTo] = opacity.window;
  const frames: Keyframe[] = [
    { height: `${startHeight}px`, opacity: opacity.start, offset: 0 },
  ];
  if (fadeFrom > 0) frames.push({ opacity: opacity.start, offset: fadeFrom });
  if (fadeTo < 1) frames.push({ opacity: opacity.end, offset: fadeTo });
  frames.push({ height: `${endHeight}px`, opacity: opacity.end, offset: 1 });
  return frames;
}

function computedRadius(shell: HTMLElement): string {
  return (
    shell.ownerDocument.defaultView?.getComputedStyle(shell).borderRadius || "0px"
  );
}

function roundedInset(radius: string): string {
  return `inset(0 round ${radius})`;
}

function unlockShell(shell: HTMLElement): void {
  shell.style.removeProperty("height");
  shell.style.removeProperty("overflow");
  shell.style.removeProperty("border-radius");
  shell.style.removeProperty("clip-path");
  delete shell.dataset.closing;
  delete shell.dataset.animating;
}

/** Opens or closes a disclosure shell and declares the change to its scrollport. */
export function animateHeightToggle(
  shell: HTMLElement,
  ctrl: HeightToggleController,
): void {
  const previous = heightToggleAnimations.get(shell);
  const opening = ctrl.direction === "open";
  if (previous && previous.opening === opening) return;

  const scrollport = scrollportMotionContaining(shell);
  if (typeof shell.animate !== "function" || prefersReducedMotion(shell.ownerDocument.defaultView)) {
    // A collapse states its contraction before it lands, so its scrollport can keep the range.
    const change = scrollport?.declareHeightChange(shell, {
      contraction: opening ? 0 : Math.max(0, shell.getBoundingClientRect().height - ctrl.collapsedHeight()),
    });
    if (previous) {
      previous.supersede();
      unlockShell(shell);
    }
    if (opening) {
      ctrl.showBody();
    } else {
      ctrl.hideBody();
      ctrl.closeBody();
    }
    ctrl.resetBodyVisibility();
    change?.end();
    // Reactive bodies mount before the settled layout is read.
    queueMicrotask(() => ctrl.onSettled?.());
    return;
  }

  const startHeight = shell.getBoundingClientRect().height;
  const radius = computedRadius(shell);
  // Read before the lock writes, so a collapse shares the start height's layout.
  const collapsedHeight = opening ? 0 : ctrl.collapsedHeight();

  shell.dataset.animating = "true";
  shell.style.height = `${startHeight}px`;
  shell.style.overflow = "hidden";
  shell.style.borderRadius = radius;
  // The clip keeps rounded overflow stable during interpolation.
  shell.style.clipPath = roundedInset(radius);
  // A reversal cancels the old animation under the new lock. Cancelling first would
  // restore the old start height while the scrollport resizes its held range.
  previous?.supersede();

  if (opening) {
    delete shell.dataset.closing;
    ctrl.showBody();
  } else {
    shell.dataset.closing = "true";
  }

  const endHeight = opening ? shell.scrollHeight : collapsedHeight;
  // Declared before the first frame, with the contraction a collapse will make.
  const change = scrollport?.declareHeightChange(shell, { contraction: Math.max(0, startHeight - endHeight) });
  const opacity = ctrl.opacity;
  const keyframes: Keyframe[] | PropertyIndexedKeyframes = opacity
    ? heightKeyframes(startHeight, endHeight, opacity)
    : { height: [`${startHeight}px`, `${endHeight}px`] };

  const animation = shell.animate(keyframes, {
    duration: ctrl.duration ?? HEIGHT_TOGGLE_MS,
    easing: ctrl.easing ?? HEIGHT_TOGGLE_EASING,
    fill: "forwards",
  });
  const supersede = () => {
    animation.oncancel = null;
    animation.onfinish = null;
    animation.cancel();
    heightToggleAnimations.delete(shell);
    change?.end();
    ctrl.onSettled?.();
  };
  const state = { animation, opening, supersede };
  heightToggleAnimations.set(shell, state);

  const releaseLock = () => {
    unlockShell(shell);
    ctrl.resetBodyVisibility();
    heightToggleAnimations.delete(shell);
    change?.end();
    ctrl.onSettled?.();
  };

  animation.oncancel = () => {
    if (heightToggleAnimations.get(shell) !== state) return;
    if (!opening) {
      ctrl.hideBody();
      ctrl.closeBody();
    }
    releaseLock();
  };

  animation.onfinish = () => {
    if (heightToggleAnimations.get(shell) !== state) return;
    shell.style.height = `${endHeight}px`;
    animation.oncancel = null;
    animation.cancel();

    if (!opening) {
      ctrl.hideBody();
      ctrl.closeBody();
    }

    const unlock = () => {
      if (heightToggleAnimations.get(shell) !== state) return;
      if (!opening && ctrl.bodyStillOpen()) {
        ctrl.hideBody();
        ctrl.closeBody();
        requestAnimationFrame(unlock);
        return;
      }
      releaseLock();
    };
    requestAnimationFrame(unlock);
  };
}

export interface HeightFollow {
  /** Coalesces height changes per frame; false preserves the current animation. */
  change(timing: HeightMotionTiming, mutate: () => boolean | void): void;
  /** Suppresses motion for body changes until the next frame. */
  snap(mutate: () => void): void;
  dispose(): void;
}

/** The unpadded shell pins its height during edits; its body stays bottom-aligned. */
export function followBodyHeight(shell: HTMLElement, body: HTMLElement): HeightFollow {
  const view = shell.ownerDocument.defaultView;
  let motion:
    | { animation: Animation; to: number; timing: HeightMotionTiming; bodyWidth: number }
    | undefined;
  let settleFrame: number | undefined;
  let quietFrame: number | undefined;
  /** Changes since the last frame, pinned at the height they started from. */
  let pending: { from: number; timing: HeightMotionTiming; frame: number } | undefined;

  const pin = (height: number) => {
    // Pinned height keeps siblings still while the body changes.
    shell.style.minHeight = `${height}px`;
    shell.style.maxHeight = `${height}px`;
    shell.style.overflow = "clip";
  };
  const unpin = () => {
    shell.style.removeProperty("min-height");
    shell.style.removeProperty("max-height");
    // A running motion holds the clip until it lands.
    if (!motion) shell.style.removeProperty("overflow");
  };
  const dropPending = () => {
    if (!pending) return;
    cancelAnimationFrame(pending.frame);
    pending = undefined;
    unpin();
  };

  const edges = () => {
    const style = view?.getComputedStyle(shell);
    if (!style) return 0;
    return (
      (Number.parseFloat(style.borderTopWidth) || 0) +
      (Number.parseFloat(style.borderBottomWidth) || 0)
    );
  };

  const stop = (current: { animation: Animation } | undefined) => {
    if (!current) return;
    current.animation.onfinish = null;
    current.animation.oncancel = null;
    current.animation.cancel();
  };

  const settle = () => {
    const current = motion;
    if (!current) return;
    motion = undefined;
    stop(current);
    shell.style.removeProperty("overflow");
  };

  const ease = (from: number, to: number, timing: HeightMotionTiming, bodyWidth: number) => {
    // An unchanged target preserves the running curve.
    if (motion && Math.abs(motion.to - to) < HEIGHT_EPSILON_PX) return;
    if (
      quietFrame !== undefined ||
      Math.abs(from - to) < HEIGHT_EPSILON_PX ||
      from <= 0 ||
      typeof shell.animate !== "function" ||
      shell.ownerDocument.hidden ||
      prefersReducedMotion(view)
    ) {
      settle();
      return;
    }
    stop(motion);
    const animation = shell.animate(
      { height: [`${from}px`, `${to}px`] },
      { duration: timing.duration, easing: timing.easing },
    );
    motion = { animation, to, timing, bodyWidth };
    shell.style.overflow = "clip";
    const release = () => {
      if (motion?.animation !== animation) return;
      motion = undefined;
      shell.style.removeProperty("overflow");
    };
    animation.onfinish = release;
    animation.oncancel = release;
  };

  // Body changes retarget the running animation.
  const stopObserving = observeSharedContentBox(body, (box) => {
    const current = motion;
    if (!current) return;
    const width = box.borderWidth ?? box.width;
    if (Math.abs(width - current.bodyWidth) >= HEIGHT_EPSILON_PX) {
      // Ancestor resize observers finish before settlement.
      settleFrame ??= requestAnimationFrame(() => {
        settleFrame = undefined;
        settle();
      });
      return;
    }
    ease(
      shell.getBoundingClientRect().height,
      (box.borderHeight ?? box.height) + edges(),
      current.timing,
      width,
    );
  });

  // One body measurement per frame, after every change in that frame has mutated.
  const measurePending = () => {
    const current = pending;
    pending = undefined;
    if (!current) return;
    const measured = body.getBoundingClientRect();
    ease(current.from, measured.height + edges(), current.timing, measured.width);
    unpin();
  };

  return {
    change(timing, mutate) {
      // Chrome that mounts in the same frame as a snap lands with it.
      if (quietFrame !== undefined) {
        mutate();
        return;
      }
      const current = pending;
      const from = current?.from ?? shell.getBoundingClientRect().height;
      if (!current) pin(from);
      let changed: boolean;
      try {
        changed = mutate() !== false;
      } catch (error) {
        if (!current) unpin();
        throw error;
      }
      if (!changed) {
        if (!current) unpin();
        return;
      }
      if (current) {
        // Coalesced changes move on the longer motion.
        if (timing.duration > current.timing.duration) current.timing = timing;
        return;
      }
      pending = { from, timing, frame: requestAnimationFrame(measurePending) };
    },
    snap(mutate) {
      dropPending();
      settle();
      mutate();
      if (quietFrame !== undefined || typeof requestAnimationFrame === "undefined") return;
      quietFrame = requestAnimationFrame(() => {
        quietFrame = undefined;
      });
    },
    dispose() {
      stopObserving();
      dropPending();
      if (settleFrame !== undefined) cancelAnimationFrame(settleFrame);
      if (quietFrame !== undefined) cancelAnimationFrame(quietFrame);
      settle();
    },
  };
}
