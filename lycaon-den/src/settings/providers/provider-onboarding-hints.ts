import type { ProviderMeta } from "../../api/types.ts";
import { providerKind, readyProviders } from "./models-editor-model.ts";

/**
 * First-run Default model hints keyed by ship provider kind.
 * One sentence on what tends to work as Coordinator for that AI provider.
 */
export const PROVIDER_ONBOARDING_MODEL_HINTS = {
  openai: "GPT-5.6 Sol or Terra work well.",
  anthropic: "Claude Sonnet and Opus both work well.",
  together: "Prefer Kimi K2.7 Code or K2.6, or another strong model with tools.",
  "openai-compatible": "Use a strong model with function calling on your host.",
  azure: "Deploy a GPT model with tool calling, then select that deployment.",
  ollama:
    "Needs a large instruct model with tools; usually more memory than a typical laptop.",
  lmstudio:
    "Needs a large instruct model with tools; usually more memory than a typical laptop.",
  omlx:
    "Needs a large MLX chat model with tools — usually a Mac with plenty of unified memory.",
  "litellm-proxy": "Route the proxy to a strong upstream model with tools.",
  gemini: "Gemini 3.5 Flash or a Pro model work well.",
  fireworks: "Prefer Kimi K2.7 Code or K2.6, or another strong model with tools.",
  bedrock: "Claude Sonnet or Opus with tool use work well.",
  vertex: "Gemini Flash or Pro models work well.",
  "vertex-express":
    "Gemini Flash or Pro models work well; express mode serves a subset.",
  openrouter:
    "Claude Sonnet, Gemini Flash, or Kimi — pick a strong model with tools.",
  "cloudflare-workers-ai":
    "Prefer large Workers AI models with tools; small ones struggle.",
} as const satisfies Record<string, string>;

export type ProviderOnboardingKind = keyof typeof PROVIDER_ONBOARDING_MODEL_HINTS;

/** Hint for a ship provider kind, if we have one. */
export function onboardingModelHintForKind(
  kind: string,
): string | undefined {
  const hint =
    PROVIDER_ONBOARDING_MODEL_HINTS[
      kind as ProviderOnboardingKind
    ];
  return hint;
}

/**
 * Resolve the onboarding Default model hint from the selected / ready provider.
 * Prefers the default-model assignment's provider when set.
 */
export function onboardingModelHintForProviders(
  providers: readonly ProviderMeta[],
  opts?: {
    defaultProviderId?: string;
  },
): string | undefined {
  const defaultId = opts?.defaultProviderId?.trim() ?? "";
  if (defaultId) {
    const assigned = providers.find((p) => p.id === defaultId);
    if (assigned) {
      return onboardingModelHintForKind(providerKind(assigned));
    }
  }
  for (const provider of readyProviders(providers)) {
    const hint = onboardingModelHintForKind(providerKind(provider));
    if (hint) return hint;
  }
  return undefined;
}
