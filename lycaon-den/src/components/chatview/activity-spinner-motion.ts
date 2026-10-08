import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";

/** Seek the existing CSS keyframes so visible frames also commit the spinner pose. */
export function driveActivitySpinner(element: HTMLElement): () => void {
  if (typeof element.getAnimations !== "function") return () => undefined;
  let frame: number | undefined;
  let spin: Animation | undefined;
  let origin = 0;
  let stopped = false;
  const cancel = () => {
    if (frame !== undefined) cancelAnimationFrame(frame);
    frame = undefined;
  };
  const tick = (now: number) => {
    frame = undefined;
    if (stopped || document.visibilityState !== "visible") return;
    if (prefersReducedMotion()) return;
    spin ??= element.getAnimations?.().find((animation) =>
      (animation as CSSAnimation).animationName === "den-composer-activity-spin");
    if (spin) {
      spin.pause();
      spin.currentTime = now - origin;
    }
    frame = requestAnimationFrame(tick);
  };
  const arm = () => {
    cancel();
    spin?.play();
    spin = undefined;
    origin = performance.now();
    if (!stopped && !prefersReducedMotion() && document.visibilityState === "visible") {
      frame = requestAnimationFrame(tick);
    }
  };
  const media = window.matchMedia?.("(prefers-reduced-motion: reduce)");
  document.addEventListener("visibilitychange", arm);
  window.addEventListener("focus", arm);
  media?.addEventListener("change", arm);
  arm();
  return () => {
    stopped = true;
    cancel();
    spin?.play();
    document.removeEventListener("visibilitychange", arm);
    window.removeEventListener("focus", arm);
    media?.removeEventListener("change", arm);
  };
}
