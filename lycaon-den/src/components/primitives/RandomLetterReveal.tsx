import { Index, createEffect, on, onCleanup } from "solid-js";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";

const REVEAL_MIN = 1;
const REVEAL_MAX = 3;
const TICK_MIN_MS = 10;
const TICK_MAX_MS = 26;

const VISIBLE_CLASS = "den-letter-reveal-char--visible";

/** Prevents repeated reveals after keyed remounts. */
const revealedOnceKeys = new Set<string>();

export function hasLetterRevealOnceKey(onceKey: string): boolean {
  return revealedOnceKeys.has(onceKey.trim());
}

export function markLetterRevealOnceKey(onceKey: string): void {
  const key = onceKey.trim();
  if (!key) return;
  if (revealedOnceKeys.size > 256) revealedOnceKeys.clear();
  revealedOnceKeys.add(key);
}

export function clearLetterRevealOnceKeys(): void {
  revealedOnceKeys.clear();
}

function shuffle(indices: number[]): number[] {
  for (let i = 0; i < indices.length - 1; i++) {
    const j = i + Math.floor(Math.random() * (indices.length - i));
    const a = indices[i];
    const b = indices[j];
    if (a === undefined || b === undefined) continue;
    indices[i] = b;
    indices[j] = a;
  }
  return indices;
}
type Props = {
  text: string;
  /** Dedupe key across remounts. */
  onceKey?: string;
  class?: string;
  "data-testid"?: string;
};

/** Animates characters while exposing the complete text. */
export function RandomLetterReveal(props: Props) {
  const chars = () => Array.from(props.text);
  let container: HTMLSpanElement | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  // Preserve settled opacity across updates.
  let settledText: string | undefined;

  const clearTimer = () => {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
  };
  onCleanup(clearTimer);

  const showAll = (els: readonly HTMLElement[]) => {
    for (const el of els) el.classList.add(VISIBLE_CLASS);
  };

  const runReveal = (text: string) => {
    clearTimer();
    if (!container) return;
    const els = Array.from(
      container.querySelectorAll<HTMLElement>(".den-letter-reveal-char"),
    );
    if (els.length === 0) return;

    const onceKey = props.onceKey?.trim() ?? "";
    const skipMotion =
      settledText === text ||
      (onceKey !== "" && hasLetterRevealOnceKey(onceKey)) ||
      prefersReducedMotion();

    if (skipMotion) {
      showAll(els);
      settledText = text;
      if (onceKey) markLetterRevealOnceKey(onceKey);
      return;
    }

    // Index reuses spans when text changes.
    for (const el of els) el.classList.remove(VISIBLE_CLASS);
    const order = shuffle(els.map((_, i) => i));
    let cursor = 0;
    // Mark before animation so an early remount cannot restart it.
    settledText = text;
    if (onceKey) markLetterRevealOnceKey(onceKey);
    const step = () => {
      const n = REVEAL_MIN + Math.floor(Math.random() * (REVEAL_MAX - REVEAL_MIN + 1));
      for (let k = 0; k < n && cursor < order.length; k++, cursor++) {
        const at = order[cursor];
        if (at === undefined) continue;
        els[at]?.classList.add(VISIBLE_CLASS);
      }
      if (cursor < order.length) {
        timer = setTimeout(
          step,
          TICK_MIN_MS + Math.floor(Math.random() * (TICK_MAX_MS - TICK_MIN_MS)),
        );
      } else {
        timer = undefined;
      }
    };
    timer = setTimeout(
      step,
      TICK_MIN_MS + Math.floor(Math.random() * (TICK_MAX_MS - TICK_MIN_MS)),
    );
  };

  createEffect(
    on(
      () => props.text,
      (text) => {
        // Wait for Index to patch the spans.
        queueMicrotask(() => runReveal(text));
      },
    ),
  );

  return (
    <span
      ref={container}
      class={`den-letter-reveal ${props.class ?? ""}`.trim()}
      aria-label={props.text}
      data-testid={props["data-testid"]}
    >
      <Index each={chars()}>
        {(ch) => (
          <span aria-hidden="true" class="den-letter-reveal-char">
            {ch()}
          </span>
        )}
      </Index>
    </span>
  );
}
