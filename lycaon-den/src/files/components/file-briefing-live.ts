import type {
  FileBriefingEvent,
  FileBriefingResponse,
  SourceChange,
} from "../../api/types.ts";

export type LiveFileBriefing = Omit<FileBriefingResponse, "status"> & {
  status: FileBriefingResponse["status"] | "streaming";
  streamText?: string;
  stale: boolean;
};

type Listener = (
  projectId: string,
  rootId: string,
  path: string,
  briefing: LiveFileBriefing,
) => void;

const briefings = new Map<string, LiveFileBriefing>();
const targetByRequest = new Map<string, string>();
const MAX_LIVE_BRIEFINGS = 128;
const MAX_LIVE_BRIEFING_REQUESTS = 256;
const listeners = new Set<Listener>();
const resyncListeners = new Set<() => void>();
const statusOrder: Record<LiveFileBriefing["status"], number> = {
  pending: 0,
  streaming: 1,
  preview: 2,
  complete: 2,
  failed: 2,
};

function key(projectId: string, rootId: string, path: string, targetKey: string): string {
  return `${projectId.trim()}\0${rootId.trim()}\0${path.trim()}\0${targetKey.trim()}`;
}

function fileKey(projectId: string, rootId: string, path: string): string {
  return `${projectId.trim()}\0${rootId.trim()}\0${path.trim()}`;
}

function requestKey(projectId: string, requestIdentity: string): string {
  return `${projectId.trim()}\0${requestIdentity}`;
}

function mapRequest(request: string, entryKey: string): void {
  targetByRequest.delete(request);
  targetByRequest.set(request, entryKey);
  while (targetByRequest.size > MAX_LIVE_BRIEFING_REQUESTS) {
    const oldest = targetByRequest.keys().next().value;
    if (oldest === undefined) break;
    targetByRequest.delete(oldest);
  }
}

function notify(
  projectId: string,
  rootId: string,
  path: string,
  briefing: LiveFileBriefing,
): void {
  for (const listener of listeners) {
    listener(projectId, rootId, path, briefing);
  }
}

function storeBriefing(entryKey: string, briefing: LiveFileBriefing): void {
  briefings.delete(entryKey);
  briefings.set(entryKey, briefing);
  while (briefings.size > MAX_LIVE_BRIEFINGS) {
    const entries = [...briefings];
    const evicted =
      entries.find(([, candidate]) =>
        candidate.status !== "pending" && candidate.status !== "streaming"
      ) ?? entries[0];
    if (!evicted) break;
    briefings.delete(evicted[0]);
    for (const [request, mapped] of targetByRequest) {
      if (mapped === evicted[0]) targetByRequest.delete(request);
    }
  }
}

// Terminal payloads reuse streamed text when no explanation is present.
function retainedStreamText(
  prior: LiveFileBriefing | undefined,
  attemptId: string,
  sourceSHA256: string,
  sections: LiveFileBriefing["sections"] | undefined,
  fallbackText: string,
): string | undefined {
  if (
    !prior ||
    prior.attempt_id !== attemptId ||
    prior.source_sha256 !== sourceSHA256
  ) return undefined;
  if ((sections?.length ?? 0) > 0 || fallbackText.trim() !== "") return undefined;
  return prior.streamText;
}

function acceptsUpdate(
  prior: LiveFileBriefing | undefined,
  attemptId: string,
  sourceSHA256: string,
  status: LiveFileBriefing["status"],
  updatedAt: string,
  snapshot: boolean,
): boolean {
  if (!prior) return true;
  const priorTime = Date.parse(prior.updated_at);
  const nextTime = Date.parse(updatedAt);
  if (prior.attempt_id !== attemptId) {
    if (!snapshot && status !== "pending") return false;
    return nextTime > priorTime;
  }
  if (nextTime !== priorTime) return nextTime > priorTime;
  if (prior.source_sha256 !== sourceSHA256) return true;
  return statusOrder[status] >= statusOrder[prior.status];
}

export function getLiveFileBriefing(
  projectId: string,
  requestIdentity: string,
): LiveFileBriefing | null {
  const request = requestKey(projectId, requestIdentity);
  const entryKey = targetByRequest.get(request);
  if (!entryKey) return null;
  const briefing = briefings.get(entryKey);
  if (!briefing) return null;
  mapRequest(request, entryKey);
  storeBriefing(entryKey, briefing);
  return briefing;
}

export function receiveFileBriefingSnapshot(
  projectId: string,
  response: FileBriefingResponse,
  requestIdentity: string,
): LiveFileBriefing {
  const id = projectId.trim();
  const entryKey = key(id, response.root_id, response.path, response.target_key);
  mapRequest(requestKey(id, requestIdentity), entryKey);
  const prior = briefings.get(entryKey);
  if (
    prior &&
    !acceptsUpdate(
      prior,
      response.attempt_id,
      response.source_sha256,
      response.status,
      response.updated_at,
      true,
    )
  ) {
    return prior;
  }
  const next: LiveFileBriefing = {
    ...response,
    streamText: retainedStreamText(
      prior,
      response.attempt_id,
      response.source_sha256,
      response.sections,
      response.fallback_text,
    ),
    stale: false,
  };
  storeBriefing(entryKey, next);
  notify(id, response.root_id, response.path, next);
  return next;
}

export function applyFileBriefingEvent(event: FileBriefingEvent): void {
  const projectId = event.project_id.trim();
  const rootId = event.root_id.trim();
  const path = event.path.trim();
  if (!projectId || !rootId || !path) return;
  const entryKey = key(projectId, rootId, path, event.target_key);
  const prior = briefings.get(entryKey);
  if (!acceptsUpdate(
    prior,
    event.attempt_id,
    event.source_sha256,
    event.status,
    event.updated_at,
    false,
  )) return;
  const preview = event.preview ?? prior?.preview;
  if (!preview) return;
  const sameAttempt = prior?.attempt_id === event.attempt_id;
  const sameRevision = sameAttempt && prior?.source_sha256 === event.source_sha256;
  const streamed = sameRevision ? (prior.streamText ?? "") : "";
  const sections = event.sections ?? (sameRevision ? prior?.sections : undefined) ?? [];
  const fallbackText = event.fallback_text;
  const streamText =
    event.status === "streaming"
      ? streamed + (event.delta ?? "")
      : retainedStreamText(
          prior,
          event.attempt_id,
          event.source_sha256,
          event.sections ?? sections,
          fallbackText,
        );
  const next: LiveFileBriefing = {
    target_key: event.target_key,
    attempt_id: event.attempt_id,
    root_id: rootId,
    path,
    presentation: event.presentation,
    source_sha256: event.source_sha256,
    status: event.status,
    preview,
    locations:
      event.locations ?? (sameRevision ? prior?.locations : undefined) ?? [],
    sections,
    fallback_text: fallbackText,
    streamText,
    truncated: event.truncated,
    error: event.error,
    stale: sameRevision ? (prior?.stale ?? false) : false,
    updated_at: event.updated_at,
  };
  storeBriefing(entryKey, next);
  notify(projectId, rootId, path, next);
}

export function markFileBriefingSourceChanged(
  projectId: string,
  event: SourceChange,
): void {
  const id = projectId.trim();
  if (!id || !event.root_id) return;
  const paths = new Set(
    [event.path, event.from_path].filter((path): path is string => Boolean(path)),
  );
  const staleUpdates: Array<[string, string, LiveFileBriefing]> = [];
  for (const path of paths) {
    const prefix = `${fileKey(id, event.root_id, path)}\0`;
    for (const [entryKey, prior] of briefings) {
      if (!entryKey.startsWith(prefix)) continue;
      if (prior.presentation !== "current") continue;
      if (event.after_sha256 && event.after_sha256 === prior.source_sha256) continue;
      const next = { ...prior, stale: true };
      staleUpdates.push([entryKey, path, next]);
    }
  }
  for (const [entryKey, path, next] of staleUpdates) {
    storeBriefing(entryKey, next);
    notify(id, event.root_id, path, next);
  }
}

export function subscribeFileBriefings(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function subscribeFileBriefingResync(listener: () => void): () => void {
  resyncListeners.add(listener);
  return () => resyncListeners.delete(listener);
}

export function requestFileBriefingResync(): void {
  for (const listener of resyncListeners) listener();
}

export function clearFileBriefingLive(): void {
  briefings.clear();
  targetByRequest.clear();
}

export function resetFileBriefingLiveForTest(): void {
  clearFileBriefingLive();
  listeners.clear();
  resyncListeners.clear();
}
