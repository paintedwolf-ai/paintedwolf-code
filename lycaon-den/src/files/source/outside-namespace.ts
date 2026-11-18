import { createSignal } from "solid-js";

/** Source history pairs this ID with the host path minus its leading slash. */
const [outsideRoots, setOutsideRoots] = createSignal<ReadonlyMap<string, string>>(new Map());

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

export function outsideHostPath(path: string): string {
  return `/${path.replace(/^\/+/, "")}`;
}

export function outsideHostDirectory(path: string): string {
  const host = outsideHostPath(path);
  const slash = host.lastIndexOf("/");
  return slash <= 0 ? "/" : host.slice(0, slash);
}

export function resetOutsideRootsForTests(): void {
  setOutsideRoots(new Map());
}
