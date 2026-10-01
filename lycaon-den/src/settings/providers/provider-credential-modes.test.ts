import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { ProviderKindTemplate, ProviderMeta } from "../../api/types.ts";
import {
  ambientAuthCopy,
  kindIsAmbientOnly,
  kindOffersCredentialChoice,
  providerCredentialSource,
} from "./provider-credential-modes.ts";

const REPO_ROOT = join(import.meta.dirname, "../../../..");
const PROVIDERS_YAML = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/host/providers.yaml",
);

/** Unique `ambient_auth:` chain ids under the ship providers list. */
function shipAmbientAuthChains(): string[] {
  const text = readFileSync(PROVIDERS_YAML, "utf8");
  const providersStart = text.indexOf("\nproviders:");
  const slice = providersStart >= 0 ? text.slice(providersStart) : text;
  const chains = [
    ...slice.matchAll(/^\s+ambient_auth:\s+([a-z0-9-]+)\s*$/gm),
  ].map((m) => m[1]!);
  return [...new Set(chains)].sort();
}

function template(
  partial: Partial<ProviderKindTemplate>,
): ProviderKindTemplate {
  return {
    kind: "demo",
    label: "Demo",
    base_url: "https://example.test",
    requires_api_key: true,
    ...partial,
  };
}

describe("ambient credential chain copy", () => {
  it("has copy for every chain the ship catalog names", () => {
    const chains = shipAmbientAuthChains();
    expect(chains.length).toBeGreaterThan(0);
    for (const chain of chains) {
      const copy = ambientAuthCopy(chain);
      expect(copy, `missing ambient auth copy for chain ${chain}`).toBeTruthy();
      expect(copy!.label.length).toBeGreaterThan(0);
      expect(copy!.detail.length).toBeGreaterThan(0);
    }
  });

  it("names every source the AWS chain reads, not just env vars", () => {
    // Calling this mode "environment variables" would mislead the ~/.aws and
    // instance-role users who are the reason it exists.
    const detail = ambientAuthCopy("aws-sdk-chain")!.detail;
    expect(detail).toContain("environment variables");
    expect(detail).toContain("~/.aws");
    expect(detail).toContain("instance role");
  });

  it("returns nothing for a kind with no ambient chain", () => {
    expect(ambientAuthCopy(undefined)).toBeUndefined();
    expect(ambientAuthCopy("")).toBeUndefined();
    expect(ambientAuthCopy("   ")).toBeUndefined();
    expect(ambientAuthCopy("not-a-chain")).toBeUndefined();
  });
});

describe("credential mode availability", () => {
  it("offers a choice only when a key mode and a chain both exist", () => {
    expect(
      kindOffersCredentialChoice(
        template({ requires_api_key: true, ambient_auth: "aws-sdk-chain" }),
      ),
    ).toBe(true);
    // Ambient-only: nothing to choose against.
    expect(
      kindOffersCredentialChoice(
        template({ requires_api_key: false, ambient_auth: "google-adc" }),
      ),
    ).toBe(false);
    // Key-only providers name no credential chain.
    expect(
      kindOffersCredentialChoice(template({ requires_api_key: true })),
    ).toBe(false);
    expect(kindOffersCredentialChoice(undefined)).toBe(false);
  });

  it("marks a chain-only kind as ambient-only", () => {
    expect(
      kindIsAmbientOnly(
        template({ requires_api_key: false, ambient_auth: "google-adc" }),
      ),
    ).toBe(true);
    expect(
      kindIsAmbientOnly(
        template({ requires_api_key: true, ambient_auth: "aws-sdk-chain" }),
      ),
    ).toBe(false);
    // Keyless local kinds (Ollama) are not ambient-cloud kinds.
    expect(kindIsAmbientOnly(template({ requires_api_key: false }))).toBe(false);
    expect(kindIsAmbientOnly(undefined)).toBe(false);
  });

  it("takes the credential source from the wire, not mode flags", () => {
    const meta = (partial: Partial<ProviderMeta>): ProviderMeta =>
      ({
        id: "demo",
        configured: false,
        credential_present: false,
        requires_api_key: false,
        features: { tool_calls: true, thinking: false, prompt_cache: "none" },
        models: [],
        ...partial,
      }) as ProviderMeta;
    expect(providerCredentialSource(meta({ credential_source: "ambient" }))).toBe(
      "ambient",
    );
    // An ambient-mode instance whose chain has not resolved reports none.
    expect(
      providerCredentialSource(
        meta({ credential_source: "none", ambient_auth: "aws-sdk-chain" }),
      ),
    ).toBe("none");
    expect(
      providerCredentialSource(meta({ credential_present: true })),
    ).toBe("stored");
  });
});
