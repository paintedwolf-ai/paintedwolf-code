import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import {
  ensureInvocationRecordings,
  getRecordingForInvocation,
  subscribeInvocationRecordingStore,
} from "../../chat/visual/invocation-recording-store.ts";
import {
  getLatestPreviewForHolder,
  getPreviewForInvocation,
  subscribeLivePreviewStore,
} from "../../chat/visual/preview-store.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { LivePreviewPane } from "../preview/LivePreviewPane.tsx";
import { LiveToolRecordingPlayer } from "./LiveToolRecordingPlayer.tsx";

type Props = {
  client?: LycaonClient | null;
  sessionId?: string | null;
  assistantMessageId: string;
  toolCallId: string;
  /** Parent task rows mirror the newest live invocation from this worker. */
  mirrorHolderSessionId?: string;
  layout: TranscriptLayout;
};

/** Media for one ordered tool invocation. */
export function InvocationRenderingSlot(props: Props) {
  const [previewTick, setPreviewTick] = createSignal(0);
  const [recordingTick, setRecordingTick] = createSignal(0);

  const stopPreview = subscribeLivePreviewStore(() =>
    setPreviewTick((value) => value + 1),
  );
  const stopRecording = subscribeInvocationRecordingStore(() =>
    setRecordingTick((value) => value + 1),
  );
  onCleanup(() => {
    stopPreview();
    stopRecording();
  });

  const sessionId = () => props.sessionId?.trim() ?? "";
  const mirrorHolderSessionId = () => props.mirrorHolderSessionId?.trim() ?? "";
  const snapshot = createMemo(() => {
    previewTick();
    const sid = sessionId();
    if (!sid) return undefined;
    const holder = mirrorHolderSessionId();
    return holder
      ? getLatestPreviewForHolder(sid, holder)
      : getPreviewForInvocation(
          sid,
          props.assistantMessageId,
          props.toolCallId,
        );
  });

  createEffect(() => {
    recordingTick();
    const client = props.client;
    const sid = sessionId();
    if (!client || !sid || mirrorHolderSessionId()) return;
    void ensureInvocationRecordings(client, sid).catch(() => undefined);
  });

  const recording = createMemo(() => {
    recordingTick();
    const sid = sessionId();
    if (!sid || mirrorHolderSessionId()) return undefined;
    return getRecordingForInvocation(
      sid,
      props.assistantMessageId,
      props.toolCallId,
    );
  });
  const media = () => snapshot()?.jpegB64 || recording();

  const content = () => (
    <Show when={props.client} keyed>
      {(client) => (
        <section
          class="den-transcript-media-run"
          data-testid="invocation-rendering-slot"
          data-assistant-message-id={props.assistantMessageId}
          data-tool-call-id={props.toolCallId}
        >
          <Show when={snapshot()?.jpegB64}>
            <LivePreviewPane
              sessionId={sessionId()}
              client={client}
              snapshot={snapshot}
            />
          </Show>
          <Show when={recording()} keyed>
            {(item) => (
              <LiveToolRecordingPlayer
                client={client}
                sessionId={item.sessionId}
                artifactId={item.artifactId}
              />
            )}
          </Show>
        </section>
      )}
    </Show>
  );

  return (
    <Show when={media()}>
      <Show when={props.layout === "worker"} fallback={content()}>
        <li class="den-worker-msg--assistant den-worker-msg">
          <div class="den-worker-part--visual den-worker-part">{content()}</div>
        </li>
      </Show>
    </Show>
  );
}
