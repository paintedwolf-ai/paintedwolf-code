// Watermarks follow the store across navigation and stream replacement.
const watermarks = new WeakMap<object, Map<string, number>>();

/** The per-session revision watermarks for one store's event streams. */
export function sessionRevisionWatermarks(context: object): Map<string, number> {
  let revisions = watermarks.get(context);
  if (!revisions) {
    revisions = new Map();
    watermarks.set(context, revisions);
  }
  return revisions;
}

/** The newest session revision this store applied, if any. */
export function appliedSessionRevision(context: object, sessionId: string): number | undefined {
  return watermarks.get(context)?.get(sessionId);
}

/** Drops a store's watermarks when its backend connection generation changes. */
export function forgetSessionRevisions(context: object): void {
  watermarks.delete(context);
}
