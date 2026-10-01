import { createEffect, createMemo, onCleanup, untrack, type Accessor } from "solid-js";

/** Scroll subscriptions belong to an active, hydrated session and its DOM viewport. */
export function watchTranscriptRuntime(options: {
  active: Accessor<boolean>;
  sessionId: Accessor<string | undefined>;
  currentSessionId: Accessor<string | undefined>;
  hydrating: Accessor<boolean>;
  stream: Accessor<HTMLElement | undefined>;
  bind: () => () => void;
}): void {
  const sessionId = createMemo(() => {
    if (!options.active() || options.hydrating()) return undefined;
    const id = options.sessionId();
    return id === options.currentSessionId() ? id : undefined;
  });
  createEffect(() => {
    if (!sessionId() || !options.stream()) return;
    onCleanup(untrack(options.bind));
  });
}
