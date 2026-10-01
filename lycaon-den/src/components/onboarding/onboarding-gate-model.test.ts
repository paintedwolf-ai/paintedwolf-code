import { describe, expect, it } from "vitest";
import {
  ONBOARDING_GATE_STEP_COUNT,
  needsOnboardingConfig,
  onboardingEscapeTarget,
  onboardingGatePage,
  onboardingGateStep,
  onboardingLayoutChoice,
  onboardingNextPage,
  onboardingPreviousPage,
  onboardingSetupBlocker,
  onboardingSplitCompanion,
  shouldShowOnboardingHardGate,
  shouldSuppressShellForFirstRun,
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
    prefsReady: true,
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

describe("shouldSuppressShellForFirstRun", () => {
  it("suppresses home while unlatched after prefs load", () => {
    expect(
      shouldSuppressShellForFirstRun({
        prefsReady: true,
        firstRunSetupCompleted: false,
      }),
    ).toBe(true);
  });

  it("never suppresses after the latch", () => {
    expect(
      shouldSuppressShellForFirstRun({
        prefsReady: true,
        firstRunSetupCompleted: true,
      }),
    ).toBe(false);
  });

  it("suppresses until prefs load", () => {
    expect(
      shouldSuppressShellForFirstRun({
        prefsReady: false,
        firstRunSetupCompleted: false,
      }),
    ).toBe(true);
  });
});

describe("onboardingGatePage", () => {
  it("routes setup vs summarizer from needsConfig (mid-flow → summarizer)", () => {
    expect(onboardingGatePage(true)).toBe("setup");
    expect(onboardingGatePage(false)).toBe("summarizer");
  });
});

describe("onboarding page order", () => {
  it("counts progress from the page list", () => {
    expect(ONBOARDING_GATE_STEP_COUNT).toBe(4);
    expect(onboardingGateStep("setup")).toBe(1);
    expect(onboardingGateStep("summarizer")).toBe(2);
    expect(onboardingGateStep("layout")).toBe(3);
    expect(onboardingGateStep("welcome")).toBe(4);
  });

  it("walks forward and back through the layout page", () => {
    expect(onboardingNextPage("summarizer")).toBe("layout");
    expect(onboardingNextPage("layout")).toBe("welcome");
    expect(onboardingNextPage("welcome")).toBeNull();
    expect(onboardingPreviousPage("welcome")).toBe("layout");
    expect(onboardingPreviousPage("layout")).toBe("summarizer");
    expect(onboardingPreviousPage("setup")).toBeNull();
  });

  it("escapes from the ends and steps forward between them", () => {
    expect(onboardingEscapeTarget("setup")).toBe("finish");
    expect(onboardingEscapeTarget("summarizer")).toBe("layout");
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

describe("onboardingSetupBlocker", () => {
  it("requires both a ready provider and a default model", () => {
    expect(
      onboardingSetupBlocker({ readyProviderCount: 1, hasDefaultModel: true }),
    ).toBeNull();
  });

  // The gate cannot say what it is waiting for from a bare boolean.
  it("names which half is missing", () => {
    expect(
      onboardingSetupBlocker({ readyProviderCount: 0, hasDefaultModel: true }),
    ).toBe("provider");
    expect(
      onboardingSetupBlocker({ readyProviderCount: 1, hasDefaultModel: false }),
    ).toBe("default_model");
    // No ready provider outranks the model choice — it is fixed first.
    expect(
      onboardingSetupBlocker({ readyProviderCount: 0, hasDefaultModel: false }),
    ).toBe("provider");
  });
});
