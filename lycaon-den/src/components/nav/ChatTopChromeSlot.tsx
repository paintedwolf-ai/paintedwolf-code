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

const TOP_CHROME_SLOT_ANIMATION_MS = 260;

const TOP_CHROME_SLOT_EASING = "cubic-bezier(0.33, 0, 0.2, 1)";

const OPEN_FADE_WINDOW: [number, number] = [0.35, 1];

const CLOSE_FADE_WINDOW: [number, number] = [0, 0.55];

type ChatTopChromeSlotId =
  | "session"
  | "spend"
  | "notifications"
  | "verify"
  | "protection"
  | "promote"
  | "provider"
  | "folder";

function snapsInsteadOfAnimating(node: HTMLElement): boolean {
  return prefersReducedMotion() || typeof node.animate !== "function";
}

type Props = {
  slot: ChatTopChromeSlotId;
  present: boolean;
  children: JSX.Element;
};

export function ChatTopChromeSlot(props: Props) {
  const [mounted, setMounted] = createSignal(Boolean(props.present));
  const [collapsed, setCollapsed] = createSignal(!props.present);
  let shell: HTMLDivElement | undefined;
  let inner: HTMLDivElement | undefined;
  let pendingOpenRaf: number | undefined;

  const runOpenAnimation = () => {
    pendingOpenRaf = undefined;
    const node = shell;
    const body = inner;
    if (!node || !body || !mounted()) return;
    if (snapsInsteadOfAnimating(node)) {
      setCollapsed(false);
      return;
    }
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
      duration: TOP_CHROME_SLOT_ANIMATION_MS,
      easing: TOP_CHROME_SLOT_EASING,
    });
  };

  const runCloseAnimation = () => {
    const node = shell;
    const body = inner;
    if (!node || !body || !mounted() || snapsInsteadOfAnimating(node)) {
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
      duration: TOP_CHROME_SLOT_ANIMATION_MS,
      easing: TOP_CHROME_SLOT_EASING,
    });
  };

  const applyPresence = (next: boolean) => {
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
      // Mount one frame before measuring the open height.
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

  // Track presence without subscribing to slot bookkeeping.
  const present = createMemo(() => props.present);

  createEffect(() => {
    const next = present();
    untrack(() => applyPresence(next));
  });

  onCleanup(() => {
    // Cancel work scheduled past disposal.
    if (pendingOpenRaf !== undefined) {
      cancelAnimationFrame(pendingOpenRaf);
      pendingOpenRaf = undefined;
    }
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
        class="den-chat-top-chrome-slot"
        data-slot={props.slot}
        data-collapsed={collapsed() ? "" : undefined}
        data-testid={`chat-top-chrome-slot-${props.slot}`}
      >
        <div
          ref={(node) => {
            inner = node;
          }}
          class="den-chat-top-chrome-slot__inner"
        >
          {props.children}
        </div>
      </div>
    </Show>
  );
}
