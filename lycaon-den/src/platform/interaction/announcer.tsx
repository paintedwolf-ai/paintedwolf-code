/** One polite status region for announcements that have no region of their own. */
import { createSignal } from "solid-js";

const [message, setMessage] = createSignal("");
let epoch = 0;

/** Speaks `text` once; a repeat of the same text is spoken again. */
export function announce(text: string): void {
  const next = text.trim();
  if (!next) return;
  const current = ++epoch;
  setMessage("");
  queueMicrotask(() => {
    if (current === epoch) setMessage(next);
  });
}

export function Announcer() {
  return (
    <div class="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="app-announcer">
      {message()}
    </div>
  );
}
