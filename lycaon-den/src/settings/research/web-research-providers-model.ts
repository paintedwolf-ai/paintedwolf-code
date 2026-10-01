import type {
  WebResearchProviderMeta,
} from "../../api/types.ts";
import {
  WEB_RESEARCH_CATALOG,
  WEB_RESEARCH_PROVIDER_IDS,
  catalogEntry,
  type WebResearchCatalogEntry,
  type WebResearchProviderId,
  type WebResearchProviderKind,
} from "../web-research-catalog.generated.ts";
import { WEB_RESEARCH_SETTINGS_COPY } from "./web-research-settings-copy.ts";

/** The built-in direct pipeline's id in the enabled set. It has no catalog row. */
export const DIRECT_PROVIDER_ID = "direct";

export type WebResearchProviderGroup = {
  kind: WebResearchProviderKind;
  label: string;
  providers: WebResearchProviderMeta[];
};

export type WebResearchAddProviderGroup = {
  id: string;
  label: string;
  options: ReadonlyArray<{ id: string; label: string }>;
};

const KIND_LABELS: Record<WebResearchProviderKind, string> = {
  keyed: WEB_RESEARCH_SETTINGS_COPY.kindKeyed,
  keyed_extra: WEB_RESEARCH_SETTINGS_COPY.kindKeyedExtra,
  keyless_endpoint: WEB_RESEARCH_SETTINGS_COPY.kindKeylessEndpoint,
  keyless: WEB_RESEARCH_SETTINGS_COPY.kindKeyless,
};

/** Default-enabled keyless rows run inside Direct. */
export function catalogEntryIsDirectBundled(
  entry: Pick<WebResearchCatalogEntry, "kind" | "default_enabled">,
): boolean {
  return entry.kind === "keyless" && entry.default_enabled === true;
}

export function providerIdIsDirectBundled(id: string): boolean {
  if (!(WEB_RESEARCH_PROVIDER_IDS as readonly string[]).includes(id)) {
    return false;
  }
  return catalogEntryIsDirectBundled(catalogEntry(id as WebResearchProviderId));
}

export function providerStatusLabel(meta: WebResearchProviderMeta): string {
  if (meta.configured) {
    return WEB_RESEARCH_SETTINGS_COPY.statusReady;
  }
  if (meta.kind === "keyed" || meta.kind === "keyed_extra") {
    if (!meta.credential_present) return WEB_RESEARCH_SETTINGS_COPY.statusNeedsKey;
    if (meta.kind === "keyed_extra") {
      const entry = catalogEntry(meta.id as WebResearchProviderId);
      const missingExtra = entry.extra_fields?.some(
        (field) => !meta.config?.[field.name]?.trim(),
      );
      if (missingExtra) return WEB_RESEARCH_SETTINGS_COPY.statusNeedsConfig;
    }
    return WEB_RESEARCH_SETTINGS_COPY.statusNeedsConfig;
  }
  if (meta.kind === "keyless_endpoint") {
    if (!meta.config?.endpoint?.trim()) {
      const entry = catalogEntry(meta.id as WebResearchProviderId);
      if (entry.default_endpoint?.trim()) return WEB_RESEARCH_SETTINGS_COPY.statusReady;
      return WEB_RESEARCH_SETTINGS_COPY.statusNeedsEndpoint;
    }
  }
  if (meta.kind === "keyless") {
    return WEB_RESEARCH_SETTINGS_COPY.statusReady;
  }
  return WEB_RESEARCH_SETTINGS_COPY.statusNeedsConfig;
}

export function providerIsReady(meta: WebResearchProviderMeta): boolean {
  return meta.configured;
}

/** Enabled provider cards exclude rows bundled with Direct. */
export function enabledWebResearchProviders(
  providers: readonly WebResearchProviderMeta[],
  enabledIds: ReadonlySet<string>,
): WebResearchProviderMeta[] {
  return providers.filter(
    (p) => enabledIds.has(p.id) && !providerIdIsDirectBundled(p.id),
  );
}

export function groupWebResearchProvidersByKind(
  providers: readonly WebResearchProviderMeta[],
  enabledIds: ReadonlySet<string>,
): WebResearchProviderGroup[] {
  const listed = enabledWebResearchProviders(providers, enabledIds);
  const kinds = [...new Set(listed.map((p) => p.kind))];
  const order: WebResearchProviderKind[] = [
    "keyless",
    "keyed",
    "keyed_extra",
    "keyless_endpoint",
  ];
  return order
    .filter((kind) => kinds.includes(kind))
    .map((kind) => ({
      kind,
      label: KIND_LABELS[kind],
      providers: listed.filter((p) => p.kind === kind),
    }));
}

/** Addable rows exclude enabled providers and rows bundled with Direct. */
export function catalogProvidersAddable(
  enabledIds: ReadonlySet<string>,
): typeof WEB_RESEARCH_CATALOG {
  return WEB_RESEARCH_CATALOG.filter(
    (entry) => !enabledIds.has(entry.id) && !catalogEntryIsDirectBundled(entry),
  );
}

export function hasAddProviderCandidates(enabledIds: ReadonlySet<string>): boolean {
  if (!enabledIds.has(DIRECT_PROVIDER_ID)) return true;
  return catalogProvidersAddable(enabledIds).length > 0;
}

export function groupAddProviderCandidates(
  enabledIds: ReadonlySet<string>,
  directLabel: string,
): WebResearchAddProviderGroup[] {
  const groups: WebResearchAddProviderGroup[] = [];
  if (!enabledIds.has(DIRECT_PROVIDER_ID)) {
    groups.push({
      id: "builtin",
      label: WEB_RESEARCH_SETTINGS_COPY.builtinGroupLabel,
      options: [{ id: DIRECT_PROVIDER_ID, label: directLabel }],
    });
  }
  const candidates = catalogProvidersAddable(enabledIds);
  const apiKey = candidates.filter(
    (entry) => entry.kind === "keyed" || entry.kind === "keyed_extra",
  );
  if (apiKey.length > 0) {
    groups.push({
      id: "api_key",
      label: WEB_RESEARCH_SETTINGS_COPY.addGroupRequiresApiKey,
      options: apiKey.map((entry) => ({ id: entry.id, label: entry.label })),
    });
  }
  const endpoint = candidates.filter((entry) => entry.kind === "keyless_endpoint");
  if (endpoint.length > 0) {
    groups.push({
      id: "endpoint",
      label: WEB_RESEARCH_SETTINGS_COPY.addGroupEndpoint,
      options: endpoint.map((entry) => ({ id: entry.id, label: entry.label })),
    });
  }
  const keyless = candidates.filter((entry) => entry.kind === "keyless");
  if (keyless.length > 0) {
    groups.push({
      id: "no_key",
      label: WEB_RESEARCH_SETTINGS_COPY.addGroupNoKey,
      options: keyless.map((entry) => ({ id: entry.id, label: entry.label })),
    });
  }
  return groups;
}

/** The enabled set contains known standalone providers and Direct. */
export function resolveInitialEnabledProviderIds(
  status: { enabled_providers?: string[]; providers?: WebResearchProviderMeta[] },
): Set<string> {
  const catalog = status.providers
    ? new Set(status.providers.map((p) => p.id))
    : new Set<string>(WEB_RESEARCH_PROVIDER_IDS);
  const ids = new Set<string>();
  for (const id of status.enabled_providers ?? []) {
    if (providerIdIsDirectBundled(id)) continue;
    if (id === DIRECT_PROVIDER_ID || catalog.has(id)) {
      ids.add(id);
    }
  }
  return ids;
}

export function directStatusLabel(configured: boolean): string {
  return configured
    ? WEB_RESEARCH_SETTINGS_COPY.directReady
    : WEB_RESEARCH_SETTINGS_COPY.directNotEnabled;
}
