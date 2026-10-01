type ProjectFilesRevealRequest = {
  projectId: string;
  rootId: string;
  path: string;
  isDir: boolean;
};

let pending: { request: ProjectFilesRevealRequest; current: () => boolean } | null = null;
const listeners = new Set<() => void>();

export function setProjectFilesRevealRequest(
  req: ProjectFilesRevealRequest,
): void {
  pending = { request: { ...req }, current: beginSourceNavigation().current };
  for (const listener of listeners) listener();
}

export function takeProjectFilesRevealRequest(
  projectId: string,
): ProjectFilesRevealRequest | null {
  if (pending?.request.projectId !== projectId) return null;
  const req = pending.current() ? pending.request : null;
  pending = null;
  return req;
}

export function onProjectFilesRevealRequest(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
