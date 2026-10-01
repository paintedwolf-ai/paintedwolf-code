import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { ProviderMeta } from "../../api/types.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import {
  PROVIDER_ONBOARDING_MODEL_HINTS,
  onboardingModelHintForKind,
  onboardingModelHintForProviders,
} from "./provider-onboarding-hints.ts";

const REPO_ROOT = join(import.meta.dirname, "../../../..");
const PROVIDERS_YAML = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/host/providers.yaml",
);

/** Unique `kind:` values under the ship providers list. */
function shipProviderKinds(): string[] {
  const text = readFileSync(PROVIDERS_YAML, "utf8");
  const providersStart = text.indexOf("\nproviders:");
  const slice = providersStart >= 0 ? text.slice(providersStart) : text;
  const kinds = [
    ...slice.matchAll(/^\s+kind:\s+([a-z0-9-]+)\s*$/gm),
  ].map((m) => m[1]!);
  return [...new Set(kinds)].sort();
}

function provider(
  partial: Partial<ProviderMeta> & Pick<ProviderMeta, "id" | "kind">,
): ProviderMeta {
  return {
    configured: true,
    credential_present: true,
    requires_api_key: true,
    ready_to_assign: true,
    features: {
      tool_calls: true,
      thinking: true,
      prompt_cache: "automatic_prefix",
    },
    models: [providerModel("demo-model")],
    ...partial,
  };
}

describe("PROVIDER_ONBOARDING_MODEL_HINTS", () => {
  it("is a bijection with ship provider kinds (honest dual — kinds are membership)", () => {
    const kinds = shipProviderKinds();
    expect(kinds.length).toBeGreaterThan(0);
    const hintKeys = Object.keys(PROVIDER_ONBOARDING_MODEL_HINTS).sort();
    expect(hintKeys).toEqual(kinds);
    for (const kind of kinds) {
      const hint = onboardingModelHintForKind(kind);
      expect(hint, `missing onboarding hint for kind ${kind}`).toBeTruthy();
      expect(hint!.trim().length).toBeGreaterThan(20);
      expect(hint!.includes("\n")).toBe(false);
    }
  });

  it("does not use a generated onboarding-kinds stub", () => {
    // Reject generated kind stubs.
    const generated = join(
      REPO_ROOT,
      "lycaon-den/src/settings/provider-onboarding-kinds.generated.ts",
    );
    expect(() => readFileSync(generated, "utf8")).toThrow();
  });

  it("returns the Anthropic Coordinator guidance", () => {
    expect(onboardingModelHintForKind("anthropic")).toBe(
      "Claude Sonnet and Opus both work well.",
    );
  });
});

describe("onboardingModelHintForProviders", () => {
  it("prefers the default-model provider kind", () => {
    const providers = [
      provider({ id: "ollama", kind: "ollama", requires_api_key: false }),
      provider({ id: "anthropic", kind: "anthropic" }),
    ];
    expect(
      onboardingModelHintForProviders(providers, {
        defaultProviderId: "anthropic",
      }),
    ).toBe(PROVIDER_ONBOARDING_MODEL_HINTS.anthropic);
  });

  it("falls back to the first ready provider", () => {
    const providers = [
      provider({ id: "fireworks", kind: "fireworks" }),
      provider({ id: "gemini", kind: "gemini" }),
    ];
    expect(onboardingModelHintForProviders(providers)).toBe(
      PROVIDER_ONBOARDING_MODEL_HINTS.fireworks,
    );
  });
});
