import { onCleanup } from "solid-js";

/** How long typing settles before a surface asks the host again. */
export const TYPING_SETTLE_MS = 280;

/** Coalesces typing into one call and cancels it when the owning scope closes. */
export function createDebounced(
  run: () => void,
  delayMs: number = TYPING_SETTLE_MS,
): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => {
    if (timer) clearTimeout(timer);
  });
  return () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(run, delayMs);
  };
}
