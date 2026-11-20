import { SETUP_REVISION } from "../../../shared/app-state-types.ts";
import { createSignal } from "solid-js";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const [firstRunSetupCompleted, setFirstRunSetupCompleted] = createSignal(false);
const [localModelOnboardingCompleted, setLocalModelOnboardingCompleted] = createSignal(false);
const [postponed, setPostponed] = createSignal(false);

export function localModelPromptHandled(): boolean {
  return postponed() || localModelOnboardingCompleted();
}

export function postponeLocalModelPrompt(): void {
  setPostponed(true);
}

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
  const saved = getAppStateSnapshot().onboarding;
  setLocalModelOnboardingCompleted((saved?.completedLocalModelStepRevision ?? 0) >= SETUP_REVISION);
  setFirstRunSetupCompleted((saved?.completedSetupRevision ?? 0) >= SETUP_REVISION);
  setPrefsReady(true);
}

export async function completeLocalModelOnboarding(): Promise<void> {
  if (localModelOnboardingCompleted()) return;
  await persistAppStateInBackground({
    onboarding: { ...getAppStateSnapshot().onboarding, completedLocalModelStepRevision: SETUP_REVISION },
  });
  setLocalModelOnboardingCompleted(true);
}

export async function completeFirstRunSetup(): Promise<void> {
  if (firstRunSetupCompleted()) return;
  await persistAppStateInBackground({
    onboarding: { ...getAppStateSnapshot().onboarding, completedSetupRevision: SETUP_REVISION },
  });
  setFirstRunSetupCompleted(true);
}
