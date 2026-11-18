import { batch, createEffect, createSignal, on, onCleanup, untrack, type Accessor } from "solid-js";
import { prefersReducedMotion } from "../platform/interaction/reduced-motion.ts";

const HINT_FADE_MS = 120;

export function createFadingInputHint<T extends string | undefined>(
  source: Accessor<T>,
  visible: Accessor<boolean>,
) {
  const [text, setText] = createSignal<T>(untrack(source));
  const [out, setOut] = createSignal(false);

  createEffect(on([source, visible], ([next, showing]) => {
    const previous = text();
    if (!showing || prefersReducedMotion() || !previous || next === previous) {
      setText(() => next);
      setOut(false);
      return;
    }

    setOut(true);
    const timer = globalThis.setTimeout(() => {
      batch(() => {
        setText(() => next);
        setOut(false);
      });
    }, HINT_FADE_MS);
    onCleanup(() => globalThis.clearTimeout(timer));
  }));

  return { text, out };
}
