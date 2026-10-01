import { createSignal } from "solid-js";
import type { DenFirstTimeTipsPrefs } from "../../../shared/app-state-types.ts";
import type { FirstTimeTipId } from "../../first-time-tips/first-time-tips-catalog.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_ENABLED = true;

const [enabled, setEnabled] = createSignal(DEFAULT_ENABLED);
const [dismissed, setDismissed] = createSignal<readonly FirstTimeTipId[]>([]);
const [ready, setReady] = createSignal(false);

export function firstTimeTipsEnabledPref(): boolean {
  return enabled();
}

export function firstTimeTipsPrefsReady(): boolean {
  return ready();
}

export function isFirstTimeTipDismissed(id: FirstTimeTipId): boolean {
  return dismissed().includes(id);
}


export function resolveFirstTimeTipsEnabled(
  prefs?: DenFirstTimeTipsPrefs,
): boolean {
  return prefs?.enabled ?? DEFAULT_ENABLED;
}

export function resolveDismissedFirstTimeTips(
  prefs?: DenFirstTimeTipsPrefs,
): readonly FirstTimeTipId[] {
  return (prefs?.dismissed ?? []) as readonly FirstTimeTipId[];
}

export function syncFirstTimeTipsFromSnapshot(): void {
  const prefs = getAppStateSnapshot().firstTimeTips;
  setEnabled(resolveFirstTimeTipsEnabled(prefs));
  setDismissed(resolveDismissedFirstTimeTips(prefs));
  setReady(true);
}

export async function saveFirstTimeTipsEnabled(value: boolean): Promise<void> {
  setEnabled(value);
  setReady(true);
  await persistAppStateInBackground({
    firstTimeTips: {
      ...getAppStateSnapshot().firstTimeTips,
      enabled: value,
    },
  });
}

export async function dismissFirstTimeTip(id: FirstTimeTipId): Promise<void> {
  const next = isFirstTimeTipDismissed(id) ? dismissed() : [...dismissed(), id];
  setDismissed(next);
  setReady(true);
  await persistAppStateInBackground({
    firstTimeTips: {
      ...getAppStateSnapshot().firstTimeTips,
      dismissed: [...next],
    },
  });
}

export function resetFirstTimeTipsPrefsForTests(
  prefs?: DenFirstTimeTipsPrefs,
  isReady = true,
): void {
  setEnabled(resolveFirstTimeTipsEnabled(prefs));
  setDismissed(resolveDismissedFirstTimeTips(prefs));
  setReady(isReady);
}
