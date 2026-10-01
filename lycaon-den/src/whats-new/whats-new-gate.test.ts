import { describe, expect, it } from "vitest";
import { decideWhatsNew } from "./whats-new-gate.ts";

describe("decideWhatsNew", () => {
  it("hides until prefs and /health version are ready", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.2.0",
        lastSeenVersion: undefined,
        notes: "notes",
        prefsReady: false,
        onboardingHardGate: false,
      }),
    ).toEqual({ kind: "hide" });
    expect(
      decideWhatsNew({
        currentVersion: null,
        lastSeenVersion: undefined,
        notes: "notes",
        prefsReady: true,
        onboardingHardGate: false,
      }),
    ).toEqual({ kind: "hide" });
  });

  it("seeds silently when the latch is unset", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.1.0",
        lastSeenVersion: undefined,
        notes: "### Added\n\n- stuff",
        prefsReady: true,
        onboardingHardGate: false,
      }),
    ).toEqual({ kind: "seed", version: "0.1.0" });
  });

  it("hides when latch equals current", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.1.0",
        lastSeenVersion: "0.1.0",
        notes: "notes",
        prefsReady: true,
        onboardingHardGate: false,
      }),
    ).toEqual({ kind: "hide" });
  });

  it("shows when version changed and notes exist", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.2.0",
        lastSeenVersion: "0.1.0",
        notes: "### Added\n\n- New thing",
        prefsReady: true,
        onboardingHardGate: false,
      }),
    ).toEqual({
      kind: "show",
      version: "0.2.0",
      notes: "### Added\n\n- New thing",
    });
  });

  it("advances the latch with no card when notes are empty", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.2.0",
        lastSeenVersion: "0.1.0",
        notes: "  \n",
        prefsReady: true,
        onboardingHardGate: false,
      }),
    ).toEqual({ kind: "advance", version: "0.2.0" });
  });

  it("defers the card while onboarding hard gate is active", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.2.0",
        lastSeenVersion: "0.1.0",
        notes: "### Added\n\n- New",
        prefsReady: true,
        onboardingHardGate: true,
      }),
    ).toEqual({ kind: "hide" });
  });

  it("still seeds on first run even during onboarding", () => {
    expect(
      decideWhatsNew({
        currentVersion: "0.1.0",
        lastSeenVersion: undefined,
        notes: "notes",
        prefsReady: true,
        onboardingHardGate: true,
      }),
    ).toEqual({ kind: "seed", version: "0.1.0" });
  });
});
