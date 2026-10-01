import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
  type JSX,
} from "solid-js";
import {
  animateHeightToggle,
  isHeightToggleAnimating,
} from "../../ui/height-toggle-motion.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";

export const COMPOSER_CHROME_SLOT_ANIMATION_MS = 300;

const COMPOSER_CHROME_SLOT_EASING = "cubic-bezier(0.33, 0, 0.2, 1)";

const OPEN_FADE_WINDOW: [number, number] = [0.35, 1];

const CLOSE_FADE_WINDOW: [number, number] = [0, 0.55];

type ComposerChromeSlotId =
  | "launcher"
  | "arm"
  | "find"
  | "checkpoint"
  | "ask_user";

/** Collapse measures height; fade preserves layout through exit. */
type ComposerChromeSlotMotion = "collapse" | "fade";

type Props = {
  slot: ComposerChromeSlotId;
  present: boolean;
  motion?: ComposerChromeSlotMotion;
  children: JSX.Element;
};

export function ComposerChromeSlot(props: Props) {
  const [mounted, setMounted] = createSignal(Boolean(props.present));
  const [collapsed, setCollapsed] = createSignal(!props.present);
  let shell: HTMLDivElement | undefined;
  let inner: HTMLDivElement | undefined;
  let pendingOpenRaf: number | undefined;
  let fadeOut: Animation | undefined;

  const cancelFadeOut = () => {
    const animation = fadeOut;
    fadeOut = undefined;
    animation?.cancel();
  };

  const runFadeOut = () => {
    const node = shell;
    if (!node || typeof node.animate !== "function" || prefersReducedMotion()) {
      setMounted(false);
      return;
    }
    if (fadeOut) return;
    const anim = node.animate(
      { opacity: [1, 0] },
      {
        duration: COMPOSER_CHROME_SLOT_ANIMATION_MS,
        easing: COMPOSER_CHROME_SLOT_EASING,
        fill: "forwards",
      },
    );
    fadeOut = anim;
    anim.oncancel = () => {
      if (fadeOut !== anim) return;
      fadeOut = undefined;
      setMounted(false);
    };
    anim.onfinish = () => {
      if (fadeOut !== anim) return;
      fadeOut = undefined;
      anim.oncancel = null;
      anim.cancel();
      setMounted(false);
    };
  };

  const runOpenAnimation = () => {
    pendingOpenRaf = undefined;
    const node = shell;
    const body = inner;
    if (!node || !body || prefersReducedMotion() || !mounted()) return;
    if (!collapsed() && !isHeightToggleAnimating(node)) return;

    animateHeightToggle(node, {
      direction: "open",
      showBody: () => setCollapsed(false),
      hideBody: () => {
        body.style.display = "none";
      },
      closeBody: () => {
        setCollapsed(true);
      },
      resetBodyVisibility: () => {
        body.style.display = "";
      },
      collapsedHeight: () => 0,
      bodyStillOpen: () => false,
      opacity: { start: 0, end: 1, window: OPEN_FADE_WINDOW },
      duration: COMPOSER_CHROME_SLOT_ANIMATION_MS,
      easing: COMPOSER_CHROME_SLOT_EASING,
    });
  };

  const runCloseAnimation = () => {
    const node = shell;
    const body = inner;
    if (!node || !body || prefersReducedMotion() || !mounted()) {
      setMounted(false);
      return;
    }
    if (collapsed()) {
      setMounted(false);
      return;
    }

    animateHeightToggle(node, {
      direction: "close",
      showBody: () => {},
      hideBody: () => {
        body.style.display = "none";
      },
      closeBody: () => {
        setCollapsed(true);
        setMounted(false);
      },
      resetBodyVisibility: () => {},
      collapsedHeight: () => 0,
      bodyStillOpen: () => !collapsed(),
      opacity: { start: 1, end: 0, window: CLOSE_FADE_WINDOW },
      duration: COMPOSER_CHROME_SLOT_ANIMATION_MS,
      easing: COMPOSER_CHROME_SLOT_EASING,
    });
  };

  const applyPresence = (next: boolean) => {
    if (props.motion === "fade") {
      if (next) {
        cancelFadeOut();
        setMounted(true);
        setCollapsed(false);
        return;
      }
      if (!mounted()) return;
      runFadeOut();
      return;
    }
    if (next) {
      const wasMounted = mounted();
      if (!wasMounted) {
        setMounted(true);
        setCollapsed(true);
      }
      if (prefersReducedMotion()) {
        setCollapsed(false);
        return;
      }
      // Fresh mounts render one collapsed frame before measurement.
      if (pendingOpenRaf !== undefined) {
        cancelAnimationFrame(pendingOpenRaf);
      }
      if (wasMounted && !collapsed()) {
        runOpenAnimation();
      } else {
        pendingOpenRaf = requestAnimationFrame(runOpenAnimation);
      }
      return;
    }
    if (pendingOpenRaf !== undefined) {
      cancelAnimationFrame(pendingOpenRaf);
      pendingOpenRaf = undefined;
    }
    if (!mounted()) return;
    if (prefersReducedMotion()) {
      setMounted(false);
      return;
    }
    runCloseAnimation();
  };

  const present = createMemo(() => props.present);

  createEffect(() => {
    const next = present();
    untrack(() => applyPresence(next));
  });

  onCleanup(() => {
    if (pendingOpenRaf !== undefined) {
      cancelAnimationFrame(pendingOpenRaf);
      pendingOpenRaf = undefined;
    }
    cancelFadeOut();
    const node = shell;
    if (!node || typeof node.getAnimations !== "function") return;
    for (const anim of node.getAnimations()) {
      anim.cancel();
    }
  });

  return (
    <Show when={mounted()}>
      <div
        ref={(node) => {
          shell = node;
        }}
        class="den-composer-chrome-slot"
        data-slot={props.slot}
        data-collapsed={collapsed() ? "" : undefined}
        data-testid={`composer-chrome-slot-${props.slot}`}
      >
        <div
          ref={(node) => {
            inner = node;
          }}
          class="den-composer-chrome-slot__inner"
        >
          {props.children}
        </div>
      </div>
    </Show>
  );
}
