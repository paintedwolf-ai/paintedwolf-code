import { createEffect, createSignal, on, type Accessor } from "solid-js";
import type { SessionMode } from "../api/types.ts";
import { chatPrefsReady, lastStartedModePref } from "../settings/chat/chat-prefs.ts";

export function createHomeIdeaDraftMode(visible: Accessor<boolean>, hasDraft: Accessor<boolean>) {
  const [mode, setMode] = createSignal<SessionMode | undefined>(chatPrefsReady() ? lastStartedModePref() : undefined);
  let picked = false;

  createEffect(on([visible, chatPrefsReady], ([shown, ready]) => {
    if (shown && ready && (mode() === undefined || (!picked && !hasDraft()))) {
      setMode(lastStartedModePref());
    }
  }));

  return {
    mode,
    select(value: SessionMode) {
      picked = true;
      setMode(value);
    },
    clear() {
      picked = false;
      if (chatPrefsReady()) setMode(lastStartedModePref());
    },
    accept(value: SessionMode) {
      picked = false;
      setMode(value);
    },
  };
}
