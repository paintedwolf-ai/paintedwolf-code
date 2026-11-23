import type { SessionEvent } from "./types.ts";

// Watermarks follow the store across navigation and stream replacement.
const watermarks = new WeakMap<object, Map<string, number>>();
type ModeFields = Pick<SessionEvent, "mode" | "started">;
type ModeRevisions = Partial<Record<keyof ModeFields, number>>;
const modeWatermarks = new WeakMap<object, Map<string, ModeRevisions>>();

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

export function sessionModeRevisionWatermarks(context: object): Map<string, ModeRevisions> {
  let revisions = modeWatermarks.get(context);
  if (!revisions) {
    revisions = new Map();
    modeWatermarks.set(context, revisions);
  }
  return revisions;
}

function acceptsModeRevision(previous: number | undefined, revision: number | undefined): boolean {
  return previous === undefined || (revision !== undefined && revision >= previous);
}

export function takeSessionModeFields(
  watermarks: Map<string, ModeRevisions>,
  event: SessionEvent,
  revision: number | undefined,
): ModeFields | undefined {
  const previous = watermarks.get(event.id);
  const mode = acceptsModeRevision(previous?.mode, revision) ? event.mode : undefined;
  const started = acceptsModeRevision(previous?.started, revision) ? event.started : undefined;
  if (mode === undefined && started === undefined) return undefined;
  watermarks.set(event.id, {
    mode: mode === undefined ? previous?.mode : revision,
    started: started === undefined ? previous?.started : revision,
  });
  return { mode, started };
}

/** Drops a store's watermarks when its backend connection generation changes. */
export function forgetSessionRevisions(context: object): void {
  watermarks.delete(context);
  modeWatermarks.delete(context);
}
