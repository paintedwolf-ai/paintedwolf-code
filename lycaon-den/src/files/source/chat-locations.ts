import { createSignal } from "solid-js";
import type { SourceChatLocation } from "../../api/types.ts";

// Tree addresses use location IDs. History uses the outside namespace.
const [locationsByProject, setLocationsByProject] = createSignal<ReadonlyMap<string, readonly SourceChatLocation[]>>(new Map());
const [epochs, setEpochs] = createSignal<ReadonlyMap<string, number>>(new Map());

export function recordChatLocations(projectId: string, locations: readonly SourceChatLocation[]): void {
  const current = chatLocations(projectId);
  if (current === locations || JSON.stringify(current) === JSON.stringify(locations)) return;
  const next = new Map(locationsByProject());
  next.set(projectId, locations);
  setLocationsByProject(next);
}

export function chatLocations(projectId: string): readonly SourceChatLocation[] {
  return locationsByProject().get(projectId) ?? [];
}

export function chatLocation(projectId: string, rootId: string): SourceChatLocation | undefined {
  return chatLocations(projectId).find((location) => location.id === rootId);
}

export function chatLocationsEpoch(projectId: string): number {
  return epochs().get(projectId) ?? 0;
}

export function refreshChatLocations(projectId: string): void {
  const next = new Map(epochs());
  next.set(projectId, (next.get(projectId) ?? 0) + 1);
  setEpochs(next);
}

function parentPath(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? "" : path.slice(0, slash);
}

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

// An exact-file root displays the containing folder.
export function locationLedgerPath(location: SourceChatLocation, path: string): string {
  if (location.kind === "file") return path === "." ? parentPath(location.anchor) : location.anchor;
  return path === "." ? location.anchor : `${location.anchor}/${path}`;
}

/** The row under a location that names an outside path, or undefined when it is not covered. */
export function locationRowPath(location: SourceChatLocation, outsidePath: string): string | undefined {
  if (location.kind === "file") return outsidePath === location.anchor ? baseName(location.anchor) : undefined;
  if (outsidePath === location.anchor) return ".";
  return outsidePath.startsWith(`${location.anchor}/`) ? outsidePath.slice(location.anchor.length + 1) : undefined;
}

/** Source history's address for a tree row; project rows pass through. */
export function ledgerAddress(projectId: string, outsideRootId: string | undefined, rootId: string, path: string): { rootId: string; path: string } {
  const location = chatLocation(projectId, rootId);
  if (!location || !outsideRootId) return { rootId, path };
  return { rootId: outsideRootId, path: locationLedgerPath(location, path) };
}

/** The tree row for a source-history address; outside paths no location covers have none. */
export function treeAddress(projectId: string, outsideRootId: string | undefined, rootId: string, path: string): { rootId: string; path: string } | undefined {
  if (!outsideRootId || rootId !== outsideRootId) return { rootId, path };
  for (const location of chatLocations(projectId)) {
    const row = locationRowPath(location, path);
    if (row !== undefined) return { rootId: location.id, path: row };
  }
  return undefined;
}

export function resetChatLocationsForTests(): void {
  setLocationsByProject(new Map());
  setEpochs(new Map());
}
