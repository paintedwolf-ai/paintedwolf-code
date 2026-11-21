import { createSignal } from "solid-js";
import type { SessionMode } from "../../api/types.ts";
import type { DenChatPrefs, DenMessageTimes } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_MESSAGE_TIMES: DenMessageTimes = "hover";

const [messageTimes, setMessageTimes] = createSignal<DenMessageTimes>(DEFAULT_MESSAGE_TIMES);
const [lastStartedMode, setLastStartedMode] = createSignal<SessionMode>("agent");
const [prefsReady, setPrefsReady] = createSignal(false);

export function chatPrefsReady(): boolean {
  return prefsReady();
}

export function lastStartedModePref(): SessionMode {
  return lastStartedMode();
}

export function rememberStartedMode(mode: SessionMode): void {
  if (lastStartedMode() === mode) return;
  setLastStartedMode(mode);
  void persistAppStateInBackground({
    chat: { ...getAppStateSnapshot().chat, lastStartedMode: mode },
  });
}

/** When the person's own messages show their time. */
export function messageTimesPref(): DenMessageTimes {
  return messageTimes();
}

export function resolveMessageTimes(prefs?: DenChatPrefs): DenMessageTimes {
  return prefs?.messageTimes ?? DEFAULT_MESSAGE_TIMES;
}

export function syncChatPrefsFromSnapshot(): void {
  const prefs = getAppStateSnapshot().chat;
  setMessageTimes(resolveMessageTimes(prefs));
  setLastStartedMode(prefs?.lastStartedMode === "ask" ? "ask" : "agent");
  setPrefsReady(true);
}

export async function saveMessageTimes(value: DenMessageTimes): Promise<void> {
  setMessageTimes(value);
  await persistAppStateInBackground({
    chat: {
      ...getAppStateSnapshot().chat,
      messageTimes: value,
    },
  });
}
