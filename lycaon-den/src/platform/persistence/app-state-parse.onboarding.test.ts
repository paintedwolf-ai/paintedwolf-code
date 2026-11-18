import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState onboarding", () => {
  it("preserves firstRunSetupCompleted", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      onboarding: { firstRunSetupCompleted: true },
    });
    expect(state.onboarding).toEqual({ firstRunSetupCompleted: true });
  });

  it("preserves the local model choice latch", () => {
    const onboarding = {firstRunSetupCompleted: true, localModelOnboardingCompleted: true};
    expect(parseAppState({version: 1, recents: [], onboarding}).onboarding).toEqual(onboarding);
  });

  it("drops non-boolean latch values", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      onboarding: { firstRunSetupCompleted: "yes" },
    });
    expect(state.onboarding).toBeUndefined();
  });
});
