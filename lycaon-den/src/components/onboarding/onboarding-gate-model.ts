import type { LocalGenerationStatus } from "../../api/types.ts";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { DEFAULT_SPLIT_COMPANION } from "../../shell/stage-placement.ts";

/** Gate pages in walking order; the progress cue counts this list. */
export const ONBOARDING_GATE_PAGES = [
  "local_model",
  "layout",
  "welcome",
] as const;

export type OnboardingGatePage = (typeof ONBOARDING_GATE_PAGES)[number];

export const ONBOARDING_GATE_STEP_COUNT = ONBOARDING_GATE_PAGES.length;

export function needsOnboardingConfig(input: {
  readyProviderCount: number;
  hasDefaultModel: boolean;
}): boolean {
  return input.readyProviderCount === 0 || !input.hasDefaultModel;
}

export function localModelSetup(runtime?: LocalGenerationStatus) {
  if (runtime?.offline_installer) return "packaged";
  const decision = runtime?.selection_state;
  if (decision === "selected" || decision === "opted_out") return "decided";
  if (!decision && (runtime?.selected_model || runtime?.state === "opted_out")) return "decided";
  if (!runtime?.available_models) return "unknown";
  return runtime.available_models.some(tier => tier.eligible && tier.download_available) ? "available" : "deferred";
}

export function shouldShowOnboardingHardGate(input: {
  prefsReady: boolean;
  firstRunSetupCompleted: boolean;
  localModelOnboardingCompleted: boolean;
  runtime?: LocalGenerationStatus;
}): boolean {
  if (!input.prefsReady) return false;
  if (!input.firstRunSetupCompleted) return true;
  if (input.localModelOnboardingCompleted) return false;
  return localModelSetup(input.runtime) === "available";
}

export function onboardingGateStep(page: OnboardingGatePage): number {
  return ONBOARDING_GATE_PAGES.indexOf(page) + 1;
}

export function onboardingEscapeTarget(page: OnboardingGatePage): OnboardingGatePage | "finish" {
  return page === "layout" ? "welcome" : "finish";
}

/** The two starting layouts first run offers; Settings lists every companion. */
export type OnboardingLayoutChoice = "chat" | "split";

export function onboardingLayoutChoice(
  startupCompanion: ContextNavItemId | null,
): OnboardingLayoutChoice {
  return startupCompanion ? "split" : "chat";
}

/** The companion a split choice keeps: the one already set, else Files. */
export function onboardingSplitCompanion(
  startupCompanion: ContextNavItemId | null,
): ContextNavItemId {
  return startupCompanion ?? DEFAULT_SPLIT_COMPANION;
}

/** The Open at launch value a choice writes. */
export function startupCompanionForChoice(
  choice: OnboardingLayoutChoice,
  startupCompanion: ContextNavItemId | null,
): ContextNavItemId | null {
  return choice === "chat" ? null : onboardingSplitCompanion(startupCompanion);
}
