import { Show, createEffect, createSignal, on, onCleanup, untrack } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";
import type { TurnClock } from "../../api/types.ts";
import { turnClockElapsedMs } from "../../chat/session/turn-clock.ts";
import { useNow } from "../../time/now.ts";
import { formatElapsed } from "../../time/time-copy.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import { HEIGHT_TOGGLE_TIMING } from "../../ui/height-toggle-motion.ts";

export const COMPOSER_ACTIVITY_LEAVING_ATTR = "data-leaving";

const RESTING = { opacity: "1", transform: "none" };
const ENTER_OFFSET = { opacity: "0", transform: "translateY(-4px)" };
const EXIT_OFFSET = { opacity: "0", transform: "translateY(-3px)" };
/** The line appears once its lane has mostly opened. */
const ENTER_FADE_START = 0.35;
/** The line is gone before its lane has mostly closed. */
const EXIT_FADE_END = 0.55;

type Props = {
  /** Visible and announced activity text. */
  activityLabel?: string;
  /** Host-stamped active time. */
  turnClock?: TurnClock;
  /** Whether the line shows; changes move with the disclosure timeline. */
  present?: boolean;
  /** Applies a layout change through the composer's height motion. */
  resize?: (mutate: () => void) => void;
};

/** Presents the composer's activity line as its lane opens and closes. */
export function ComposerActivityIndicator(props: Props) {
  const present = () => props.present ?? true;
  const [mounted, setMounted] = createSignal(untrack(present));
  let line: HTMLParagraphElement | undefined;
  let fade: Animation | undefined;
  let reconcileQueued = false;

  const resize = (mutate: () => void) => {
    if (props.resize) props.resize(mutate);
    else mutate();
  };

  const moves = (el: HTMLElement) =>
    typeof el.animate === "function" && !prefersReducedMotion();

  const cancelFade = () => {
    const animation = fade;
    fade = undefined;
    if (!animation) return;
    animation.onfinish = null;
    animation.cancel();
  };

  const runFade = (el: HTMLElement, keyframes: Keyframe[], fill: FillMode, onDone?: () => void) => {
    const animation = el.animate(keyframes, { ...HEIGHT_TOGGLE_TIMING, fill });
    fade = animation;
    animation.onfinish = () => {
      if (fade !== animation) return;
      fade = undefined;
      onDone?.();
    };
  };

  const pose = (el: HTMLElement) => {
    const style = getComputedStyle(el);
    return { opacity: style.opacity || "1", transform: style.transform || "none" };
  };

  // Runs outside Solid's update batch, so each mount lands inside `resize`.
  const reconcile = () => {
    reconcileQueued = false;
    const el = line;
    if (present()) {
      if (!mounted()) {
        resize(() => setMounted(true));
        if (line && moves(line)) {
          runFade(line, [
            { ...ENTER_OFFSET, offset: 0 },
            { ...ENTER_OFFSET, offset: ENTER_FADE_START },
            { ...RESTING, offset: 1 },
          ], "none");
        }
        return;
      }
      if (!el?.hasAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR)) return;
      const from = pose(el);
      cancelFade();
      resize(() => el.removeAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR));
      if (moves(el)) runFade(el, [from, RESTING], "none");
      return;
    }
    if (!mounted() || el?.hasAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR)) return;
    if (!el || !moves(el)) {
      cancelFade();
      resize(() => setMounted(false));
      return;
    }
    const from = pose(el);
    cancelFade();
    // Out of flow, the composer measures its closed height while the line fades.
    resize(() => el.setAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR, ""));
    runFade(
      el,
      [
        { ...from, offset: 0 },
        { ...EXIT_OFFSET, offset: EXIT_FADE_END },
        { ...EXIT_OFFSET, offset: 1 },
      ],
      "forwards",
      () => setMounted(false),
    );
  };

  createEffect(
    on(present, () => {
      if (reconcileQueued) return;
      reconcileQueued = true;
      queueMicrotask(reconcile);
    }, { defer: true }),
  );
  onCleanup(cancelFade);

  return (
    <Show when={mounted()}>
      <ActivityLine
        ref={(el) => {
          line = el;
        }}
        activityLabel={props.activityLabel}
        turnClock={props.turnClock}
      />
    </Show>
  );
}

/** Uses visible status text as the accessible name. */
function ActivityLine(props: {
  ref: (el: HTMLParagraphElement) => void;
  activityLabel?: string;
  turnClock?: TurnClock;
}) {
  const activity = () => props.activityLabel?.trim() || "Thinking";
  const nowMs = useNow("second", () => props.turnClock?.running === true);
  const elapsed = () => turnClockElapsedMs(props.turnClock, nowMs());
  // Render elapsed time after the host starts the clock.
  const showElapsed = () => props.turnClock?.running || elapsed() > 0;
  return (
    <p
      ref={props.ref}
      class="den-composer-activity"
      role="status"
      aria-live="polite"
      aria-busy="true"
      data-testid="thinking-indicator"
      {...chromeProps()}
    >
      <span
        class="den-composer-activity-spinner"
        aria-hidden="true"
        data-testid="thinking-spinner"
      />
      <span class="den-composer-activity-label">{activity()}</span>
      <Show when={showElapsed()}>
        {/* Avoid announcing each timer tick. */}
        <span
          class="den-composer-activity-elapsed"
          aria-hidden="true"
          data-testid="thinking-elapsed"
        >
          {formatElapsed(elapsed())}
        </span>
      </Show>
    </p>
  );
}
