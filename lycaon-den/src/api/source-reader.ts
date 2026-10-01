import type { LycaonClient } from "./client.ts";
import type { SourceComparisonTarget } from "./http-capabilities/source-history.ts";
import { SourceComparisonSession, type ComparisonBoundary } from "./source-comparison-session.ts";
import type { SourceComparisonDetails, SourceComparisonIntent, SourceComparisonSearchPage, SourceComparisonSelector, SourceComparisonSummary, SourceComparisonView } from "./types.ts";

export type ComparisonReference = { view: SourceComparisonView; source: SourceComparisonSelector; session_id?: string };
export type ComparisonSnapshot = SourceComparisonDetails & { reference?: ComparisonReference };
export type SourceReaderAccess = {
  release?(): void;
  summary(): Promise<SourceComparisonSummary>;
  reference(): Promise<ComparisonReference>;
  presentation(intent?: SourceComparisonIntent, signal?: AbortSignal): Promise<SourceComparisonSession>;
  search(query: string, cursor: string | undefined, caseSensitive: boolean, signal?: AbortSignal): Promise<SourceComparisonSearchPage>;
  content(side: "before" | "after"): Promise<string>;
  selection(start: ComparisonBoundary, end: ComparisonBoundary): Promise<string>;
};

type Entry = { access: SourceReaderAccess; bytes: number; dispose(): void };
const readers = new WeakMap<LycaonClient, Map<string, Entry>>();
const MAX_READERS = 128;
const IDLE_MS = 5 * 60_000;
let requestSerial = 0;

/** Immutable content is shared; each mounted reader gets its own presentation intent. */
export function sourceReaderAccess(client: LycaonClient, projectId: string,
  request: SourceComparisonSelector | ComparisonReference, sessionId?: string, resolveSource?: () => Promise<SourceComparisonSelector>): SourceReaderAccess {
  let cache = readers.get(client);
  if (!cache) { cache = new Map(); readers.set(client, cache); }
  const reference = "view" in request ? request : undefined;
  const source = reference?.source ?? request as SourceComparisonSelector;
  const transient = source.kind === "current" || source.kind === "text" || source.kind === "scope" || source.kind === "commit" || source.kind === "retained" && source.comparison === "current";
  const key = `${projectId}\0${reference?.session_id ?? sessionId ?? ""}\0${reference?.view.id ?? (transient ? `snapshot:${++requestSerial}` : JSON.stringify(source))}`;
  const held = cache.get(key);
  if (held) { cache.delete(key); cache.set(key, held); return held.access; }
  let master: SourceComparisonSession | undefined;
  let lastView = reference?.view;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const dispose = () => { clearTimeout(timer); const previous = master; master = undefined; lastView = previous?.state() ?? lastView; if (previous) void previous.close().catch(() => {}); };
  const get = () => {
    clearTimeout(timer);
    timer = setTimeout(dispose, IDLE_MS);
    return master ??= new SourceComparisonSession(client, projectId, source, reference?.session_id ?? sessionId,
      { mode: "changes" }, lastView, undefined, resolveSource);
  };
  const access: SourceReaderAccess = {
    release: () => { dispose(); cache?.delete(key); },
    reference: async () => { const current = get(); await current.refresh(); return { view: await current.ready(), source, session_id: reference?.session_id ?? sessionId }; },
    async summary() {
      const state = await get().ready();
      if (!state.comparison?.summary) throw new Error("This comparison has no readable source.");
      return state.comparison.summary;
    },
    presentation: (intent, signal) => get().fork(intent, signal),
    search: (query, cursor, caseSensitive, signal) => get().search(query, cursor, caseSensitive, signal),
    content: side => get().text(side),
    selection: (start, end) => get().text(start.side, start, end),
  };
  const bytes = source.kind === "text" ? 2048 + 2 * ((source.before?.length ?? 0) + (source.after?.length ?? 0)) : 8192;
  cache.set(key, { access, bytes, dispose });
  let retained = [...cache.values()].reduce((sum, value) => sum + value.bytes, 0);
  while (cache.size > MAX_READERS || retained > 16 * 1024 * 1024) {
    const oldestEntry = cache.entries().next().value;
    if (!oldestEntry) break;
    const [oldest, entry] = oldestEntry;
    retained -= entry.bytes; entry.dispose(); cache.delete(oldest);
  }
  return access;
}

export function comparisonSelector(target: SourceComparisonTarget): SourceComparisonSelector {
  if ("effectId" in target) return { kind: "effect", effect_id: target.effectId };
  if ("versionId" in target) return { kind: "version", version_id: target.versionId };
  if ("blobOid" in target) return { kind: "blob", root_id: target.rootId, blob_oid: target.blobOid, before_blob_oid: target.beforeBlobOid, display_path: target.displayPath };
  if ("reviewedThroughOrdinal" in target) return { kind: "reviewed", file_id: target.fileId, reviewed_through_ordinal: target.reviewedThroughOrdinal };
  if ("turn" in target) return { kind: "turn", file_id: target.fileId, session_id: target.sessionId, turn: target.turn, mark_user_edits: target.markUserEdits };
  if ("path" in target) return { kind: "commit", root_id: target.rootId, path: target.path, expected_head: target.expectedHead };
  return { kind: "scope", file_id: target.fileId, baseline: target.baseline, mark_user_edits: target.markUserEdits, presentation_after_ordinal: target.presentationAfterOrdinal };
}

/** Transfers a prepared handle to the bounded reader cache without retaining a presenter. */
export async function loadComparisonSnapshot(client: LycaonClient, projectId: string, source: SourceComparisonSelector,
  sessionId?: string, signal?: AbortSignal): Promise<ComparisonSnapshot> {
  const session = new SourceComparisonSession(client, projectId, source, sessionId);
  try {
    const view = await session.ready(signal);
    signal?.throwIfAborted();
    if (!view.comparison) throw new Error("The comparison details are missing.");
    const result: ComparisonSnapshot = { ...view.comparison, reference: { view, source, session_id: sessionId } };
    sourceReaderAccess(client, projectId, result.reference!);
    session.forget();
    return result;
  } catch (error) { await session.close(); throw error; }
}

/** Current-file comparison retains the selected historical endpoint across handle renewal. */
export function currentComparisonAccess(original: SourceReaderAccess, client: LycaonClient, projectId: string,
  rootId: string, path: string, sessionId?: string): SourceReaderAccess {
  let resolvedSessionId = sessionId;
  const source = async (): Promise<SourceComparisonSelector> => {
    const reference = await original.reference();
    resolvedSessionId ??= reference.session_id;
    return { kind: "retained", view_id: reference.view.id, comparison: "current", root_id: rootId, path };
  };
  let opening: Promise<SourceReaderAccess> | undefined;
  const get = () => opening ??= source().then(selector => sourceReaderAccess(client, projectId, selector, resolvedSessionId, source))
    .catch(error => { opening = undefined; throw error; });
  return {
    summary: async () => (await get()).summary(), reference: async () => (await get()).reference(),
    presentation: async (intent, signal) => (await get()).presentation(intent, signal),
    search: async (query, cursor, caseSensitive, signal) => (await get()).search(query, cursor, caseSensitive, signal),
    content: async side => (await get()).content(side),
    selection: async (start, end) => (await get()).selection(start, end),
  };
}
