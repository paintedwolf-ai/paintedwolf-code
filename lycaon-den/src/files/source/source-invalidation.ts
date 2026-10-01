export type SourceInvalidation = {
  projectId: string;
  /** Absent after an event continuity gap, when every project workspace is stale. */
  workspaceId?: string;
};

const listeners = new Set<(scope: SourceInvalidation) => void>();

export function subscribeSourceInvalidation(listener: (scope: SourceInvalidation) => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

/** Invalidates derived queries within the affected workspace. */
export function invalidateSourceQueries(scope: SourceInvalidation): void {
  for (const listener of [...listeners]) {
    if (listeners.has(listener)) listener(scope);
  }
}
