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

  it("drops non-boolean latch values", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      onboarding: { firstRunSetupCompleted: "yes" },
    });
    expect(state.onboarding).toBeUndefined();
  });
});
