import { createSignal } from "solid-js";

const [selected, setSelected] = createSignal<ReadonlyMap<string, string>>(new Map());

export function selectedSecurityFolder(projectId: string | undefined): string | undefined {
  return projectId ? selected().get(projectId) : undefined;
}

export function selectSecurityFolder(projectId: string, rootId: string): void {
  setSelected((previous) => new Map(previous).set(projectId, rootId));
}

export function resetSecurityFoldersForTests(): void {
  setSelected(new Map());
}
