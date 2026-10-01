import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem } from "../../api/types.ts";
import {
  LIVE_TOOL_RECORDING_MIN_DURATION_MS,
  type LiveToolRecordingArtifact,
} from "./live-tool-recording.ts";
import { isVisualVideoMime } from "./visual-artifact-model.ts";

export type InvocationRecording = LiveToolRecordingArtifact & {
  assistantMessageId: string;
  toolCallId: string;
};

type ArtifactClient = Pick<LycaonClient, "listSessionArtifacts">;

const bySession = new Map<string, Map<string, InvocationRecording>>();
const loaded = new Set<string>();
const loading = new Map<string, Promise<void>>();
const listeners = new Set<() => void>();

/** Bounds bySession to the most recently touched sessions. Map iteration
 * order doubles as recency order — touchSession bumps a key to the end. */
const INVOCATION_RECORDING_SESSION_CAP = 64;

function touchSession(sessionId: string): void {
  const records = bySession.get(sessionId);
  if (!records) return;
  bySession.delete(sessionId);
  bySession.set(sessionId, records);
}

function evictSession(sessionId: string): void {
  bySession.delete(sessionId);
  loaded.delete(sessionId);
  loading.delete(sessionId);
}

function trimSessions(): void {
  while (bySession.size > INVOCATION_RECORDING_SESSION_CAP) {
    const oldest = bySession.keys().next().value;
    if (oldest === undefined) return;
    evictSession(oldest);
  }
}

function key(assistantMessageId: string, toolCallId: string): string {
  return `${assistantMessageId.trim()}\u0000${toolCallId.trim()}`;
}

function notify(): void {
  for (const listener of listeners) listener();
}

function recordingFromArtifact(
  sessionId: string,
  artifact: ArtifactListItem,
): InvocationRecording | null {
  const assistantMessageId = artifact.origin_message_id?.trim() ?? "";
  const toolCallId = artifact.tool_call_id?.trim() ?? "";
  if (
    !assistantMessageId ||
    !toolCallId ||
    !artifact.id.trim() ||
    artifact.session_id?.trim() !== sessionId ||
    !artifact.page_id?.trim() ||
    !isVisualVideoMime(artifact.mime) ||
    (artifact.duration_ms != null &&
      artifact.duration_ms < LIVE_TOOL_RECORDING_MIN_DURATION_MS)
  ) {
    return null;
  }
  return {
    sessionId,
    artifactId: artifact.id,
    assistantMessageId,
    toolCallId,
  };
}

export function applyInvocationRecording(recording: InvocationRecording): void {
  const sessionId = recording.sessionId.trim();
  const assistantMessageId = recording.assistantMessageId.trim();
  const toolCallId = recording.toolCallId.trim();
  if (!sessionId || !assistantMessageId || !toolCallId) return;
  let records = bySession.get(sessionId);
  if (!records) {
    records = new Map();
    bySession.set(sessionId, records);
  }
  records.set(key(assistantMessageId, toolCallId), {
    ...recording,
    sessionId,
    assistantMessageId,
    toolCallId,
  });
  touchSession(sessionId);
  trimSessions();
  notify();
}

export function getRecordingForInvocation(
  sessionId: string,
  assistantMessageId: string,
  toolCallId: string,
): InvocationRecording | undefined {
  return bySession
    .get(sessionId.trim())
    ?.get(key(assistantMessageId, toolCallId));
}

export function subscribeInvocationRecordingStore(
  listener: () => void,
): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Hydrate host-managed recordings once per session. */
export function ensureInvocationRecordings(
  client: ArtifactClient,
  sessionId: string,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid || loaded.has(sid)) return Promise.resolve();
  const pending = loading.get(sid);
  if (pending) return pending;
  const request = client
    .listSessionArtifacts(sid)
    .then((response) => {
      if (loading.get(sid) !== request) return;
      const records = new Map<string, InvocationRecording>();
      for (const artifact of response.artifacts ?? []) {
        const recording = recordingFromArtifact(sid, artifact);
        if (!recording) continue;
        records.set(
          key(recording.assistantMessageId, recording.toolCallId),
          recording,
        );
      }
      bySession.set(sid, records);
      loaded.add(sid);
      touchSession(sid);
      trimSessions();
      notify();
    })
    .finally(() => {
      if (loading.get(sid) === request) loading.delete(sid);
    });
  loading.set(sid, request);
  return request;
}

export function invalidateInvocationRecordings(sessionId: string): void {
  const sid = sessionId.trim();
  if (!sid) return;
  loaded.delete(sid);
  loading.delete(sid);
  bySession.delete(sid);
  notify();
}

/** Forget client recording state for a retired session. */
export function forgetInvocationRecordingSession(sessionId: string): void {
  const sid = sessionId.trim();
  if (!sid) return;
  evictSession(sid);
  notify();
}

export function resetInvocationRecordingStoreForTests(): void {
  bySession.clear();
  loaded.clear();
  loading.clear();
  listeners.clear();
}
