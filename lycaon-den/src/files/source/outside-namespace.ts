import { createSignal } from "solid-js";

/**
 * Each project's namespace for files beyond its folders. Source history
 * addresses those files by this root id and their host path without its
 * leading slash.
 */
const [outsideRoots, setOutsideRoots] = createSignal<ReadonlyMap<string, string>>(new Map());

/** The label every surface shows for the outside namespace. */
export const OUTSIDE_ROOT_LABEL = "Outside project";

export function recordOutsideRoot(projectId: string, rootId: string): void {
  if (!rootId || outsideRoots().get(projectId) === rootId) return;
  const next = new Map(outsideRoots());
  next.set(projectId, rootId);
  setOutsideRoots(next);
}

export function outsideRootId(projectId: string): string | undefined {
  return outsideRoots().get(projectId);
}

export function isOutsideRoot(projectId: string, rootId: string | undefined): boolean {
  return !!rootId && outsideRoots().get(projectId) === rootId;
}

/** Whether rootId is any known project's outside namespace. */
export function isKnownOutsideRoot(rootId: string): boolean {
  for (const id of outsideRoots().values()) if (id === rootId) return true;
  return false;
}

/** The host path of an outside-namespace path. */
export function outsideHostPath(path: string): string {
  return `/${path.replace(/^\/+/, "")}`;
}

/** The host folder holding an outside-namespace path. */
export function outsideHostDirectory(path: string): string {
  const host = outsideHostPath(path);
  const slash = host.lastIndexOf("/");
  return slash <= 0 ? "/" : host.slice(0, slash);
}

export function resetOutsideRootsForTests(): void {
  setOutsideRoots(new Map());
}
