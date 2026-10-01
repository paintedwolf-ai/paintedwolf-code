import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { DEFAULT_SPLIT_COMPANION } from "../../shell/stage-placement.ts";

/** Gate pages in walking order; the progress cue counts this list. */
export const ONBOARDING_GATE_PAGES = [
  "setup",
  "summarizer",
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

export function shouldShowOnboardingHardGate(input: {
  prefsReady: boolean;
  firstRunSetupCompleted: boolean;
}): boolean {
  return input.prefsReady && !input.firstRunSetupCompleted;
}

export function shouldSuppressShellForFirstRun(input: {
  prefsReady: boolean;
  firstRunSetupCompleted: boolean;
}): boolean {
  return !input.prefsReady || !input.firstRunSetupCompleted;
}

export function onboardingGatePage(needsConfig: boolean): OnboardingGatePage {
  return needsConfig ? "setup" : "summarizer";
}

export function onboardingGateStep(page: OnboardingGatePage): number {
  return ONBOARDING_GATE_PAGES.indexOf(page) + 1;
}

export function onboardingNextPage(
  page: OnboardingGatePage,
): OnboardingGatePage | null {
  return ONBOARDING_GATE_PAGES[ONBOARDING_GATE_PAGES.indexOf(page) + 1] ?? null;
}

export function onboardingPreviousPage(
  page: OnboardingGatePage,
): OnboardingGatePage | null {
  const index = ONBOARDING_GATE_PAGES.indexOf(page);
  return index > 0 ? (ONBOARDING_GATE_PAGES[index - 1] ?? null) : null;
}

/**
 * Escape leaves the gate from Setup, where it is the same as Skip, and from
 * the last page; between them it steps forward without latching.
 */
export function onboardingEscapeTarget(
  page: OnboardingGatePage,
): OnboardingGatePage | "finish" {
  if (page === "setup") return "finish";
  return onboardingNextPage(page) ?? "finish";
}

/** Which half of the setup step is still missing, in the order it is fixed. */
export type OnboardingSetupBlocker = "provider" | "default_model";

/** The step the gate is waiting for, or null when Continue may proceed. */
export function onboardingSetupBlocker(input: {
  readyProviderCount: number;
  hasDefaultModel: boolean;
}): OnboardingSetupBlocker | null {
  if (input.readyProviderCount === 0) return "provider";
  if (!input.hasDefaultModel) return "default_model";
  return null;
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
