import { createSignal } from "solid-js";

export type PrettySurfaceCapability = {
  enabled: () => boolean;
  available?: () => boolean;
  tip?: () => string | undefined;
  onToggle: () => boolean | void;
};

export type PrettyStatusToggle = {
  enabled: boolean;
  available: boolean;
  tip?: string;
  onToggle: () => void;
};

const prefByProjectTab = new Map<string, boolean>();
const capabilityByProjectTab = new Map<string, PrettySurfaceCapability>();
const [prettyStateVersion, setPrettyStateVersion] = createSignal(0);

function bumpPrettyState(): void {
  setPrettyStateVersion((version) => version + 1);
}

function projectTabEntryKey(projectId: string, tabKey: string): string {
  return `${projectId}\u0000${tabKey}`;
}

export function isPrettyDisplayEnabled(projectId: string, tabKey: string | null | undefined): boolean {
  prettyStateVersion();
  if (!projectId || !tabKey) return false;
  return prefByProjectTab.get(projectTabEntryKey(projectId, tabKey)) === true;
}

export function setPrettyDisplayEnabled(
  projectId: string,
  tabKey: string | null | undefined,
  enabled: boolean,
): void {
  if (!projectId || !tabKey) return;
  const key = projectTabEntryKey(projectId, tabKey);
  if (prefByProjectTab.get(key) === enabled) return;
  prefByProjectTab.set(key, enabled);
  bumpPrettyState();
}

export function togglePrettyDisplayPref(projectId: string, tabKey: string | null | undefined): boolean {
  if (!projectId || !tabKey) return false;
  const next = !isPrettyDisplayEnabled(projectId, tabKey);
  setPrettyDisplayEnabled(projectId, tabKey, next);
  return next;
}

export function clearPrettyDisplayTab(projectId: string, tabKey: string | null | undefined): void {
  if (!projectId || !tabKey) return;
  const key = projectTabEntryKey(projectId, tabKey);
  const removedPref = prefByProjectTab.delete(key);
  const removedCap = capabilityByProjectTab.delete(key);
  if (removedPref || removedCap) bumpPrettyState();
}

export function clearPrettyDisplayProject(projectId: string): void {
  if (!projectId) return;
  const prefix = `${projectId}\u0000`;
  let changed = false;
  for (const key of Array.from(prefByProjectTab.keys())) {
    if (key.startsWith(prefix)) {
      prefByProjectTab.delete(key);
      changed = true;
    }
  }
  for (const key of Array.from(capabilityByProjectTab.keys())) {
    if (key.startsWith(prefix)) {
      capabilityByProjectTab.delete(key);
      changed = true;
    }
  }
  if (changed) bumpPrettyState();
}

export function registerPrettySurfaceCapability(
  projectId: string,
  tabKey: string,
  capability: PrettySurfaceCapability,
): () => void {
  if (!projectId || !tabKey) return () => {};
  const key = projectTabEntryKey(projectId, tabKey);
  capabilityByProjectTab.set(key, capability);
  bumpPrettyState();
  return () => {
    if (capabilityByProjectTab.get(key) === capability) {
      capabilityByProjectTab.delete(key);
      bumpPrettyState();
    }
  };
}

export function getPrettySurfaceCapability(
  projectId: string,
  tabKey: string | null | undefined,
): PrettySurfaceCapability | undefined {
  prettyStateVersion();
  if (!projectId || !tabKey) return undefined;
  return capabilityByProjectTab.get(projectTabEntryKey(projectId, tabKey));
}

export function invokePrettyToggleForTab(
  projectId: string,
  tabKey: string | null | undefined,
): boolean {
  const capability = getPrettySurfaceCapability(projectId, tabKey);
  if (!capability) return false;
  if (capability.available && !capability.available()) return false;
  const handled = capability.onToggle();
  bumpPrettyState();
  return handled !== false;
}

export function prettyStatusToggleForTab(
  projectId: string,
  tabKey: string | null | undefined,
): PrettyStatusToggle {
  const capability = getPrettySurfaceCapability(projectId, tabKey);
  const available = Boolean(capability && (!capability.available || capability.available()));
  const enabled = available && capability ? capability.enabled() : false;
  const tip = capability?.tip?.();
  return {
    enabled,
    available,
    tip,
    onToggle: () => {
      invokePrettyToggleForTab(projectId, tabKey);
    },
  };
}

export function resetPrettyDisplayStateForTests(): void {
  prefByProjectTab.clear();
  capabilityByProjectTab.clear();
  bumpPrettyState();
}
