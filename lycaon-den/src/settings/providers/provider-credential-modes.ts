import type { ProviderKindTemplate, ProviderMeta } from "../../api/types.ts";
import { providerKind } from "./models-editor-model.ts";

export type AmbientAuthCopy = {
  /** Radio option / status label. */
  label: string;
  /** One line naming what the chain actually reads, in the user's terms. */
  detail: string;
};

/** Credential-chain labels follow the catalog ambient_auth value. */
const AMBIENT_AUTH_COPY = {
  "aws-sdk-chain": {
    label: "Use ambient AWS credentials",
    detail:
      "Resolved by the AWS SDK: environment variables, ~/.aws, or an instance role.",
  },
  "google-adc": {
    label: "Use ambient Google credentials",
    detail:
      "Application Default Credentials, plus the project and region variables gcloud sets.",
  },
} as const satisfies Record<string, AmbientAuthCopy>;

export type AmbientAuthChain = keyof typeof AMBIENT_AUTH_COPY;

/** Copy for a chain id, or undefined for a kind with no ambient option. */
export function ambientAuthCopy(
  chain: string | undefined,
): AmbientAuthCopy | undefined {
  const key = chain?.trim() ?? "";
  if (!key) return undefined;
  return AMBIENT_AUTH_COPY[key as AmbientAuthChain];
}

/** Mode selection requires both a named ambient chain and API-key support. */
export function kindOffersCredentialChoice(
  tmpl: ProviderKindTemplate | undefined,
): boolean {
  return Boolean(tmpl?.ambient_auth) && (tmpl?.requires_api_key ?? false);
}

/**
 * Whether this kind can only authenticate ambiently (Vertex: no pasteable
 * credential covers the same models). Such kinds show an explainer, not a
 * choice.
 */
export function kindIsAmbientOnly(
  tmpl: ProviderKindTemplate | undefined,
): boolean {
  return Boolean(tmpl?.ambient_auth) && !(tmpl?.requires_api_key ?? false);
}

export type ProviderCredentialSource = "none" | "stored" | "ambient";

/** The host reports the resolved credential source; unresolved ambient chains report none. */
export function providerCredentialSource(
  provider: ProviderMeta,
): ProviderCredentialSource {
  return (
    provider.credential_source ??
    (provider.credential_present ? "stored" : "none")
  );
}

/** The kind template an instance belongs to, if the catalog still lists it. */
export function templateForProvider(
  provider: ProviderMeta,
  kinds: readonly ProviderKindTemplate[],
): ProviderKindTemplate | undefined {
  const kind = providerKind(provider);
  return kinds.find((k) => k.kind === kind);
}
