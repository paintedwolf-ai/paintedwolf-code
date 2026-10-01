import type {
  ModelPolicy,
  ModelPolicyPatch,
  ModelRef,
  ProviderKindTemplate,
  ProviderMeta,
  ProviderModelMeta,
} from "../../api/types.ts";
import { formatModelTokenRates } from "../../cost/cost-format.ts";
import {
  tauriPlatform,
  type TauriPlatform,
} from "../../platform/runtime.ts";
import { MODELS_SETTINGS_COPY } from "./models-settings-copy.ts";
import {
  providerModelsVisibleForRoleSlot,
  modelEligibilityForSlot,
  type ModelRoleSlot,
} from "./models-role-filter.ts";

/** Optional role-layer filter for assignment pickers. */
export type ModelAssignmentRoleFilter = {
  slot: ModelRoleSlot;
  showAll?: boolean;
};

export function shortModelId(id: string): string {
  const slash = id.lastIndexOf("/");
  return slash >= 0 ? id.slice(slash + 1) : id;
}

/** Per-1M rate suffix when both input and output hints are present. */
export function modelTokenRatesLabel(
  model: ProviderModelMeta,
): string | undefined {
  if (model.input_per_1k_nano_usd == null || model.output_per_1k_nano_usd == null) {
    return undefined;
  }
  return formatModelTokenRates(model.input_per_1k_nano_usd / 1e9, model.output_per_1k_nano_usd / 1e9);
}

/** Host-projected assignment readiness. */
export function providerIsReady(provider: ProviderMeta): boolean {
  return provider.ready_to_assign === true;
}

/** Subdued catalog freshness hint; null when nothing to show. */
export function providerCatalogStatusHint(
  provider: ProviderMeta,
): string | null {
  switch (provider.catalog_status) {
    case "unavailable":
      return MODELS_SETTINGS_COPY.catalogUnavailable;
    case "stale":
      return MODELS_SETTINGS_COPY.catalogStale;
    default:
      return null;
  }
}

/** Empty discovery has a hint; failures have a separate error presentation. */
export function providerDiscoveryStatusHint(
  provider: ProviderMeta,
): string | null {
  return provider.discovery_status === "empty"
    ? MODELS_SETTINGS_COPY.discoveryEmpty
    : null;
}

export type ProviderDiscoveryFailure = {
  title: string;
  detail: string;
  nextStep: string;
};

/**
 * A failed model listing, explained from the status axes the host publishes.
 * The provider's own error text is not on the list projection, so the next
 * step points at Test connection, which reports it on demand.
 */
export function providerDiscoveryFailure(
  provider: ProviderMeta,
): ProviderDiscoveryFailure | null {
  if (provider.discovery_status !== "error") return null;
  return {
    title: MODELS_SETTINGS_COPY.discoveryFailed,
    detail: MODELS_SETTINGS_COPY.discoveryFailedDetail(
      providerEndpointHost(provider.base_url),
    ),
    nextStep: discoveryFailureNextStep(provider),
  };
}

function discoveryFailureNextStep(provider: ProviderMeta): string {
  if (providerNeedsApiKey(provider)) {
    return MODELS_SETTINGS_COPY.discoveryFailedNextStepKeyMissing;
  }
  if (!provider.configured) {
    return MODELS_SETTINGS_COPY.discoveryFailedNextStepEndpoint;
  }
  return MODELS_SETTINGS_COPY.discoveryFailedNextStep;
}

/** Compact endpoint disambiguator; a region or derived value passes through. */
export function providerEndpointHost(url: string | undefined): string {
  const raw = url?.trim() ?? "";
  if (!raw) return "";
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}

/**
 * Whether the endpoint host names this machine. A fact about the URL only:
 * a local proxy can forward anywhere. Empty and unparsable endpoints report false.
 */
export function providerEndpointIsLoopback(url: string | undefined): boolean {
  const raw = url?.trim() ?? "";
  if (!raw) return false;
  try {
    const host = new URL(raw).hostname.toLowerCase();
    return (
      host === "localhost" ||
      host === "::1" ||
      host === "[::1]" ||
      host.startsWith("127.")
    );
  } catch {
    return false;
  }
}

/** Describe setup and assignment from the host's status axes. */
export function providerStatusLabel(provider: ProviderMeta): string {
  if (providerIsReady(provider)) return MODELS_SETTINGS_COPY.statusReady;
  if (providerNeedsApiKey(provider)) {
    return MODELS_SETTINGS_COPY.statusNeedsApiKey;
  }
  if (!provider.configured) {
    return provider.requires_api_key && provider.credential_present
      ? MODELS_SETTINGS_COPY.statusNeedsSetup
      : MODELS_SETTINGS_COPY.statusUnavailable;
  }
  switch (provider.discovery_status) {
    case "error":
      return MODELS_SETTINGS_COPY.statusCannotReach;
    case "empty":
      return MODELS_SETTINGS_COPY.statusNoModels;
    case "ok":
      return MODELS_SETTINGS_COPY.statusNoAssignableModel;
    default:
      return MODELS_SETTINGS_COPY.statusNotChecked;
  }
}

/** Subdued hints for what the host observed; the key status and readiness label cover the rest. */
export function providerObservedStatusHints(provider: ProviderMeta): string[] {
  const hints: string[] = [];
  if (provider.credential_verified_at) {
    hints.push(MODELS_SETTINGS_COPY.observedCredentialsAccepted(provider.credential_verified_at));
  }
  return hints;
}

export function readyProviders(
  providers: readonly ProviderMeta[],
): ProviderMeta[] {
  return [...providers]
    .filter(
      (p) => providerIsReady(p) && providerPlatformsSupported(p.platforms),
    )
    .sort((a, b) => a.id.localeCompare(b.id));
}

export type ModelAssignmentOption = {
  provider_id: string;
  model: string;
  /** Provider + model id (no rates) — primary selectable title. */
  title: string;
  /** Muted rate suffix, e.g. `($2.50/$10 per 1M)`. */
  rates?: string;
  disabled?: boolean;
  reason?: string;
  unverified?: boolean;
};

/** Local, cloud, or custom placement in provider/model pickers, in picker order. */
const PROVIDER_PICKER_GROUP_ORDER = ["local", "cloud", "custom"] as const;

export type ProviderPickerGroup = (typeof PROVIDER_PICKER_GROUP_ORDER)[number];

const CUSTOM_PROVIDER_KINDS = new Set(["openai-compatible"]);

export function providerKindIsCustom(kind: string): boolean {
  return CUSTOM_PROVIDER_KINDS.has(kind);
}

/** Empty platforms and unknown hosts allow the provider. */
export function providerPlatformsSupported(
  platforms: readonly string[] | undefined,
  host: TauriPlatform | null = tauriPlatform(),
): boolean {
  if (!platforms || platforms.length === 0) return true;
  if (host == null) return true;
  return platforms.includes(host);
}

export function providerPickerGroupForKind(
  kind: string,
  requiresApiKey: boolean,
  baseUrl: string,
): ProviderPickerGroup {
  if (providerKindIsCustom(kind)) {
    return "custom";
  }
  return deploymentGroupFromEndpoint(requiresApiKey, baseUrl);
}

export function providerPickerGroup(
  provider: ProviderMeta,
): ProviderPickerGroup {
  return providerPickerGroupForKind(
    providerKind(provider),
    provider.requires_api_key,
    provider.base_url ?? "",
  );
}

function deploymentGroupFromEndpoint(
  requiresApiKey: boolean,
  baseUrl: string,
): ProviderPickerGroup {
  const trimmed = baseUrl.trim();
  const lower = trimmed.toLowerCase();
  if (lower.includes("localhost") || lower.includes("127.0.0.1")) {
    return "local";
  }
  // Keyless HTTP endpoints are local; derived endpoints stay hosted.
  if (!requiresApiKey && /^https?:\/\//i.test(trimmed)) {
    return "local";
  }
  return "cloud";
}

function providerPickerGroupLabel(group: ProviderPickerGroup): string {
  switch (group) {
    case "local":
      return MODELS_SETTINGS_COPY.providerGroupLocal;
    case "cloud":
      return MODELS_SETTINGS_COPY.providerGroupCloud;
    case "custom":
      return MODELS_SETTINGS_COPY.providerGroupCustom;
  }
}

export type ModelAssignmentOptionGroup = {
  group: ProviderPickerGroup;
  label: string;
  options: ModelAssignmentOption[];
};

export function modelAssignmentOptionCount(
  groups: readonly ModelAssignmentOptionGroup[],
): number {
  let n = 0;
  for (const group of groups) {
    n += group.options.length;
  }
  return n;
}

export function modelRefKey(ref: ModelRef): string {
  return `${ref.provider_id}\0${ref.model}`;
}

export function parseModelRefKey(key: string): ModelRef | null {
  const idx = key.indexOf("\0");
  if (idx <= 0 || idx >= key.length - 1) return null;
  return {
    provider_id: key.slice(0, idx),
    model: key.slice(idx + 1),
  };
}

export function modelPoliciesEqual(a: ModelPolicy, b: ModelPolicy): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

export function cloneModelPolicy(policy: ModelPolicy): ModelPolicy {
  return JSON.parse(JSON.stringify(policy)) as ModelPolicy;
}

export function policyHasModelAssignments(policy: ModelPolicy): boolean {
  const refs = [
    policy.coordinator,
    policy.lite,
    ...policy.agent_pool.models,
  ];
  return refs.some((ref) => Boolean(ref?.provider_id && ref.model));
}

/** Flat list of ready provider models for assignment dropdowns. */
export function modelAssignmentOptions(
  providers: readonly ProviderMeta[],
  filter?: ModelAssignmentRoleFilter,
): ModelAssignmentOption[] {
  const out: ModelAssignmentOption[] = [];
  for (const provider of providers.filter((p) => p.configured && providerPlatformsSupported(p.platforms))) {
    const models = filter
      ? providerModelsVisibleForRoleSlot(
          provider,
          filter.slot,
          filter.showAll === true,
        )
      : provider.models;
    for (const model of models) {
      out.push({
        provider_id: provider.id,
        model: model.id,
        title: `${providerDisplayName(provider)} — ${shortModelId(model.id)}`,
        rates: modelTokenRatesLabel(model),
        disabled: filter ? !modelEligibilityForSlot(model, filter.slot).selectable : false,
        reason: filter ? modelEligibilityForSlot(model, filter.slot).reason : undefined,
        unverified: filter ? modelEligibilityForSlot(model,filter.slot).state === "unverified" : false,
      });
    }
  }
  return out;
}

/** Model options grouped under local vs cloud provider sections. */
export function modelAssignmentOptionGroups(
  providers: readonly ProviderMeta[],
  filter?: ModelAssignmentRoleFilter,
): ModelAssignmentOptionGroup[] {
  const flat = modelAssignmentOptions(providers, filter);
  const byGroup = new Map<ProviderPickerGroup, ModelAssignmentOption[]>();
  for (const option of flat) {
    const provider = providers.find((p) => p.id === option.provider_id);
    const group = provider ? providerPickerGroup(provider) : "cloud";
    const bucket = byGroup.get(group) ?? [];
    bucket.push(option);
    byGroup.set(group, bucket);
  }
  return PROVIDER_PICKER_GROUP_ORDER.flatMap((group) => {
    const options = byGroup.get(group);
    if (!options || options.length === 0) return [];
    return [{ group, label: providerPickerGroupLabel(group), options }];
  });
}

/** Grouped dropdown options including the current assignment when it is not otherwise listed. */
export function modelAssignmentOptionGroupsForPicker(
  providers: readonly ProviderMeta[],
  value: ModelRef,
  filter?: ModelAssignmentRoleFilter,
): ModelAssignmentOptionGroup[] {
  const base = modelAssignmentOptionGroups(providers, filter);
  if (!value.provider_id || !value.model) return base;
  const key = modelRefKey(value);
  if (
    base.some((group) =>
      group.options.some((option) => modelRefKey(option) === key),
    )
  ) {
    return base;
  }
  const orphan = orphanModelAssignmentOption(value);
  const provider = providers.find((p) => p.id === value.provider_id);
  const group = provider ? providerPickerGroup(provider) : "cloud";
  const label = providerPickerGroupLabel(group);
  const existing = base.find((entry) => entry.group === group);
  if (existing) {
    return base.map((entry) =>
      entry.group === group
        ? { ...entry, options: [orphan, ...entry.options] }
        : entry,
    );
  }
  return [{ group, label, options: [orphan] }, ...base];
}

function orphanModelAssignmentOption(value: ModelRef): ModelAssignmentOption {
  return {
    provider_id: value.provider_id,
    model: value.model,
    title: formatModelAssignmentLabel(value),
    disabled: true,
    reason: "This assignment is no longer available for this role.",
  };
}

export function formatModelAssignmentLabel(ref: ModelRef): string {
  if (!ref.provider_id || !ref.model) {
    return MODELS_SETTINGS_COPY.unsetModel;
  }
  return `${ref.provider_id} — ${shortModelId(ref.model)}`;
}

export function workerModelRef(policy: ModelPolicy): ModelRef {
  return (
    policy.agent_pool.models[0] ?? { provider_id: "", model: "" }
  );
}

export function usesWorkerPool(policy: ModelPolicy): boolean {
  return policy.agent_pool.models.length > 1;
}

export function setWorkerModel(policy: ModelPolicy, ref: ModelRef): ModelPolicy {
  if (usesWorkerPool(policy)) {
    const models = [...policy.agent_pool.models];
    models[0] = ref;
    return {
      ...policy,
      agent_pool: { ...policy.agent_pool, models },
    };
  }
  return {
    ...policy,
    agent_pool: { ...policy.agent_pool, models: [ref] },
  };
}

export function setWorkerPoolEnabled(
  policy: ModelPolicy,
  enabled: boolean,
): ModelPolicy {
  if (enabled) {
    if (usesWorkerPool(policy)) return policy;
    const worker = workerModelRef(policy);
    return addPoolRow({
      ...policy,
      agent_pool: {
        ...policy.agent_pool,
        selection: "random",
        models: worker.provider_id && worker.model ? [worker] : [],
      },
    });
  }
  return {
    ...policy,
    agent_pool: {
      ...policy.agent_pool,
      models: [workerModelRef(policy)],
    },
  };
}

const EMPTY_MODEL_REF: ModelRef = { provider_id: "", model: "" };

function isAssigned(ref: ModelRef | undefined): ref is ModelRef {
  return Boolean(ref?.provider_id && ref.model);
}

/** A slot the host omits or an empty picker value both mean unassigned. */
function assignedOrNull(ref: ModelRef): ModelRef | null {
  return isAssigned(ref) ? ref : null;
}

function clearRefIfProvider(ref: ModelRef | undefined, providerId: string): ModelRef | undefined {
  return ref === undefined || ref.provider_id === providerId ? undefined : { ...ref };
}

/** Drop every policy slot that points at a removed provider instance. */
export function clearProviderFromModelPolicy(
  policy: ModelPolicy,
  providerId: string,
): ModelPolicy {
  const models = policy.agent_pool.models
    .map((ref) => clearRefIfProvider(ref, providerId))
    .filter(isAssigned);
  const next: ModelPolicy = {
    ...policy,
	...(policy.thinking_overrides ? { thinking_overrides: policy.thinking_overrides.filter((override) => override.provider_id !== providerId) } : {}),
    agent_pool: {
      ...policy.agent_pool,
      models,
    },
  };
  const coordinator = clearRefIfProvider(policy.coordinator, providerId);
  const lite = clearRefIfProvider(policy.lite, providerId);
  if (coordinator) next.coordinator = coordinator;
  else delete next.coordinator;
  if (lite) next.lite = lite;
  else delete next.lite;
  return next;
}

export function hasReadyProvider(providers: readonly ProviderMeta[]): boolean {
  return providers.some(providerIsReady);
}

/** Supported live instances for Settings lists. */
export function listedProviders(
  providers: readonly ProviderMeta[],
): ProviderMeta[] {
  return [...providers]
    .filter((provider) => providerPlatformsSupported(provider.platforms))
    .sort((a, b) => a.id.localeCompare(b.id));
}

export function providerNeedsApiKey(provider: ProviderMeta): boolean {
  return providerUsesApiKey(provider) && !provider.credential_present;
}

/** Keeps stored credentials reachable across authentication modes. */
export function providerShowsApiKeyControls(provider: ProviderMeta): boolean {
  return (
    providerUsesApiKey(provider) ||
    provider.credential_present ||
    providerKindIsCustom(providerKind(provider))
  );
}

export function providerUsesApiKey(provider: ProviderMeta): boolean {
  return provider.requires_api_key;
}

/** Human-facing name for an AI provider instance. */
export function providerDisplayName(provider: ProviderMeta): string {
  return provider.label?.trim() || provider.id;
}

/** Resolves a catalog kind label before instance labels. */
export function providerLabelForId(
  id: string | undefined,
  providers: readonly ProviderMeta[] | undefined,
  kinds: readonly ProviderKindTemplate[] | undefined,
): string | undefined {
  const wanted = id?.trim();
  if (!wanted) return undefined;
  const match = providers?.find((p) => p.id === wanted);
  if (!match) return wanted;
  const kind = providerKind(match);
  const template = kinds?.find((t) => t.kind === kind);
  return template?.label?.trim() || providerDisplayName(match);
}

export type ProviderToolCallSupport =
  ProviderModelMeta["capabilities"]["tools"]["state"];

/** Unsupported requires every model to report it; missing evidence remains unknown. */
export function providerToolCallSupport(
  provider: ProviderMeta,
): ProviderToolCallSupport {
  if (provider.models.length === 0) return "unknown";
  let allUnsupported = true;
  for (const model of provider.models) {
    const state = model.capabilities?.tools.state;
    if (state === "supported") return "supported";
    if (state !== "unsupported") allUnsupported = false;
  }
  return allUnsupported ? "unsupported" : "unknown";
}

export type ProviderKindTemplateGroup = {
  group: ProviderPickerGroup;
  label: string;
  templates: ProviderKindTemplate[];
};

/** Add-AI-provider picker kinds grouped under local, cloud, or custom sections. */
export function providerKindTemplateGroups(
  kinds: readonly ProviderKindTemplate[],
): ProviderKindTemplateGroup[] {
  const byGroup = new Map<ProviderPickerGroup, ProviderKindTemplate[]>();
  const sorted = [...kinds]
    .filter((t) => providerPlatformsSupported(t.platforms))
    .sort((a, b) => a.label.localeCompare(b.label));
  for (const template of sorted) {
    const group = providerPickerGroupForKind(
      template.kind,
      template.requires_api_key,
      template.base_url,
    );
    const bucket = byGroup.get(group) ?? [];
    bucket.push(template);
    byGroup.set(group, bucket);
  }
  return PROVIDER_PICKER_GROUP_ORDER.flatMap((group) => {
    const templates = byGroup.get(group);
    if (!templates || templates.length === 0) return [];
    return [{ group, label: providerPickerGroupLabel(group), templates }];
  });
}

/** The kind a provider instance belongs to (falls back to its own id). */
export function providerKind(provider: ProviderMeta): string {
  return provider.kind?.trim() || provider.id;
}

/** Visible instances grouped under a canonical kind name. */
export type ProviderKindGroup = {
  kind: string;
  label: string;
  providers: ProviderMeta[];
};

/** Groups instances by kind and sorts the canonical instance first. */
export function groupProvidersByKind(
  providers: readonly ProviderMeta[],
  kinds: readonly ProviderKindTemplate[] = [],
): ProviderKindGroup[] {
  const canonicalLabel = new Map<string, string>();
  for (const template of kinds) {
    canonicalLabel.set(template.kind, template.label);
  }
  const groups = new Map<string, ProviderKindGroup>();
  for (const provider of listedProviders(providers)) {
    const kind = providerKind(provider);
    let group = groups.get(kind);
    if (group === undefined) {
      group = {
        kind,
        label: canonicalLabel.get(kind) ?? providerDisplayName(provider),
        providers: [],
      };
      groups.set(kind, group);
    }
    group.providers.push(provider);
  }
  for (const group of groups.values()) {
    group.providers.sort((a, b) => {
      const aCanonical = a.id === group.kind ? 0 : 1;
      const bCanonical = b.id === group.kind ? 0 : 1;
      if (aCanonical !== bCanonical) return aCanonical - bCanonical;
      return providerDisplayName(a).localeCompare(providerDisplayName(b));
    });
  }
  return [...groups.values()].sort((a, b) => a.label.localeCompare(b.label));
}

export function slugifyProviderId(raw: string): string {
  return raw
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** An AI provider id that does not collide with an existing instance. */
export function uniqueProviderId(
  base: string,
  existing: ReadonlySet<string>,
): string {
  const root = slugifyProviderId(base) || "provider";
  if (!existing.has(root)) return root;
  for (let n = 2; ; n++) {
    const candidate = `${root}-${n}`;
    if (!existing.has(candidate)) return candidate;
  }
}

/** Returns the first unused numbered instance label. */
export function nextNumberedProviderLabel(
  base: string,
  providers: readonly ProviderMeta[],
): string {
  const used = new Set(
    providers.map((p) => providerDisplayName(p).trim().toLowerCase()),
  );
  for (let n = 1; ; n++) {
    const candidate = `${base} ${n}`;
    if (!used.has(candidate.toLowerCase())) return candidate;
  }
}

export function addPoolRow(policy: ModelPolicy): ModelPolicy {
  return {
    ...policy,
    agent_pool: {
      ...policy.agent_pool,
      models: [...policy.agent_pool.models, { provider_id: "", model: "" }],
    },
  };
}

export function removePoolRow(policy: ModelPolicy, index: number): ModelPolicy {
  const models = policy.agent_pool.models.filter((_, i) => i !== index);
  return {
    ...policy,
    agent_pool: {
      ...policy.agent_pool,
      models: models.length > 0 ? models : policy.agent_pool.models.slice(0, 1),
    },
  };
}

export const POOL_SELECTION_LABELS: Record<
  ModelPolicy["agent_pool"]["selection"],
  string
> = {
  round_robin: "Round robin",
  random: "Random",
  first: "Always first",
};

/** Coordinator slot; Settings labels this the default model. */
export function defaultModelRef(policy: ModelPolicy): ModelRef {
  return policy.coordinator ?? EMPTY_MODEL_REF;
}

/** Lite slot; empty inherits the default model. */
export function summarizerOverrideRef(policy: ModelPolicy): ModelRef {
  return policy.lite ?? EMPTY_MODEL_REF;
}

export function defaultModelPatch(ref: ModelRef): ModelPolicyPatch {
  const coordinator = assignedOrNull(ref);
  return {
    coordinator,
    agent_pool: {
      selection: "first",
      models: coordinator ? [coordinator] : [],
    },
  };
}

export function summarizerPatch(ref: ModelRef): ModelPolicyPatch {
  return { lite: assignedOrNull(ref) };
}

/** A draft's role assignments as a patch; an unassigned slot clears to null. */
export function assignmentPatch(policy: ModelPolicy): ModelPolicyPatch {
  return {
    coordinator: policy.coordinator ? assignedOrNull(policy.coordinator) : null,
    lite: policy.lite ? assignedOrNull(policy.lite) : null,
    agent_pool: policy.agent_pool,
  };
}

function modelRefsEqual(a: ModelRef | undefined, b: ModelRef | undefined): boolean {
  return a?.provider_id === b?.provider_id && a?.model === b?.model;
}

/** Slots that differ after removing a provider instance. */
export function providerRemovalPatch(
  policy: ModelPolicy,
  providerId: string,
): ModelPolicyPatch | null {
  const next = clearProviderFromModelPolicy(policy, providerId);
  if (modelPoliciesEqual(policy, next)) return null;
  const patch: ModelPolicyPatch = {};
	if (JSON.stringify(policy.thinking_overrides) !== JSON.stringify(next.thinking_overrides)) patch.thinking_overrides = next.thinking_overrides ?? [];
  if (!modelRefsEqual(policy.coordinator, next.coordinator)) {
    patch.coordinator = next.coordinator ?? null;
  }
  if (!modelRefsEqual(policy.lite, next.lite)) {
    patch.lite = next.lite ?? null;
  }
  if (JSON.stringify(policy.agent_pool) !== JSON.stringify(next.agent_pool)) {
    patch.agent_pool = next.agent_pool;
  }
  return Object.keys(patch).length > 0 ? patch : null;
}

export function hasDefaultModel(policy: ModelPolicy | undefined): boolean {
  return isAssigned(policy?.coordinator);
}
