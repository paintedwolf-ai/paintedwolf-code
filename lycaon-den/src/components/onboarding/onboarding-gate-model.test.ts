import { describe, expect, it } from "vitest";
import {
  ONBOARDING_GATE_STEP_COUNT,
  needsOnboardingConfig,
  onboardingEscapeTarget,
  onboardingGateStep,
  onboardingLayoutChoice,
  onboardingSplitCompanion,
  shouldShowOnboardingHardGate,
  startupCompanionForChoice,
} from "./onboarding-gate-model.ts";

describe("needsOnboardingConfig", () => {
  it("is true with no ready providers", () => {
    expect(
      needsOnboardingConfig({ readyProviderCount: 0, hasDefaultModel: false }),
    ).toBe(true);
  });

  it("is true when providers exist but default model is unset", () => {
    expect(
      needsOnboardingConfig({ readyProviderCount: 1, hasDefaultModel: false }),
    ).toBe(true);
  });

  it("is false when ready provider and default model exist", () => {
    expect(
      needsOnboardingConfig({ readyProviderCount: 1, hasDefaultModel: true }),
    ).toBe(false);
  });
});

describe("shouldShowOnboardingHardGate", () => {
  const base = {
    prefsReady: true, localModelOnboardingCompleted: true,
    firstRunSetupCompleted: false,
  };

  it("shows while latch is unset", () => {
    expect(shouldShowOnboardingHardGate(base)).toBe(true);
  });

  it("hides when latch is set even if needsConfig would be true", () => {
    expect(
      shouldShowOnboardingHardGate({ ...base, firstRunSetupCompleted: true }),
    ).toBe(false);
  });

  it("waits for prefs", () => {
    expect(shouldShowOnboardingHardGate({ ...base, prefsReady: false })).toBe(
      false,
    );
  });
});

describe("onboarding page order", () => {
  it("counts progress from the page list", () => {
    expect(ONBOARDING_GATE_STEP_COUNT).toBe(3);
    expect(onboardingGateStep("local_model")).toBe(1);
    expect(onboardingGateStep("layout")).toBe(2);
    expect(onboardingGateStep("welcome")).toBe(3);
  });

  it("escapes from the ends and steps forward between them", () => {
    expect(onboardingEscapeTarget("local_model")).toBe("finish");
    expect(onboardingEscapeTarget("layout")).toBe("welcome");
    expect(onboardingEscapeTarget("welcome")).toBe("finish");
  });
});

describe("onboarding layout choice", () => {
  it("reads Chat from an unset Open at launch, the default", () => {
    expect(onboardingLayoutChoice(null)).toBe("chat");
    expect(onboardingLayoutChoice("files")).toBe("split");
  });

  it("writes unset for Chat and Files for a fresh split", () => {
    expect(startupCompanionForChoice("chat", "files")).toBeNull();
    expect(startupCompanionForChoice("split", null)).toBe("files");
  });

  // A companion picked in Settings survives first run coming back after a
  // store reset; the split card names it instead of replacing it with Files.
  it("keeps a companion that is already set", () => {
    expect(onboardingSplitCompanion("search")).toBe("search");
    expect(startupCompanionForChoice("split", "search")).toBe("search");
  });
});
