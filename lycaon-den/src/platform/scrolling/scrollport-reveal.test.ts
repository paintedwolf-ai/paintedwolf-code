import { afterEach, expect, it, vi } from "vitest";
import { animateScrollportReveal } from "./scrollport-reveal.ts";

afterEach(() => vi.unstubAllGlobals());

function animationClock() {
  let next = 0, now = 0;
  const frames = new Map<number, FrameRequestCallback>();
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { frames.set(++next, callback); return next; });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  return { tick: (elapsed = 16) => {
    now += elapsed;
    const callbacks = [...frames.values()]; frames.clear();
    for (const callback of callbacks) callback(now);
  }, pending: () => frames.size };
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(yes => { resolve = yes; });
  return { promise, resolve };
}

it.each([300, 2000])("settles a %i px overrun gradually without overshooting", async (distance) => {
  const clock = animationClock();
  const published: number[] = [];
  const animation = animateScrollportReveal({
    target: () => 0, reducedMotion: false, motion: "settle",
    coordinates: { read: () => distance, publish: offset => { published.push(offset); } },
  });
  animation.begin();
  clock.tick();
  clock.tick();
  expect(published[1]).toBeLessThan(distance);
  expect(published[1]).toBeGreaterThan(distance * 0.99);
  for (let time = 32; time <= 224; time += 16) clock.tick();
  expect(published.at(-1)).toBeGreaterThan(0);
  for (let time = 240; time <= 672; time += 16) clock.tick();
  expect(await animation.finished).toBe(true);
  expect(published.at(-1)).toBe(0);
  expect(published.every((offset, index) => offset >= 0 && offset <= (published[index - 1] ?? distance))).toBe(true);
  expect(clock.pending()).toBe(0);
});

it("waits for one prepared viewport at a time without spending animation time on I/O", async () => {
  const clock = animationClock(), first = deferred(), second = deferred();
  const published: number[] = [], prepared: number[] = [];
  const animation = animateScrollportReveal({ target: () => 10_000_000, reducedMotion: false,
    coordinates: { read: () => 0, prepare: offset => {
      prepared.push(offset); return prepared.length === 1 ? first.promise : second.promise;
    }, publish: offset => { published.push(offset); } } });
  animation.begin(); clock.tick();
  for (let index = 0; index < 10; index++) clock.tick(1000);
  expect(prepared).toEqual([0]); expect(published).toEqual([]);
  first.resolve(); await Promise.resolve(); clock.tick(); clock.tick();
  expect(published).toEqual([0]);
  expect(prepared[1]).toBeGreaterThan(0);
  expect(prepared[1]).toBeLessThan(200_000);
  for (let index = 0; index < 10; index++) clock.tick(1000);
  expect(prepared).toHaveLength(2);
  second.resolve(); await Promise.resolve(); clock.tick();
  expect(published).toEqual(prepared);
  animation.cancel(); expect(await animation.finished).toBe(false);
  expect(clock.pending()).toBe(0);
});

it("cancels pending preparation and ignores a response that disregards its abort signal", async () => {
  const clock = animationClock(), pending = deferred(), publish = vi.fn(), complete = vi.fn();
  let signal!: AbortSignal;
  const controller = new AbortController();
  const animation = animateScrollportReveal({ target: () => 1000, reducedMotion: false, signal: controller.signal, complete,
    coordinates: { read: () => 0, prepare: (_offset, current) => { signal = current; return pending.promise; }, publish } });
  animation.begin(); clock.tick(); controller.abort();
  expect(signal.aborted).toBe(true); expect(await animation.finished).toBe(false);
  pending.resolve(); await Promise.resolve(); clock.tick();
  expect(publish).not.toHaveBeenCalled(); expect(complete).not.toHaveBeenCalled();
  expect(clock.pending()).toBe(0);
});

it.each([undefined, "settle"] as const)("prepares the reduced-motion destination before publishing it (%s)", async (motion) => {
  const clock = animationClock(), pending = deferred(), publish = vi.fn();
  const prepare = vi.fn(() => pending.promise);
  const animation = animateScrollportReveal({ target: () => 9_000_000, reducedMotion: true, motion,
    coordinates: { read: () => 0, prepare, publish } });
  animation.begin();
  expect(prepare).toHaveBeenCalledWith(9_000_000, expect.any(AbortSignal));
  expect(publish).not.toHaveBeenCalled();
  pending.resolve(); await Promise.resolve(); clock.tick();
  expect(await animation.finished).toBe(true);
  expect(publish).toHaveBeenCalledExactlyOnceWith(9_000_000);
});

it("reports a failed page without publishing an unprepared position", async () => {
  const clock = animationClock(), publish = vi.fn();
  const error = new Error("Page unavailable");
  const animation = animateScrollportReveal({ target: () => 1000, reducedMotion: false,
    coordinates: { read: () => 0, prepare: () => Promise.reject(error), publish } });
  const rejected = expect(animation.finished).rejects.toBe(error);
  animation.begin(); clock.tick(); await rejected;
  expect(publish).not.toHaveBeenCalled(); expect(clock.pending()).toBe(0);
});

it("does not schedule another frame after publication cancels the reveal", async () => {
  const clock = animationClock(), complete = vi.fn();
  const animation = animateScrollportReveal({ target: () => 1000, reducedMotion: false, complete,
    coordinates: { read: () => 0, publish: () => animation.cancel() } });
  animation.begin(); clock.tick();
  expect(await animation.finished).toBe(false);
  expect(complete).not.toHaveBeenCalled(); expect(clock.pending()).toBe(0);
});
