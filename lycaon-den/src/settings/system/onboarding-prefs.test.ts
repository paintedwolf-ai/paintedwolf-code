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
  firstRunSetupCompletedPref,
  syncOnboardingPrefsFromSnapshot,
} from "./onboarding-prefs.ts";

describe("onboarding prefs durability", () => {
  beforeEach(() => {
    state.resolveWrite = undefined;
    persistAppState.mockClear();
    syncOnboardingPrefsFromSnapshot();
  });

  it("does not dismiss the hard gate before the completion latch is durable", async () => {
    const saving = completeFirstRunSetup();

    expect(firstRunSetupCompletedPref()).toBe(false);
    expect(persistAppState).toHaveBeenCalledOnce();

    state.resolveWrite?.();
    await saving;
    expect(firstRunSetupCompletedPref()).toBe(true);
  });
});
