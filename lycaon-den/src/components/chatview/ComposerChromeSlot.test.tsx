import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { render, screen } from "@solidjs/testing-library";
import {
  COMPOSER_CHROME_SLOT_ANIMATION_MS,
  ComposerChromeSlot,
} from "./ComposerChromeSlot.tsx";
import { ComposerChromeStack } from "./ComposerChromeStack.tsx";

function flushRaf() {
  return new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

function createMockElementAnimations() {
  const animations: {
    onfinish: ((this: Animation, ev: AnimationPlaybackEvent) => void) | null;
    cancel: ReturnType<typeof vi.fn>;
  }[] = [];
  const animate = vi.fn((_keyframes: unknown, _options: unknown) => {
    const record = {
      onfinish: null as ((this: Animation, ev: AnimationPlaybackEvent) => void) | null,
      cancel: vi.fn(),
    };
    const anim = {
      cancel: record.cancel,
      set onfinish(handler: typeof record.onfinish) {
        record.onfinish = handler;
      },
      get onfinish() {
        return record.onfinish;
      },
    } as unknown as Animation;
    animations.push(record);
    return anim;
  });
  const originalAnimate = Object.getOwnPropertyDescriptor(Element.prototype, "animate");
  const originalGetAnimations = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");
  Object.defineProperty(Element.prototype, "animate", {
    configurable: true,
    writable: true,
    value: animate,
  });
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    writable: true,
    value: () => [],
  });
  return {
    animate,
    animations,
    finish: (index: number) => animations[index]?.onfinish,
    cancel: (index: number) => animations[index]?.cancel,
    restore: () => {
      if (originalAnimate) {
        Object.defineProperty(Element.prototype, "animate", originalAnimate);
      } else {
        delete (Element.prototype as any).animate;
      }
      if (originalGetAnimations) {
        Object.defineProperty(Element.prototype, "getAnimations", originalGetAnimations);
      } else {
        delete (Element.prototype as any).getAnimations;
      }
    },
  };
}

let mockElementAnimations: ReturnType<typeof createMockElementAnimations> | null = null;

beforeEach(() => {
  mockElementAnimations = createMockElementAnimations();
});

afterEach(() => {
  mockElementAnimations?.restore();
  mockElementAnimations = null;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe("ComposerChromeSlot", () => {
  it("mounts when present and unmounts after exit animation", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="arm" present={present()}>
        <span data-testid="slot-body">armed</span>
      </ComposerChromeSlot>
    ));
    expect(screen.getByTestId("composer-chrome-slot-arm")).toBeTruthy();
    expect(screen.getByTestId("slot-body")).toBeTruthy();

    setPresent(false);
    const slot = screen.getByTestId("composer-chrome-slot-arm");
    expect(slot.dataset.animating).toBe("true");
    expect(slot.dataset.closing).toBe("true");
    expect(slot.style.height).toBe("0px");
    expect(slot.style.overflow).toBe("hidden");
    expect(mock.animate).toHaveBeenLastCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ opacity: 1 }),
        expect.objectContaining({ opacity: 0 }),
      ]),
      expect.objectContaining({ duration: COMPOSER_CHROME_SLOT_ANIMATION_MS, fill: "forwards" }),
    );

    mock.finish(0)?.call(
      {} as Animation,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await flushRaf();
    expect(screen.queryByTestId("composer-chrome-slot-arm")).toBeNull();
  });

  it("ignores store churn behind an unchanged present while opening", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(false);
    // The presence value stays stable while its inputs settle.
    const [churn, setChurn] = createSignal(0);
    render(() => (
      <ComposerChromeSlot slot="arm" present={(churn(), present())}>
        <span data-testid="slot-body">armed</span>
      </ComposerChromeSlot>
    ));

    // Unchanged presence preserves the pending open frame.
    setPresent(true);
    setChurn(1);
    await flushRaf();
    expect(mock.animate).toHaveBeenCalledTimes(1);

    setChurn(2);
    setChurn(3);
    await flushRaf();

    expect(mock.animate).toHaveBeenCalledTimes(1);
    const slot = screen.getByTestId("composer-chrome-slot-arm");
    expect(slot.dataset.closing).toBeUndefined();
    expect(screen.getByTestId("slot-body")).toBeTruthy();
  });

  it("expands from collapsed when present flips true", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ComposerChromeSlot slot="ask_user" present={present()}>
        <span data-testid="slot-body">ask</span>
      </ComposerChromeSlot>
    ));
    expect(screen.queryByTestId("composer-chrome-slot-ask_user")).toBeNull();

    setPresent(true);
    await flushRaf();
    const slot = screen.getByTestId("composer-chrome-slot-ask_user");
    expect(slot.dataset.animating).toBe("true");
    expect(slot.style.height).toBe("0px");
    expect(slot.style.overflow).toBe("hidden");
    expect(mock.animate).toHaveBeenLastCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ opacity: 0 }),
        expect.objectContaining({ opacity: 1 }),
      ]),
      expect.objectContaining({ duration: COMPOSER_CHROME_SLOT_ANIMATION_MS, fill: "forwards" }),
    );

    mock.finish(0)?.call(
      {} as Animation,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await flushRaf();
    expect(slot.dataset.animating).toBeUndefined();
    expect(slot.dataset.collapsed).toBeUndefined();
    expect(screen.getByTestId("slot-body")).toBeTruthy();
  });

  it("offsets the fade from the height in both directions", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ComposerChromeSlot slot="checkpoint" present={present()}>
        <span data-testid="slot-body">approval</span>
      </ComposerChromeSlot>
    ));

    // Opening reserves height before the fade.
    setPresent(true);
    await flushRaf();
    const opening = mock.animate.mock.calls[0]?.[0] as Keyframe[];
    expect(opening[0]).toMatchObject({ opacity: 0, offset: 0 });
    expect(opening).toContainEqual({ opacity: 0, offset: 0.35 });
    expect(opening[opening.length - 1]).toMatchObject({ opacity: 1, offset: 1 });
    // Height spans the full timeline.
    expect(opening.filter((frame) => "height" in frame)).toHaveLength(2);

    mock.finish(0)?.call({} as Animation, new Event("finish") as AnimationPlaybackEvent);
    await flushRaf();

    // Closing fades before collapsing height.
    setPresent(false);
    const closing = mock.animate.mock.calls[1]?.[0] as Keyframe[];
    expect(closing[0]).toMatchObject({ opacity: 1, offset: 0 });
    expect(closing).toContainEqual({ opacity: 0, offset: 0.55 });
    expect(closing[closing.length - 1]).toMatchObject({ opacity: 0, offset: 1 });
    expect(closing.filter((frame) => "height" in frame)).toHaveLength(2);
  });

  it("cancels exit when present flips true while collapsing", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="find" present={present()}>
        <span data-testid="slot-body">find</span>
      </ComposerChromeSlot>
    ));

    setPresent(false);
    expect(mock.animate).toHaveBeenCalledTimes(1);

    setPresent(true);
    await flushRaf();
    expect(mock.cancel(0)).toHaveBeenCalled();
    expect(mock.animate).toHaveBeenCalledTimes(2);

    mock.finish(1)?.call(
      {} as Animation,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await flushRaf();
    expect(screen.getByTestId("composer-chrome-slot-find")).toBeTruthy();
    expect(
      screen.getByTestId("composer-chrome-slot-find").getAttribute("data-collapsed"),
    ).toBeNull();
  });

  it("fade slot is simply there when present — no entry animation", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ComposerChromeSlot slot="launcher" motion="fade" present={present()}>
        <span data-testid="slot-body">tiles</span>
      </ComposerChromeSlot>
    ));
    expect(screen.queryByTestId("composer-chrome-slot-launcher")).toBeNull();

    setPresent(true);
    await flushRaf();
    const slot = screen.getByTestId("composer-chrome-slot-launcher");
    // Entry mounts without a transition.
    expect(mock.animate).not.toHaveBeenCalled();
    expect(slot.dataset.collapsed).toBeUndefined();
    expect(slot.dataset.animating).toBeUndefined();
    expect(slot.style.height).toBe("");
    expect(screen.getByTestId("slot-body")).toBeTruthy();
  });

  it("fade slot exits with an opacity-only fade, then unmounts", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="launcher" motion="fade" present={present()}>
        <span data-testid="slot-body">tiles</span>
      </ComposerChromeSlot>
    ));
    const slot = screen.getByTestId("composer-chrome-slot-launcher");

    setPresent(false);
    // Fade-only slots keep their height unlocked.
    expect(mock.animate).toHaveBeenCalledWith(
      { opacity: [1, 0] },
      expect.objectContaining({
        duration: COMPOSER_CHROME_SLOT_ANIMATION_MS,
        fill: "forwards",
      }),
    );
    expect(slot.style.height).toBe("");
    expect(slot.style.overflow).toBe("");
    expect(slot.dataset.animating).toBeUndefined();
    expect(screen.getByTestId("slot-body")).toBeTruthy();

    mock.finish(0)?.call(
      {} as Animation,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await flushRaf();
    expect(screen.queryByTestId("composer-chrome-slot-launcher")).toBeNull();
  });

  it("fade slot re-present mid-exit cancels the fade and stays", async () => {
    const mock = mockElementAnimations!;
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="launcher" motion="fade" present={present()}>
        <span data-testid="slot-body">tiles</span>
      </ComposerChromeSlot>
    ));

    setPresent(false);
    expect(mock.animate).toHaveBeenCalledTimes(1);

    setPresent(true);
    await flushRaf();
    // Canceling the fill restores full opacity.
    expect(mock.cancel(0)).toHaveBeenCalled();
    expect(mock.animate).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("composer-chrome-slot-launcher")).toBeTruthy();

    // A canceled finish cannot unmount the active slot.
    mock.finish(0)?.call(
      {} as Animation,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await flushRaf();
    expect(screen.getByTestId("composer-chrome-slot-launcher")).toBeTruthy();
  });

  it("fade slot unmounts synchronously under prefers-reduced-motion", () => {
    const matchMedia = vi.fn().mockReturnValue({ matches: true });
    vi.stubGlobal("matchMedia", matchMedia);
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="launcher" motion="fade" present={present()}>
        <span data-testid="slot-body">tiles</span>
      </ComposerChromeSlot>
    ));
    setPresent(false);
    expect(screen.queryByTestId("composer-chrome-slot-launcher")).toBeNull();
    vi.unstubAllGlobals();
  });

  it("unmounts synchronously under prefers-reduced-motion", () => {
    const matchMedia = vi.fn().mockReturnValue({ matches: true });
    vi.stubGlobal("matchMedia", matchMedia);
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="ask_user" present={present()}>
        <span data-testid="slot-body">ask</span>
      </ComposerChromeSlot>
    ));
    setPresent(false);
    expect(screen.queryByTestId("composer-chrome-slot-ask_user")).toBeNull();
    vi.unstubAllGlobals();
  });

  it("removes a slot at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true }));
    const [present, setPresent] = createSignal(true);
    render(() => (
      <ComposerChromeSlot slot="checkpoint" present={present()}>
        <span>approval</span>
      </ComposerChromeSlot>
    ));

    setPresent(false);
    expect(screen.queryByTestId("composer-chrome-slot-checkpoint")).toBeNull();
    vi.unstubAllGlobals();
  });
});

describe("ComposerChromeStack", () => {
  it("renders locked slot order above the composer", () => {
    createRoot((dispose) => {
      render(() => (
        <ComposerChromeStack
          active
          queue={<div data-testid="queue-slot" />}
          launcher={{
            present: () => true,
            children: () => <div data-testid="launcher-body" />,
          }}
          arm={{ present: () => true, children: () => <div data-testid="arm-body" /> }}
          checkpoint={{
            present: () => true,
            children: () => <div data-testid="checkpoint-body" />,
          }}
          ask={{ present: () => true, children: () => <div data-testid="ask-body" /> }}
          composer={<div data-testid="composer-body" />}
        />
      ));
      const stack = screen.getByTestId("composer-chrome-stack");
      const slots = [
        ...stack.querySelectorAll("[data-slot]"),
      ].map((el) => el.getAttribute("data-slot"));
      // Find mounts only in the composer placement.
      expect(slots).toEqual(["launcher", "arm", "checkpoint", "ask_user"]);
      expect(screen.getByTestId("composer-body")).toBeTruthy();
      dispose();
    });
  });

  it("mounts find slot above composer when chat-scoped find is open", async () => {
    const { openFind, registerFindableView, setPrimaryFindableView, resetFindControllerForTests } =
      await import("../../find/find-controller.ts");
    resetFindControllerForTests();
    const root = document.createElement("div");
    document.body.appendChild(root);
    registerFindableView({
      id: "session-transcript",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("session-transcript");
    openFind();

    render(() => (
      <ComposerChromeStack
        active
        queue={<div />}
        launcher={{ present: () => false, children: () => <div /> }}
        arm={{ present: () => false, children: () => <div /> }}
        checkpoint={{ present: () => false, children: () => <div /> }}
        ask={{ present: () => false, children: () => <div /> }}
        composer={<div data-testid="composer-body" />}
      />
    ));
    expect(screen.getByTestId("find-bar-host-composer")).toBeTruthy();
    expect(screen.getByTestId("find-bar")).toBeTruthy();
    const stack = screen.getByTestId("composer-chrome-stack");
    const findSlot = stack.querySelector('[data-slot="find"]');
    const composer = screen.getByTestId("composer-body");
    expect(
      findSlot &&
        composer.compareDocumentPosition(findSlot) &
          Node.DOCUMENT_POSITION_PRECEDING,
    ).toBeTruthy();
    resetFindControllerForTests();
  });

  it("a leaving stage renders no optional chrome", async () => {
    const { openFind, registerFindableView, setPrimaryFindableView, resetFindControllerForTests } =
      await import("../../find/find-controller.ts");
    resetFindControllerForTests();
    const root = document.createElement("div");
    document.body.appendChild(root);
    registerFindableView({
      id: "session-transcript",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("session-transcript");
    openFind();

    // The leaving stage withholds optional chrome.
    render(() => (
      <ComposerChromeStack
        active={false}
        queue={<div />}
        launcher={{ present: () => true, children: () => <div data-testid="launcher-body" /> }}
        arm={{ present: () => true, children: () => <div /> }}
        checkpoint={{ present: () => true, children: () => <div /> }}
        ask={{ present: () => true, children: () => <div /> }}
        composer={<div data-testid="composer-body" />}
      />
    ));

    const stack = screen.getByTestId("composer-chrome-stack");
    expect(stack.querySelectorAll("[data-slot]")).toHaveLength(0);
    expect(screen.queryByTestId("find-bar")).toBeNull();
    expect(screen.queryByTestId("launcher-body")).toBeNull();
    expect(screen.getByTestId("composer-body")).toBeTruthy();
    resetFindControllerForTests();
  });

  it("builds slot content once, not on every presence read", () => {
    let built = 0;
    const Card = () => {
      built += 1;
      return <div data-testid="ask-card" />;
    };
    const [churn, setChurn] = createSignal(0);
    const empty = { present: () => false, children: () => null };
    render(() => (
      <ComposerChromeStack
        active
        queue={null}
        launcher={empty}
        arm={empty}
        checkpoint={empty}
        ask={{ present: () => churn() >= 0, children: () => <Card /> }}
        composer={null}
      />
    ));
    setChurn(1);
    setChurn(2);
    expect(built).toBe(1);
  });
});
