/** Unseen releases wait for onboarding and nonempty notes. */

export type WhatsNewGateDecision =
  | { kind: "hide" }
  | { kind: "seed"; version: string }
  | { kind: "advance"; version: string }
  | { kind: "show"; version: string; notes: string };

export function decideWhatsNew(input: {
  currentVersion: string | null | undefined;
  lastSeenVersion: string | undefined;
  notes: string;
  prefsReady: boolean;
  onboardingHardGate: boolean;
}): WhatsNewGateDecision {
  if (!input.prefsReady) return { kind: "hide" };
  const current = input.currentVersion?.trim();
  if (!current) return { kind: "hide" };

  if (input.lastSeenVersion === undefined) {
    return { kind: "seed", version: current };
  }

  if (input.lastSeenVersion === current) {
    return { kind: "hide" };
  }

  if (input.onboardingHardGate) {
    return { kind: "hide" };
  }

  const notes = input.notes.trim();
  if (!notes) {
    return { kind: "advance", version: current };
  }

  return { kind: "show", version: current, notes };
}
