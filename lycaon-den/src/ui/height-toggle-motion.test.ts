// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SharedResizeListener } from "../layout/shared-resize-observer.ts";

const sharedBoxes = vi.hoisted(() => new Map<Element, SharedResizeListener>());
vi.mock("../layout/shared-resize-observer.ts", () => ({
  observeSharedContentBox: (el: Element, listener: SharedResizeListener) => {
    sharedBoxes.set(el, listener);
    return () => sharedBoxes.delete(el);
  },
}));
import {
  HEIGHT_TOGGLE_TIMING,
  animateHeightToggle,
  heightToggleTargetFor,
  followBodyHeight,
  type HeightMotionTiming,
} from "./height-toggle-motion.ts";

function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}

function animationDouble(shell: HTMLElement) {
  let latest: Animation | undefined;
  shell.animate = vi.fn(() => {
    latest = {
      cancel: vi.fn(),
      onfinish: null,
    } as unknown as Animation;
    return latest;
  });
  return {
    finish: () => {
      const animation = latest;
      if (!animation) throw new Error("height toggle did not start");
      animation.onfinish?.call(
        animation,
        new Event("finish") as AnimationPlaybackEvent,
      );
    },
    cancel: () => {
      const animation = latest;
      if (!animation) throw new Error("height toggle did not start");
      animation.oncancel?.call(
        animation,
        new Event("cancel") as AnimationPlaybackEvent,
      );
    },
  };
}

function surface() {
  document.head.innerHTML = `
    <style>
      .motion-surface { border-radius: 8px; }
    </style>
  `;
  const shell = document.createElement("div");
  shell.className = "motion-surface";
  Object.defineProperty(shell, "scrollHeight", { value: 180 });
  shell.getBoundingClientRect = () => ({ height: 28 }) as DOMRect;
  document.body.append(shell);
  return shell;
}

afterEach(() => {
  document.head.replaceChildren();
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe("height toggle motion", () => {
  it("locks the existing CSS corner for the full opening motion", async () => {
    const shell = surface();
    const animation = animationDouble(shell);

    animateHeightToggle(shell, {
      direction: "open",
      showBody: () => shell.setAttribute("data-open", ""),
      hideBody: () => {},
      closeBody: () => {},
      resetBodyVisibility: () => {},
      collapsedHeight: () => 28,
      bodyStillOpen: () => shell.hasAttribute("data-open"),
    });

    expect(shell.style.borderRadius).toBe("8px");
    expect(shell.style.clipPath).toBe("inset(0 round 8px)");
    expect(shell.style.overflow).toBe("hidden");
    expect(shell.hasAttribute("data-animating")).toBe(true);

    animation.finish();
    expect(shell.style.clipPath).toBe("inset(0 round 8px)");
    await nextFrame();

    expect(shell.style.borderRadius).toBe("");
    expect(shell.style.clipPath).toBe("");
    expect(getComputedStyle(shell).borderRadius).toBe("8px");
    expect(shell.hasAttribute("data-animating")).toBe(false);
  });

  it("keeps the same corner through closing and cleanup", async () => {
    const shell = surface();
    shell.setAttribute("data-open", "");
    const animation = animationDouble(shell);

    animateHeightToggle(shell, {
      direction: "close",
      showBody: () => {},
      hideBody: () => {},
      closeBody: () => shell.removeAttribute("data-open"),
      resetBodyVisibility: () => {},
      collapsedHeight: () => 28,
      bodyStillOpen: () => shell.hasAttribute("data-open"),
    });

    animation.finish();
    expect(getComputedStyle(shell).borderRadius).toBe("8px");
    expect(shell.style.clipPath).toBe("inset(0 round 8px)");
    await nextFrame();

    expect(shell.style.clipPath).toBe("");
    expect(getComputedStyle(shell).borderRadius).toBe("8px");
  });

  it("releases the layout lock when the browser cancels an animation", () => {
    const shell = surface();
    const animation = animationDouble(shell);
    const settled = vi.fn();

    animateHeightToggle(shell, {
      direction: "open",
      showBody: () => shell.setAttribute("data-open", ""),
      hideBody: () => {},
      closeBody: () => {},
      resetBodyVisibility: () => {},
      collapsedHeight: () => 28,
      bodyStillOpen: () => shell.hasAttribute("data-open"),
      onSettled: settled,
    });

    animation.cancel();

    expect(shell.style.height).toBe("");
    expect(shell.style.overflow).toBe("");
    expect(shell.hasAttribute("data-animating")).toBe(false);
    expect(settled).toHaveBeenCalledOnce();
  });

  it("lands at once without motion and settles after reactive bodies mount", async () => {
    const shell = surface();
    const settled = vi.fn();
    const steps: string[] = [];

    animateHeightToggle(shell, {
      direction: "close",
      showBody: () => steps.push("show"),
      hideBody: () => steps.push("hide"),
      closeBody: () => steps.push("close"),
      resetBodyVisibility: () => steps.push("reset"),
      collapsedHeight: () => 28,
      bodyStillOpen: () => false,
      onSettled: settled,
    });

    expect(steps).toEqual(["hide", "close", "reset"]);
    expect(shell.style.height).toBe("");
    expect(settled).not.toHaveBeenCalled();
    await Promise.resolve();
    expect(settled).toHaveBeenCalledOnce();
  });

  it("reports target state during height toggle animation", async () => {
    const shell = surface();
    expect(heightToggleTargetFor(shell)).toBeUndefined();

    let open = true;
    const animation = animationDouble(shell);
    animateHeightToggle(shell, {
      direction: "close",
      showBody: () => {},
      hideBody: () => {},
      closeBody: () => {
        open = false;
      },
      resetBodyVisibility: () => {},
      collapsedHeight: () => 28,
      bodyStillOpen: () => open,
    });

    expect(heightToggleTargetFor(shell)).toBe(false);

    animation.finish();
    await nextFrame();
    expect(heightToggleTargetFor(shell)).toBeUndefined();
  });
});

const DRAFT: HeightMotionTiming = { duration: 120, easing: "cubic-bezier(0.2, 0, 0, 1)" };

type AnimationStub = Animation & { keyframes: unknown; timing: KeyframeAnimationOptions };

function followSurface(initial: { shell: number; body: number }) {
  const shell = document.createElement("div");
  const body = document.createElement("div");
  shell.style.borderTop = "1px solid";
  shell.style.borderBottom = "1px solid";
  shell.append(body);
  document.body.append(shell);
  const heights = { ...initial };
  shell.getBoundingClientRect = () => ({ height: heights.shell }) as DOMRect;
  body.getBoundingClientRect = () => ({ height: heights.body, width: 400 }) as DOMRect;
  const animations: AnimationStub[] = [];
  shell.animate = vi.fn((keyframes, timing) => {
    const animation = {
      keyframes,
      timing,
      cancel: vi.fn(),
      onfinish: null,
      oncancel: null,
    } as unknown as AnimationStub;
    animations.push(animation);
    return animation;
  });
  return { shell, body, heights, animations };
}

function finish(animation: Animation) {
  animation.onfinish?.call(animation, new Event("finish") as AnimationPlaybackEvent);
}

describe("followed body height", () => {
  it("holds sibling geometry through the frame that measures and installs the animation", async () => {
    const { shell, body, heights } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);
    const locked = () => {
      expect(shell.style.minHeight).toBe("62px");
      expect(shell.style.maxHeight).toBe("62px");
    };
    const measure = body.getBoundingClientRect;
    body.getBoundingClientRect = () => { locked(); return measure.call(body); };
    const animate = shell.animate;
    shell.animate = (...args) => { locked(); return animate.apply(shell, args); };
    follow.change(DRAFT, () => { locked(); heights.body = 120; });
    // The pin holds until the frame has measured the body.
    locked();
    await nextFrame();
    expect(shell.style.minHeight).toBe("");
    expect(shell.style.maxHeight).toBe("");
    expect(() => follow.change(DRAFT, () => {
      throw new Error("mutation failed");
    })).toThrow("mutation failed");
    expect(shell.style.minHeight).toBe("");
    expect(shell.style.maxHeight).toBe("");
    follow.dispose();
  });

  it("eases from the rendered height to the body measured in the next frame", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);
    const measure = vi.spyOn(body, "getBoundingClientRect");
    let lockedDuringMutation = "";

    follow.change(DRAFT, () => {
      lockedDuringMutation = shell.style.minHeight;
      heights.body = 80;
    });

    expect(lockedDuringMutation).toBe("62px");
    // The mutation's own frame lays the body out once for every change.
    expect(measure).not.toHaveBeenCalled();
    await nextFrame();
    expect(measure).toHaveBeenCalledOnce();
    expect(shell.style.minHeight).toBe("");
    expect(animations).toHaveLength(1);
    expect(animations[0]!.keyframes).toEqual({ height: ["62px", "82px"] });
    expect(animations[0]!.timing).toEqual(DRAFT);
    expect(shell.style.overflow).toBe("clip");

    finish(animations[0]!);
    expect(shell.style.overflow).toBe("");
    follow.dispose();
  });

  it("measures once for every change in a frame and moves on the longer motion", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);
    const from = vi.spyOn(shell, "getBoundingClientRect");
    const measure = vi.spyOn(body, "getBoundingClientRect");

    // A send clears the draft and mounts the activity line in one frame.
    follow.change(DRAFT, () => { heights.body = 40; });
    follow.change(HEIGHT_TOGGLE_TIMING, () => { heights.body = 64; });
    expect(from).toHaveBeenCalledOnce();
    await nextFrame();

    expect(measure).toHaveBeenCalledOnce();
    expect(animations).toHaveLength(1);
    expect(animations[0]!.keyframes).toEqual({ height: ["62px", "66px"] });
    expect(animations[0]!.timing).toEqual(HEIGHT_TOGGLE_TIMING);
    follow.dispose();
  });

  it("skips measurement when a keystroke keeps the draft's height", async () => {
    const { shell, body, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);
    const measure = vi.spyOn(body, "getBoundingClientRect");

    follow.change(DRAFT, () => false);
    await nextFrame();

    expect(measure).not.toHaveBeenCalled();
    expect(animations).toHaveLength(0);
    expect(shell.style.minHeight).toBe("");
    follow.dispose();
  });

  it("keeps a running curve when the destination is unchanged", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);

    follow.change(DRAFT, () => {
      heights.body = 80;
    });
    await nextFrame();
    heights.shell = 70;
    follow.change(DRAFT, () => {});
    await nextFrame();

    expect(animations).toHaveLength(1);
    expect(animations[0]!.cancel).not.toHaveBeenCalled();
    follow.dispose();
  });

  it("retargets from the current height when the destination moves", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);

    follow.change(DRAFT, () => {
      heights.body = 80;
    });
    await nextFrame();
    heights.shell = 70;
    follow.change(DRAFT, () => {
      heights.body = 100;
    });
    await nextFrame();

    expect(animations).toHaveLength(2);
    expect(animations[0]!.cancel).toHaveBeenCalledOnce();
    expect(animations[1]!.keyframes).toEqual({ height: ["70px", "102px"] });

    // The superseded curve cannot release the clip the new one holds.
    finish(animations[0]!);
    expect(shell.style.overflow).toBe("clip");
    finish(animations[1]!);
    expect(shell.style.overflow).toBe("");
    follow.dispose();
  });

  it("retargets a running motion when the body changes underneath it", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);

    follow.change(DRAFT, () => {
      heights.body = 80;
    });
    await nextFrame();
    heights.shell = 70;
    // An attachment chip mounts mid-motion.
    sharedBoxes.get(body)?.({ width: 400, height: 102, borderWidth: 400, borderHeight: 102 });

    expect(animations).toHaveLength(2);
    expect(animations[1]!.keyframes).toEqual({ height: ["70px", "104px"] });
    expect(animations[1]!.timing).toEqual(DRAFT);
    follow.dispose();
  });

  it("ignores body changes while no motion runs", () => {
    const { shell, body, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);

    sharedBoxes.get(body)?.({ width: 400, height: 90, borderWidth: 400, borderHeight: 90 });

    expect(animations).toHaveLength(0);
    follow.dispose();
    expect(sharedBoxes.has(body)).toBe(false);
  });

  it("lands without motion for snaps, hidden shells, and reduced motion", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);
    const measure = vi.spyOn(body, "getBoundingClientRect");

    follow.snap(() => {
      heights.body = 90;
    });
    // Chrome that mounts in the same frame as a snap lands with it.
    follow.change(HEIGHT_TOGGLE_TIMING, () => {
      heights.body = 110;
    });
    expect(shell.style.minHeight).toBe("");
    await nextFrame();
    expect(measure).not.toHaveBeenCalled();
    expect(animations).toHaveLength(0);

    heights.shell = 0;
    follow.change(DRAFT, () => {
      heights.body = 120;
    });
    await nextFrame();
    expect(animations).toHaveLength(0);

    heights.shell = 122;
    const matchMedia = window.matchMedia;
    window.matchMedia = vi.fn(() => ({ matches: true }) as MediaQueryList);
    try {
      follow.change(DRAFT, () => {
        heights.body = 40;
      });
      await nextFrame();
    } finally {
      window.matchMedia = matchMedia;
    }
    expect(animations).toHaveLength(0);
    expect(shell.style.overflow).toBe("");
    follow.dispose();
  });

  it("releases a running motion and a pending measurement on dispose", async () => {
    const { shell, body, heights, animations } = followSurface({ shell: 62, body: 60 });
    const follow = followBodyHeight(shell, body);

    follow.change(DRAFT, () => {
      heights.body = 80;
    });
    await nextFrame();
    follow.change(DRAFT, () => {
      heights.body = 90;
    });
    follow.dispose();
    await nextFrame();

    expect(animations).toHaveLength(1);
    expect(animations[0]!.cancel).toHaveBeenCalledOnce();
    expect(shell.style.overflow).toBe("");
    expect(shell.style.minHeight).toBe("");
  });
});
