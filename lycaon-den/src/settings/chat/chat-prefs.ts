import { createSignal } from "solid-js";
import type { DenChatPrefs, DenMessageTimes } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_MESSAGE_TIMES: DenMessageTimes = "hover";

const [messageTimes, setMessageTimes] = createSignal<DenMessageTimes>(DEFAULT_MESSAGE_TIMES);

/** When the person's own messages show their time. */
export function messageTimesPref(): DenMessageTimes {
  return messageTimes();
}

export function resolveMessageTimes(prefs?: DenChatPrefs): DenMessageTimes {
  return prefs?.messageTimes ?? DEFAULT_MESSAGE_TIMES;
}

export function syncChatPrefsFromSnapshot(): void {
  setMessageTimes(resolveMessageTimes(getAppStateSnapshot().chat));
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
