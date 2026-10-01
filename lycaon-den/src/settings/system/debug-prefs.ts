import { createSignal } from "solid-js";
import type { DenDebugPrefs } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_VERBOSE_MODE = false;
const DEFAULT_FULL_DEBUG_LOGGING = false;

const [verboseMode, setVerboseMode] = createSignal(DEFAULT_VERBOSE_MODE);
const [fullDebugLogging, setFullDebugLogging] = createSignal(
  DEFAULT_FULL_DEBUG_LOGGING,
);

/**
 * Reactive verbose pref. Off hides draft rows and benign tool cards in chat;
 * the accepted answer's version rail stays (AssistantChatTurn).
 */
export function verboseModePref(): boolean {
  return verboseMode();
}

/** Reactive debug pref — full backend debug capture in the bundled app. */
export function fullDebugLoggingPref(): boolean {
  return fullDebugLogging();
}

export function resolveVerboseMode(prefs?: DenDebugPrefs): boolean {
  return prefs?.verboseMode ?? DEFAULT_VERBOSE_MODE;
}

export function resolveFullDebugLogging(prefs?: DenDebugPrefs): boolean {
  return prefs?.fullDebugLogging ?? DEFAULT_FULL_DEBUG_LOGGING;
}

export function syncDebugPrefsFromSnapshot(): void {
  const debug = getAppStateSnapshot().debug;
  setVerboseMode(resolveVerboseMode(debug));
  setFullDebugLogging(resolveFullDebugLogging(debug));
}

export async function saveVerboseMode(value: boolean): Promise<void> {
  setVerboseMode(value);
  await persistAppStateInBackground({
    debug: {
      ...getAppStateSnapshot().debug,
      verboseMode: value,
    },
  });
}

export async function saveFullDebugLogging(value: boolean): Promise<void> {
  setFullDebugLogging(value);
  await persistAppStateInBackground({
    debug: {
      ...getAppStateSnapshot().debug,
      fullDebugLogging: value,
    },
  });
  // Arm main-thread stall capture immediately; sidecar den-perf.jsonl writes
  // still require a restart so LYCAON_DEBUG_ALL is set on spawn.
  const {
    installMainThreadPerfObserver,
    isPerfCaptureEnabled,
    uninstallMainThreadPerfObserver,
  } =
    await import(
      "../../chat/stream/den-main-thread-perf.ts"
    );
  if (value || isPerfCaptureEnabled()) {
    installMainThreadPerfObserver();
  } else {
    uninstallMainThreadPerfObserver();
  }
}
