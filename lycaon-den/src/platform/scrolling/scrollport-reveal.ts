import { prefersReducedMotion } from "../interaction/reduced-motion.ts";

export type ScrollportRevealCoordinates = {
  read(): number;
  /** Prepare bounded content without moving the visible viewport. */
  prepare?(offset: number, signal: AbortSignal): void | Promise<void>;
  publish(offset: number): void;
};

export type ScrollportRevealOptions = {
  motion?: "settle";
  coordinates?: ScrollportRevealCoordinates;
  signal?: AbortSignal;
  complete?: () => void;
};

/** Animation time advances only while prepared viewports can be painted. */
export function animateScrollportReveal(options: {
  target: () => number;
  coordinates: ScrollportRevealCoordinates;
  reducedMotion: boolean;
  motion?: ScrollportRevealOptions["motion"];
  signal?: AbortSignal;
  complete?: () => void;
}) {
  const abort = new AbortController();
  let frame: number | undefined;
  let settled = false;
  let resolve!: (completed: boolean) => void;
  let reject!: (error: unknown) => void;
  const finished = new Promise<boolean>((yes, no) => { resolve = yes; reject = no; });
  const stop = () => {
    if (frame !== undefined) cancelAnimationFrame(frame);
    frame = undefined;
    options.signal?.removeEventListener("abort", cancel);
  };
  const cancel = () => {
    if (settled) return;
    settled = true;
    abort.abort(); stop(); resolve(false);
  };
  const fail = (error: unknown) => {
    if (settled) return;
    settled = true;
    abort.abort(); stop(); reject(error);
  };
  let start = options.coordinates.read();
  let target = Math.max(0, options.target());
  let previousFrame: number | undefined;
  let elapsed = 0;
  const publish = (offset: number, done: boolean) => {
    if (settled) return;
    options.coordinates.publish(offset);
    if (settled) return;
    if (done) {
      settled = true; stop();
      try { options.complete?.(); resolve(true); } catch (error) { reject(error); }
    } else frame = requestAnimationFrame(advance);
  };
  const prepare = (offset: number, done: boolean) => {
    try {
      const ready = options.coordinates.prepare?.(offset, abort.signal);
      if (!ready) { publish(offset, done); return; }
      void ready.then(() => {
        if (settled) return;
        frame = requestAnimationFrame(now => {
          frame = undefined;
          previousFrame = now;
          try { publish(offset, done); } catch (error) { fail(error); }
        });
      }).catch(fail);
    } catch (error) { fail(error); }
  };
  const advance = (now: number) => {
    frame = undefined;
    if (settled) return;
    try {
      if (previousFrame === undefined) {
        start = options.coordinates.read();
        target = Math.max(0, options.target());
      } else elapsed += Math.min(32, Math.max(0, now - previousFrame));
      previousFrame = now;
      const distance = Math.abs(target - start);
      const settling = options.motion === "settle";
      const duration = settling ? Math.min(650, 320 + distance * 0.2) : 220;
      const progress = distance <= 1 ? 1 : Math.min(1, elapsed / duration);
      // Settling starts and ends with zero velocity and acceleration.
      const eased = settling
        ? progress ** 3 * (10 + progress * (-15 + 6 * progress))
        : progress * progress * (3 - 2 * progress);
      prepare(start + (target - start) * eased, progress === 1);
    } catch (error) { fail(error); }
  };
  const begin = () => {
    options.signal?.addEventListener("abort", cancel, { once: true });
    if (options.signal?.aborted) cancel();
    else if (options.reducedMotion || Math.abs(target - start) <= 1) prepare(target, true);
    else frame = requestAnimationFrame(advance);
  };
  return { finished, cancel, begin };
}

/**
 * Glides a scroller's horizontal offset on the main thread; returns a cancel. The scroller
 * only takes jumps, so no native smooth scroll is left running under later writes.
 */
export function glideScrollLeft(
  element: HTMLElement,
  left: number,
  publish: (left: number) => void = (next) => { element.scrollLeft = next; },
): () => void {
  const glide = animateScrollportReveal({
    target: () => left,
    coordinates: { read: () => element.scrollLeft, publish },
    reducedMotion: prefersReducedMotion(element.ownerDocument.defaultView),
  });
  glide.begin();
  return glide.cancel;
}
