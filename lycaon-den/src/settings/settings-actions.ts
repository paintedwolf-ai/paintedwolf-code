import type { LycaonClient } from "../api/client.ts";
import type { Project } from "../api/types.ts";
import type { SettingsStore } from "../store/settings-store.ts";
import { resolveEditorSettingsScope } from "./settings-scope.ts";

export async function refreshProviders(
  store: SettingsStore,
  client: LycaonClient,
  shouldApply: () => boolean,
): Promise<void> {
  const providers = await client.listProviders();
  if (shouldApply()) store.actions.setProviders(providers);
}

/** Loads provider labels used outside Settings. */
export async function refreshProviderKinds(
  store: SettingsStore,
  client: LycaonClient,
  shouldApply: () => boolean,
): Promise<void> {
  const kinds = await client.listProviderKinds();
  if (shouldApply()) store.actions.setProviderKinds(kinds);
}

export async function refreshModelPolicy(
  store: SettingsStore,
  client: LycaonClient,
  projects: readonly Project[],
  shouldApply: () => boolean,
  projectDir?: string,
  alwaysProjectScope?: boolean,
): Promise<void> {
  const { projectId } = resolveEditorSettingsScope(
    alwaysProjectScope,
    projectDir,
    projects,
  );
  const policy = await client.getModelPolicySettings(projectId);
  if (shouldApply()) store.actions.setModelPolicy(policy);
}

export async function refreshPricing(
  store: SettingsStore,
  client: LycaonClient,
  shouldApply: () => boolean,
): Promise<void> {
  const pricing = await client.getPricingSettings();
  if (shouldApply()) store.actions.setPricing(pricing);
}

/** Panels hydrated through SettingsStore. */
const SETTINGS_STORE_PANELS = [
  "providers",
  "approvals",
  "edit-review",
  "budgets",
  "pricing",
] as const;

export type SettingsStorePanel = (typeof SETTINGS_STORE_PANELS)[number];

/** True when a section's data is hydrated by loadSettingsPanel. */
export function isSettingsStorePanel(
  section: string,
): section is SettingsStorePanel {
  return (SETTINGS_STORE_PANELS as readonly string[]).includes(section);
}

export async function loadSettingsPanel(
  store: SettingsStore,
  client: LycaonClient,
  projects: readonly Project[],
  panel: SettingsStorePanel,
  projectDir?: string,
  opts?: { alwaysProjectScope?: boolean },
): Promise<void> {
  await store.load(client, panel, async () => {
    const { projectId } = resolveEditorSettingsScope(opts?.alwaysProjectScope, projectDir, projects);
    switch (panel) {
      case "providers": {
        const [providers, modelPolicy] = await Promise.all([
          client.listProviders(),
          client.getModelPolicySettings(projectId),
        ]);
        return { providers, modelPolicy };
      }
      case "approvals":
        return { approvals: await client.getApprovalsSettings(projectId) };
      case "edit-review":
        return { review: await client.getReviewSettings(projectId) };
      case "budgets":
        return { limits: await client.getLimitsSettings() };
      case "pricing":
        return { pricing: await client.getPricingSettings() };
    }
  });
}

export type SettingsInvalidationSlice =
  | "providers"
  | "model_policy"
  | "approvals"
  | "limits"
  | "pricing"
  | "review";

const effectiveLimitRefreshes = new WeakMap<SettingsStore, object>();

async function refreshEffectiveLimits(store: SettingsStore, client: LycaonClient): Promise<void> {
  const projectId = store.state.effectiveLimits?.projectId;
  const request = {};
  effectiveLimitRefreshes.set(store, request);
  if (!projectId) return;
  const limits = await client.getLimitsSettings(projectId);
  if (effectiveLimitRefreshes.get(store) !== request ||
    store.state.effectiveLimits?.projectId !== projectId) return;
  store.actions.setEffectiveLimits(projectId, limits);
}

export async function refreshSettingsSlices(
  store: SettingsStore,
  client: LycaonClient,
  projects: readonly Project[],
  slices: readonly SettingsInvalidationSlice[],
  projectDir?: string,
): Promise<void> {
  store.invalidate();
  const panels = new Set<SettingsStorePanel>();
  if (slices.includes("providers") || slices.includes("model_policy")) panels.add("providers");
  if (slices.includes("approvals")) panels.add("approvals");
  if (slices.includes("limits")) panels.add("budgets");
  if (slices.includes("pricing")) panels.add("pricing");
  if (slices.includes("review")) panels.add("edit-review");
  await Promise.all([
    ...[...panels].map((panel) => loadSettingsPanel(store, client, projects, panel, projectDir)),
    ...(slices.includes("limits") ? [refreshEffectiveLimits(store, client)] : []),
  ]);
}
