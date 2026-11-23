import { describe, expect, it } from "vitest";
import { parseAppState } from "./app-state-parse.ts";

describe("parseAppState onboarding", () => {
  it("reads completion from the released first-run walkthrough", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      onboarding: { firstRunSetupCompleted: true },
    });
    expect(state.onboarding).toEqual({ completedSetupRevision: 1 });
  });

  it("preserves the local model choice latch", () => {
    const onboarding = {completedSetupRevision: 2, completedLocalModelStepRevision: 2};
    expect(parseAppState({version: 1, recents: [], onboarding}).onboarding).toEqual(onboarding);
  });

  it("drops non-boolean latch values", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      onboarding: { firstRunSetupCompleted: "yes", completedSetupRevision: -1, completedLocalModelStepRevision: 0.5 },
    });
    expect(state.onboarding).toBeUndefined();
  });
});
