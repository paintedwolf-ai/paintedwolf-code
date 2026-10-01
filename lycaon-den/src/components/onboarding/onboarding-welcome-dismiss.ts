/** Registers the visible onboarding page as the Escape sink. */
let gateDismissSink: (() => void) | null = null;

export function setOnboardingWelcomeDismissSink(
  sink: (() => void) | null,
): void {
  gateDismissSink = sink;
}

/** @returns true when the gate handled Escape. */
export function tryDismissOnboardingWelcome(): boolean {
  if (!gateDismissSink) return false;
  gateDismissSink();
  return true;
}
