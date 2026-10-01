import { createSignal } from "solid-js";
import type { DenWhatsNewPrefs } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const [lastSeenVersion, setLastSeenVersion] = createSignal<string | undefined>(
  undefined,
);
const [prefsReady, setPrefsReady] = createSignal(false);

/** Durable What's New latch — version whose notes were acknowledged or seeded. */
export function whatsNewLastSeenVersion(): string | undefined {
  return lastSeenVersion();
}

/** True after snapshot sync has applied. */
export function whatsNewPrefsReady(): boolean {
  return prefsReady();
}

export function resolveLastSeenVersion(
  prefs?: DenWhatsNewPrefs,
): string | undefined {
  const v = prefs?.lastSeenVersion;
  return typeof v === "string" && v.trim() ? v.trim() : undefined;
}

export function syncWhatsNewFromSnapshot(): void {
  setLastSeenVersion(resolveLastSeenVersion(getAppStateSnapshot().whatsNew));
  setPrefsReady(true);
}

export async function saveLastSeenVersion(version: string): Promise<void> {
  const trimmed = version.trim();
  setLastSeenVersion(trimmed || undefined);
  setPrefsReady(true);
  await persistAppStateInBackground({
    whatsNew: {
      ...getAppStateSnapshot().whatsNew,
      ...(trimmed ? { lastSeenVersion: trimmed } : {}),
    },
  });
}

/** Test hook — reset latch between cases. */
export function resetWhatsNewPrefsForTests(
  prefs?: DenWhatsNewPrefs,
  ready = true,
): void {
  setLastSeenVersion(resolveLastSeenVersion(prefs));
  setPrefsReady(ready);
}
