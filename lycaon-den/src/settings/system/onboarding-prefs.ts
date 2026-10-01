import { createSignal } from "solid-js";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const [firstRunSetupCompleted, setFirstRunSetupCompleted] = createSignal(false);
const [prefsReady, setPrefsReady] = createSignal(false);

export function firstRunSetupCompletedPref(): boolean {
  return firstRunSetupCompleted();
}

export function onboardingPrefsReady(): boolean {
  return prefsReady();
}

export function syncOnboardingPrefsFromSnapshot(): void {
  setFirstRunSetupCompleted(
    getAppStateSnapshot().onboarding?.firstRunSetupCompleted === true,
  );
  setPrefsReady(true);
}

export async function completeFirstRunSetup(): Promise<void> {
  setPrefsReady(true);
  await persistAppStateInBackground({
    onboarding: {
      ...getAppStateSnapshot().onboarding,
      firstRunSetupCompleted: true,
    },
  });
  setFirstRunSetupCompleted(true);
}
