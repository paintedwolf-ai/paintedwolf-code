import { prefersReducedMotion } from "../platform/interaction/reduced-motion.ts";

export const SELECTION_TRAVEL_MS = 180;
export const SELECTION_TRAVEL_EASING = "ease-out";

/** Snap when motion is reduced or animation is unavailable. */
export function shouldSnapSelectionTravel(el: HTMLElement): boolean {
  return prefersReducedMotion() || typeof el.animate !== "function";
}

/** Commits the final frame and releases the animation. */
export function playSelectionTravel(
  el: HTMLElement,
  frames: [Keyframe, Keyframe],
  commit: () => void,
): Animation {
  const animation = el.animate(frames, {
    duration: SELECTION_TRAVEL_MS,
    easing: SELECTION_TRAVEL_EASING,
    fill: "forwards",
  });
  animation.onfinish = () => {
    commit();
    animation.cancel();
  };
  return animation;
}
