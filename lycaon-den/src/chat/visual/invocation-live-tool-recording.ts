import { createEffect, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import {
  getLivePreviewsForSession,
  subscribeLivePreviewStore,
} from "./preview-store.ts";
import {
  LiveToolRecordingEpisodeEngine,
  startLiveToolRecording,
} from "./live-tool-recording.ts";
import { applyInvocationRecording } from "./invocation-recording-store.ts";

type UploadClient = Pick<LycaonClient, "createSessionArtifact">;

/** Records watched pages for their invocations. */
export function createInvocationLiveToolRecording(opts: {
  sessionId: () => string;
  getClient: () => UploadClient | null;
  isActive?: () => boolean;
}): void {
  const [previewTick, setPreviewTick] = createSignal(0);
  let recordingSessionId = "";
  const recordingEpisodes = new Map<string, LiveToolRecordingEpisodeEngine>();

  const disposeEpisodes = () => {
    for (const episode of recordingEpisodes.values()) episode.dispose();
    recordingEpisodes.clear();
  };

  const newEpisode = () =>
    new LiveToolRecordingEpisodeEngine((episode) =>
      startLiveToolRecording(async (completed) => {
        const client = opts.getClient();
        if (!client) return;
        const uploadSessionId = episode.sessionId.trim();
        if (!uploadSessionId) return;
        try {
          const artifact = await client.createSessionArtifact(
            uploadSessionId,
            completed.blob,
            {
              pageId: episode.pageId,
              assistantMessageId: episode.assistantMessageId,
              toolCallId: episode.toolCallId,
              // Stable across upload retries.
              operationId: crypto.randomUUID(),
              recordedAt: completed.recordedAt,
              durationMs: completed.durationMs,
            },
          );
          applyInvocationRecording({
            sessionId: uploadSessionId,
            artifactId: artifact.id,
            assistantMessageId: episode.assistantMessageId,
            toolCallId: episode.toolCallId,
          });
        } catch {
          // Recording failures leave tool execution unchanged.
        }
      }),
    );

  createEffect(() => {
    const unsub = subscribeLivePreviewStore(() =>
      setPreviewTick((n) => n + 1),
    );
    onCleanup(unsub);
  });

  createEffect(() => {
    previewTick();
    const sessionId = opts.sessionId().trim();
    const active = opts.isActive?.() ?? true;
    if (!sessionId || !active) {
      disposeEpisodes();
      recordingSessionId = "";
      return;
    }
    if (recordingSessionId !== sessionId) {
      disposeEpisodes();
      recordingSessionId = sessionId;
    }
    const seen = new Set<string>();
    for (const snapshot of getLivePreviewsForSession(sessionId)) {
      const key = `${snapshot.holderSessionId}\u0000${snapshot.pageId}`;
      seen.add(key);
      let episode = recordingEpisodes.get(key);
      if (!episode) {
        episode = newEpisode();
        recordingEpisodes.set(key, episode);
      }
      episode.handleSnapshot(snapshot);
    }
    for (const [key, episode] of recordingEpisodes) {
      if (seen.has(key)) continue;
      episode.dispose();
      recordingEpisodes.delete(key);
    }
  });

  onCleanup(() => {
    disposeEpisodes();
    recordingSessionId = "";
  });
}
