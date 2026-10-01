// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const declared = vi.hoisted(() => ({ started: 0, ended: 0, endedTwice: 0, contractions: [] as number[] }));
vi.mock("../platform/scrolling/scrollport-motion.ts", () => ({
  scrollportMotionContaining: () => ({
    declareHeightChange: (_shell: HTMLElement, opts?: { contraction?: number }) => {
      declared.started += 1;
      declared.contractions.push(opts?.contraction ?? 0);
      let ended = false;
      return {
        end: () => {
          if (ended) declared.endedTwice += 1;
          else declared.ended += 1;
          ended = true;
        },
      };
    },
  }),
}));
import { animateHeightToggle, heightToggleTargetFor } from "./height-toggle-motion.ts";

/** Deterministic sequences, so a failing seed replays exactly. */
function random(seed: number): () => number {
  let state = seed >>> 0 || 1;
  return () => {
    state ^= state << 13;
    state ^= state >>> 17;
    state ^= state << 5;
    return (state >>> 0) / 0x1_0000_0000;
  };
}

type Motion = { onfinish: (() => void) | null; oncancel: (() => void) | null; done: boolean; cancel: () => void };

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
  declared.started = 0;
  declared.ended = 0;
  declared.endedTwice = 0;
  declared.contractions = [];
});

describe("height toggle declaration", () => {
  it.each([["animated", false], ["instant", true]])("a %s collapse declares its whole contraction before it lands", (_kind, instant) => {
    vi.stubGlobal("requestAnimationFrame", () => 0);
    const shell = document.createElement("div");
    let height = 240;
    shell.getBoundingClientRect = () => ({ height }) as DOMRect;
    document.body.append(shell);
    shell.animate = instant
      ? (undefined as unknown as HTMLElement["animate"])
      : ((() => ({ onfinish: null, oncancel: null, cancel: () => {} })) as unknown as HTMLElement["animate"]);
    animateHeightToggle(shell, {
      direction: "close",
      showBody: () => {},
      hideBody: () => { height = 40; },
      closeBody: () => {},
      resetBodyVisibility: () => {},
      collapsedHeight: () => 40,
      bodyStillOpen: () => false,
    });
    expect(declared.contractions).toEqual([200]);
  });

  it("an opening declares no contraction", () => {
    vi.stubGlobal("requestAnimationFrame", () => 0);
    const shell = document.createElement("div");
    shell.getBoundingClientRect = () => ({ height: 40 }) as DOMRect;
    Object.defineProperty(shell, "scrollHeight", { value: 240 });
    document.body.append(shell);
    shell.animate = (() => ({ onfinish: null, oncancel: null, cancel: () => {} })) as unknown as HTMLElement["animate"];
    animateHeightToggle(shell, {
      direction: "open",
      showBody: () => {},
      hideBody: () => {},
      closeBody: () => {},
      resetBodyVisibility: () => {},
      collapsedHeight: () => 40,
      bodyStillOpen: () => true,
    });
    expect(declared.contractions).toEqual([0]);
  });
});

/** Each accepted toggle settles and ends its declared change exactly once, in any interleaving. */
describe("height toggle lifecycle", () => {
  it.each(Array.from({ length: 150 }, (_, seed) => seed + 1))("settles every accepted toggle once (seed %i)", async (seed) => {
    const next = random(seed);
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => frames.push(callback));
    vi.stubGlobal("cancelAnimationFrame", () => {});
    const shell = document.createElement("div");
    shell.getBoundingClientRect = () => ({ height: 40 }) as DOMRect;
    Object.defineProperty(shell, "scrollHeight", { value: 200 });
    document.body.append(shell);

    const motions: Motion[] = [];
    const animate = (() => {
      const motion: Motion = {
        onfinish: null,
        oncancel: null,
        done: false,
        // Browsers deliver the cancel event after the call returns.
        cancel: () => {
          if (motion.done) return;
          motion.done = true;
          queueMicrotask(() => motion.oncancel?.());
        },
      };
      motions.push(motion);
      return motion;
    }) as unknown as HTMLElement["animate"];

    let bodyOpen = false;
    const settled: number[] = [];
    let accepted = 0;
    const toggle = (instant: boolean) => {
      const opening = next() < 0.5;
      if (heightToggleTargetFor(shell) === opening) return;
      const id = accepted++;
      settled[id] = 0;
      shell.animate = instant ? (undefined as unknown as HTMLElement["animate"]) : animate;
      animateHeightToggle(shell, {
        direction: opening ? "open" : "close",
        showBody: () => { bodyOpen = true; },
        hideBody: () => {},
        closeBody: () => { bodyOpen = false; },
        resetBodyVisibility: () => {},
        collapsedHeight: () => 40,
        bodyStillOpen: () => bodyOpen,
        onSettled: () => { settled[id] = (settled[id] ?? 0) + 1; },
      });
    };
    const live = () => motions.filter((motion) => !motion.done);
    const finishLatest = () => {
      const motion = live().at(-1);
      if (!motion) return;
      motion.done = true;
      motion.onfinish?.();
    };
    const frame = () => frames.splice(0).forEach((callback) => callback(0));
    const microtasks = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

    for (let step = 0; step < 40; step += 1) {
      const roll = next();
      if (roll < 0.35) toggle(roll < 0.05);
      else if (roll < 0.55) finishLatest();
      else if (roll < 0.65) live().at(-1)?.cancel();
      else if (roll < 0.85) frame();
      else await microtasks();
    }
    for (let round = 0; round < 10; round += 1) {
      finishLatest();
      frame();
      await microtasks();
    }

    expect(settled.filter((count) => count !== 1), `seed ${seed}: settle counts ${settled.join(",")}`).toEqual([]);
    expect(declared.endedTwice).toBe(0);
    expect(declared.ended).toBe(declared.started);
    expect(heightToggleTargetFor(shell)).toBeUndefined();
    expect(shell.style.height).toBe("");
    expect(shell.hasAttribute("data-animating")).toBe(false);
  });
});
