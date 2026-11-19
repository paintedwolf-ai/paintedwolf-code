import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  resolveWrite: undefined as (() => void) | undefined,
}));

const persistAppState = vi.hoisted(() =>
  vi.fn(
    () =>
      new Promise<void>((resolve) => {
        state.resolveWrite = resolve;
      }),
  ),
);

vi.mock("../../store/app-state-snapshot.ts", () => ({
  getAppStateSnapshot: () => ({
    version: 1,
    recents: [],
    onboarding: { firstRunSetupCompleted: false },
  }),
  persistAppState,
}));

import {
  completeFirstRunSetup,
  completeLocalModelOnboarding,
  firstRunSetupCompletedPref,
  localModelOnboardingCompletedPref,
  syncOnboardingPrefsFromSnapshot,
} from "./onboarding-prefs.ts";

describe("onboarding prefs durability", () => {
  beforeEach(() => {
    state.resolveWrite = undefined;
    persistAppState.mockClear();
    syncOnboardingPrefsFromSnapshot();
  });

  it("records a completed model decision without finishing the remaining steps", async () => {
    const saving = completeLocalModelOnboarding();
    expect(localModelOnboardingCompletedPref()).toBe(false);
    state.resolveWrite?.();
    await saving;
    expect(localModelOnboardingCompletedPref()).toBe(true);
    expect(firstRunSetupCompletedPref()).toBe(false);
    await completeLocalModelOnboarding();
    expect(persistAppState).toHaveBeenCalledOnce();
  });

  it("completes layout and welcome without recording an unpublished model decision", async () => {
    const saving = completeFirstRunSetup({deferModelStep: true});
    state.resolveWrite?.();
    await saving;
    expect(firstRunSetupCompletedPref()).toBe(true);
    expect(localModelOnboardingCompletedPref()).toBe(false);
    expect(persistAppState).toHaveBeenCalledWith({
      onboarding: {firstRunSetupCompleted: true, localModelOnboardingCompleted: false},
    });
  });

  it("does not dismiss the hard gate before the completion latch is durable", async () => {
    const saving = completeFirstRunSetup({deferModelStep: false});

    expect(firstRunSetupCompletedPref()).toBe(false);
    expect(persistAppState).toHaveBeenCalledOnce();

    state.resolveWrite?.();
    await saving;
    expect(firstRunSetupCompletedPref()).toBe(true);
    expect(localModelOnboardingCompletedPref()).toBe(true);
  });
});
