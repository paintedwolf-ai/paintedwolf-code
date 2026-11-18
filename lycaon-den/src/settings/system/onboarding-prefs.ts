import { createSignal } from "solid-js";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const [firstRunSetupCompleted, setFirstRunSetupCompleted] = createSignal(false);
const [localModelOnboardingCompleted, setLocalModelOnboardingCompleted] = createSignal(false);
const [prefsReady, setPrefsReady] = createSignal(false);

export function firstRunSetupCompletedPref(): boolean {
  return firstRunSetupCompleted();
}

export function localModelOnboardingCompletedPref(): boolean {
  return localModelOnboardingCompleted();
}

export function onboardingPrefsReady(): boolean {
  return prefsReady();
}

export function syncOnboardingPrefsFromSnapshot(): void {
  setLocalModelOnboardingCompleted(getAppStateSnapshot().onboarding?.localModelOnboardingCompleted === true);
  setFirstRunSetupCompleted(
    getAppStateSnapshot().onboarding?.firstRunSetupCompleted === true,
  );
  setPrefsReady(true);
}

export async function completeLocalModelOnboarding(): Promise<void> {
  if (localModelOnboardingCompleted()) return;
  await persistAppStateInBackground({
    onboarding: { ...getAppStateSnapshot().onboarding, localModelOnboardingCompleted: true },
  });
  setLocalModelOnboardingCompleted(true);
}

export async function completeFirstRunSetup(): Promise<void> {
  setPrefsReady(true);
  await persistAppStateInBackground({
    onboarding: {
      ...getAppStateSnapshot().onboarding,
      firstRunSetupCompleted: true,
      localModelOnboardingCompleted: true,
    },
  });
  setLocalModelOnboardingCompleted(true);
  setFirstRunSetupCompleted(true);
}
