// @vitest-environment jsdom
import { batch, createRoot, createSignal, createComponent } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  afterPaint,
  surfaceRevealDom,
  useSurfaceReveal,
  useStageHandoff,
  type SurfaceReveal,
} from "./surface-reveal.ts";
import { createPreparation } from "./presentation.ts";
import { PresentationProvider } from "./presentation-context.tsx";
let preparation = createPreparation();
function inPreparation(run: () => void) {
  createComponent(PresentationProvider, { preparation, get children() { run(); return null; } });
}


describe("useSurfaceReveal", () => {
  let nextFrameId = 1;
  let frameCallbacks = new Map<number, FrameRequestCallback>();

  const flushFrame = () => {
    const callbacks = [...frameCallbacks.values()];
    frameCallbacks = new Map();
    for (const callback of callbacks) callback(performance.now());
  };

  const flushPaint = () => {
    flushFrame();
    flushFrame();
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      const id = nextFrameId++;
      frameCallbacks.set(id, cb);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      frameCallbacks.delete(id);
    });
  });

  afterEach(() => {
    frameCallbacks.clear();
    preparation = createPreparation();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("paints steady when content is already there on mount", () => {
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal();
      return d;
    });
    expect(boot.ready()).toBe(true);
    expect(boot.animate()).toBe(false);
    expect(surfaceRevealDom(boot).classList["den-enter-fade"]).toBe(false);
    expect(surfaceRevealDom(boot)["aria-hidden"]).toBe(false);
    expect(surfaceRevealDom(boot).inert).toBeUndefined();
    dispose();
  });

  it("afterPaint uses setTimeout when the document is hidden", async () => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    const run = vi.fn();
    const cancel = afterPaint(run);
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledOnce();
    cancel();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
  });

  it("afterPaint runs after two frames and supports cancellation", () => {
    const kept = vi.fn();
    const dropped = vi.fn();
    afterPaint(kept);
    const cancelDropped = afterPaint(dropped);
    cancelDropped();

    flushFrame();
    expect(kept).not.toHaveBeenCalled();
    flushFrame();
    expect(kept).toHaveBeenCalledOnce();
    expect(dropped).not.toHaveBeenCalled();
  });

  it("reveals with a fade once content arrives and a paint passes", () => {
    const [ready, setReady] = createSignal(false);
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ ready });
      return d;
    });
    expect(boot.ready()).toBe(false);
    expect(surfaceRevealDom(boot)["aria-hidden"]).toBe(true);
    expect(surfaceRevealDom(boot).inert).toBe(true);
    setReady(true);
    expect(boot.ready()).toBe(false);
    flushPaint();
    expect(boot.ready()).toBe(true);
    expect(boot.animate()).toBe(true);
    dispose();
  });

  it("can defer ready-at-mount content so children may register holds", () => {
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({
        ready: () => true,
        deferInitialReveal: true,
      });
      return d;
    });
    expect(boot.ready()).toBe(false);
    flushPaint();
    expect(boot.ready()).toBe(true);
    dispose();
  });

  it.each([0, 1])("resets a pending reveal after %i frames without stranding ready content", (frames) => {
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ ready: () => true, deferInitialReveal: true });
      return d;
    });
    if (frames) flushFrame();
    boot.reset();
    boot.reset();
    expect(boot.ready()).toBe(false);
    flushFrame();
    expect(boot.ready()).toBe(false);
    flushFrame();
    expect(boot.ready()).toBe(true);
    expect(boot.animate()).toBe(true);
    dispose();
    expect(frameCallbacks.size).toBe(0);
  });

  it.each(["onAnimationEnd", "onAnimationCancel"] as const)("clears the entrance on %s without hiding the surface", (eventName) => {
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ deferInitialReveal: true });
      return d;
    });
    flushPaint();
    const root = document.createElement("div");
    const child = document.createElement("div");
    const finish = surfaceRevealDom(boot)[eventName];
    finish({ target: child, currentTarget: root, animationName: "den-enter-reveal" } as unknown as AnimationEvent);
    finish({ target: root, currentTarget: root, animationName: "other" } as unknown as AnimationEvent);
    expect(boot.animate()).toBe(true);
    finish({ target: root, currentTarget: root, animationName: "den-enter-reveal" } as unknown as AnimationEvent);
    expect(boot.animate()).toBe(false);
    expect(boot.ready()).toBe(true);
    boot.reset();
    flushPaint();
    expect(boot.animate()).toBe(true);
    dispose();
  });

  it("does not reveal through a paint that content went missing across", () => {
    const [ready, setReady] = createSignal(false);
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ ready });
      return d;
    });
    setReady(true);
    // Readiness spans the paint boundary.
    flushFrame();
    setReady(false);
    flushFrame();
    expect(boot.ready()).toBe(false);

    setReady(true);
    flushPaint();
    expect(boot.ready()).toBe(true);
    dispose();
  });

  it("stays pending while content is missing", () => {
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ ready: () => false });
      return d;
    });
    expect(boot.ready()).toBe(false);
    flushPaint();
    expect(boot.ready()).toBe(false);
    dispose();
  });

  it("holds workspace content by name while it is pending", () => {
    const [ready, setReady] = createSignal(false);
    const dispose = createRoot((d) => {
      inPreparation(() => useSurfaceReveal({ ready, name: "files-stage" }));
      return d;
    });
    expect(preparation.pending()).toEqual(["files-stage"]);
    setReady(true);
    flushPaint();
    expect(preparation.pending()).toEqual([]);
    dispose();
  });

  it("releases its workspace hold when the surface goes away", () => {
    const dispose = createRoot((d) => {
      inPreparation(() => useSurfaceReveal({ ready: () => false, name: "files-stage" }));
      return d;
    });
    expect(preparation.pending()).toEqual(["files-stage"]);
    dispose();
    expect(preparation.pending()).toEqual([]);
  });

  it("reset blanks again and re-runs the paint-gated fade", () => {
    const [ready, setReady] = createSignal(true);
    let boot!: SurfaceReveal;
    const dispose = createRoot((d) => {
      boot = useSurfaceReveal({ ready });
      return d;
    });
    expect(boot.ready()).toBe(true);
    batch(() => {
      setReady(false);
      boot.reset();
    });
    expect(boot.ready()).toBe(false);
    expect(boot.animate()).toBe(false);
    setReady(true);
    expect(boot.ready()).toBe(false);
    flushPaint();
    expect(boot.ready()).toBe(true);
    dispose();
  });
});

describe("useStageHandoff", () => {
  it("holds the outgoing stage until the incoming surface is visible", () => {
    const [stage, setStage] = createSignal<string | null>("files");
    const [chat, setChat] = createSignal<{ pending: boolean } | null>(null);
    const [isChat, setIsChat] = createSignal(false);
    let displayed!: () => string | null;
    const dispose = createRoot((d) => {
      displayed = useStageHandoff(stage, chat, isChat);
      return d;
    });

    expect(displayed()).toBe("files");
    batch(() => {
      setStage(null);
      setIsChat(true);
      setChat({ pending: true });
    });
    expect(displayed()).toBe("files");

    setChat({ pending: false });
    expect(displayed()).toBeNull();
    dispose();
  });
});
