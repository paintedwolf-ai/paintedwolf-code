/**
 * Velocity-based spring scroll for the transcript's jump to the latest message.
 * Attribution is recorded in THIRD_PARTY_NOTICES.md.
 */

import { isStreamScrollDebugEnabled } from "./den-scroll-debug.ts";
import { perfMark } from "./den-main-thread-perf.ts";
import {
  SCROLL_EPSILON_PX,
  scrollportMotionForViewport,
} from "../../platform/scrolling/scrollport-motion.ts";

const STREAM_SCROLL_SPRING = {
  damping: 0.86,
  stiffness: 0.045,
  mass: 1.3,
} as const;

const SIXTY_FPS_INTERVAL_MS = 1000 / 60;

type SpringRun = {
  rafId?: number;
  finish: (arrived: boolean) => void;
};

const runs = new WeakMap<HTMLElement, SpringRun>();

/** Stops an active glide; its promise resolves `false`. */
export function cancelStreamSpringScroll(stream: HTMLElement): void {
  runs.get(stream)?.finish(false);
}

export function isStreamSpringScrolling(stream: HTMLElement): boolean {
  return runs.has(stream);
}

/** Tracks a moving target; resolves `false` on interruption. */
export function streamSpringScrollTo(
  stream: HTMLElement,
  target: () => number,
): Promise<boolean> {
  cancelStreamSpringScroll(stream);
  return new Promise((resolve) => {
    let velocity = 0;
    let accumulated = 0;
    let lastTick = performance.now();
    let reflowFrames = 0;
    let worstReflowMs = 0;
    let totalReflowMs = 0;
    const run: SpringRun = {
      finish: (arrived) => {
        if (runs.get(stream) !== run) return;
        runs.delete(stream);
        if (run.rafId !== undefined) cancelAnimationFrame(run.rafId);
        if (reflowFrames > 0) {
          perfMark("scroll-reflow", {
            frames: reflowFrames,
            worst_ms: Math.round(worstReflowMs * 100) / 100,
            total_ms: Math.round(totalReflowMs),
          });
        }
        resolve(arrived);
      },
    };
    runs.set(stream, run);

    const tick = (now: number) => {
      if (runs.get(stream) !== run) return;
      run.rafId = undefined;
      // A render can tear the scrollport down mid-glide.
      const motion = scrollportMotionForViewport(stream);
      if (!motion) {
        run.finish(false);
        return;
      }
      const measureReflow = isStreamScrollDebugEnabled();
      const reflowStart = measureReflow ? performance.now() : 0;
      const goal = Math.max(0, target());
      const scrollTop = stream.scrollTop;
      if (measureReflow) {
        const reflowMs = performance.now() - reflowStart;
        reflowFrames += 1;
        totalReflowMs += reflowMs;
        if (reflowMs > worstReflowMs) worstReflowMs = reflowMs;
      }
      if (scrollTop >= goal - SCROLL_EPSILON_PX) {
        // The final commit removes subpixel drift.
        if (scrollTop !== goal) motion.commit(goal, "jump");
        run.finish(true);
        return;
      }
      const frames = (now - lastTick) / SIXTY_FPS_INTERVAL_MS;
      lastTick = now;
      const { damping, stiffness, mass } = STREAM_SCROLL_SPRING;
      velocity = (damping * velocity + stiffness * (goal - scrollTop)) / mass;
      accumulated += velocity * frames;
      motion.commit(scrollTop + accumulated, "jump");
      // Sub-pixel steps accumulate until the scrollport moves.
      if (stream.scrollTop !== scrollTop) accumulated = 0;
      run.rafId = requestAnimationFrame(tick);
    };
    run.rafId = requestAnimationFrame(tick);
  });
}
