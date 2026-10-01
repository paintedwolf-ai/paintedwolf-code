import { LycaonApiError } from "../../../api/http.ts";
import type { ProviderKindTemplate, ProviderMeta, UpdateProviderRequest } from "../../../api/types.ts";
import { CLIENT_NOTICES } from "../../../notices/client-notices.generated.ts";
import {
  groupProvidersByKind,
  providerCatalogStatusHint,
  providerDiscoveryStatusHint,
  providerEndpointIsLoopback,
  providerKindIsCustom,
  providerObservedStatusHints,
  providerToolCallSupport,
} from "../../../settings/providers/models-editor-model.ts";
import { kindIsAmbientOnly, kindOffersCredentialChoice } from "../../../settings/providers/provider-credential-modes.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";

/** Provider labels share the project label limit. */
export const PROVIDER_LABEL_MAX = 64;

export type ProviderCardDraft = {
  apiKey: string;
  baseUrl: string;
  models: string;
  expanded: boolean;
  testing: boolean;
  refreshing: boolean;
  savingEndpoint: boolean;
  savingModels: boolean;
  savingSecretTrust: boolean;
  testMessage?: string;
  refreshFailure?: ProviderRefreshFailure;
};

type ProviderRefreshFailure = {
  title: string;
  message: string;
  suggestedAction?: string;
};

/** Preserves structured error copy. */

export function refreshFailureCopy(err: unknown): ProviderRefreshFailure {
  if (err instanceof LycaonApiError) {
    const fallback = CLIENT_NOTICES.model_catalog_refresh_failed;
    return {
      title: err.title?.trim() || fallback.title,
      message: err.message.trim() || fallback.message,
      suggestedAction: err.suggestedAction?.trim() || undefined,
    };
  }
  return { ...CLIENT_NOTICES.model_catalog_refresh_failed };
}

export type NewProviderForm = {
  kind: string;
  label: string;
  baseUrl: string;
  models: string;
  apiKey: string;
  requireApiKey: boolean;
  busy: boolean;
  error?: string;
};

export function emptyCardUI(): ProviderCardDraft {
  return {
    apiKey: "",
    baseUrl: "",
    models: "",
    expanded: false,
    testing: false,
    refreshing: false,
    savingEndpoint: false,
    savingModels: false,
    savingSecretTrust: false,
  };
}

export function emptyForm(): NewProviderForm {
  return {
    kind: "",
    label: "",
    baseUrl: "",
    models: "",
    apiKey: "",
    requireApiKey: false,
    busy: false,
    error: undefined,
  };
}

type ProviderEndpointStyle = "url" | "region" | "derived";

export function endpointStyle(value: string | undefined): ProviderEndpointStyle {
  switch (value) {
    case "region":
    case "derived":
      return value;
    default:
      return "url";
  }
}

export function endpointLabel(style: ProviderEndpointStyle): string {
  return style === "region"
    ? MODELS_SETTINGS_COPY.providerRegionLabel
    : MODELS_SETTINGS_COPY.providerBaseUrlLabel;
}

export function formatConfiguredModels(provider: ProviderMeta): string {
  return (provider.configured_models ?? [])
    .map((model) =>
      model.priced_as ? `${model.id}=${model.priced_as}` : model.id,
    )
    .join("\n");
}

export function parseConfiguredModels(
  raw: string,
): NonNullable<UpdateProviderRequest["models"]> {
  const seen = new Set<string>();
  const models: NonNullable<UpdateProviderRequest["models"]> = [];
  for (const token of raw.split(/[\n,]/)) {
    const [idPart, pricedAsPart] = token.split("=", 2);
    const id = idPart?.trim() ?? "";
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const pricedAs = pricedAsPart?.trim();
    models.push({ id, ...(pricedAs ? { priced_as: pricedAs } : {}) });
  }
  return models;
}

/** Resolves key visibility from kind and authentication mode. */

export function formShowsApiKeyField(
  kind: string,
  tmpl: ProviderKindTemplate | undefined,
  requireApiKey: boolean,
): boolean {
  if (kindOffersCredentialChoice(tmpl)) return requireApiKey;
  if (kindIsAmbientOnly(tmpl)) return false;
  return providerKindIsCustom(kind) || (tmpl?.requires_api_key ?? false);
}

/** Copy for a provider whose tool-call support is not `supported`. */

export function toolCallStateCopy(provider: ProviderMeta): string {
  if (providerToolCallSupport(provider) === "unsupported") {
    return MODELS_SETTINGS_COPY.noToolCallsWarning;
  }
  if (provider.models.length === 0) {
    const discovery = provider.discovery_status;
    if (discovery === "ok") return MODELS_SETTINGS_COPY.toolCallsUnknownFilteredModels;
    if (discovery === "empty") return MODELS_SETTINGS_COPY.toolCallsUnknownEmptyModels;
  }
  return provider.models.length === 0
    ? MODELS_SETTINGS_COPY.toolCallsUnknownNoModels
    : MODELS_SETTINGS_COPY.toolCallsUnknownNoEvidence;
}

/** Only an explicit URL supplies an endpoint host. */

export function secretScreenTrustEndpointNote(
  provider: ProviderMeta,
): string | undefined {
  if (endpointStyle(provider.endpoint_style) !== "url") return undefined;
  return providerEndpointIsLoopback(provider.base_url)
    ? MODELS_SETTINGS_COPY.secretScreenTrustLoopbackNote
    : MODELS_SETTINGS_COPY.secretScreenTrustRemoteNote;
}

export function providerFeedStatusHints(provider: ProviderMeta): string[] {
  const hints: string[] = [...providerObservedStatusHints(provider)];
  const catalog = providerCatalogStatusHint(provider);
  if (catalog) hints.push(catalog);
  const discovery = providerDiscoveryStatusHint(provider);
  if (discovery) hints.push(discovery);
  return hints;
}

export function flattenGroupedProviders(
  grouped: ReturnType<typeof groupProvidersByKind>,
): ProviderMeta[] {
  return grouped.flatMap((group) => group.providers);
}
