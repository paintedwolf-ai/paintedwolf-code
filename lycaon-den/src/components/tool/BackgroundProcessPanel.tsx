import { createSignal, onCleanup, Show } from "solid-js";
import type { BackgroundProcessTerminationReason } from "../../api/types.ts";
import {
  getBackgroundProcessSnapshot,
  getRecordedBackgroundTerminationReason,
  hasHydratedBackgroundProcesses,
  subscribeBackgroundProcessStore,
} from "../../chat/tool/background-process-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { formatBackgroundTerminationReason } from "../../project/background-termination-reason.ts";
import { ToolContentLink } from "./ToolContentLink.tsx";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  sessionId: string;
  processId: string;
  initialRunning?: boolean;
  terminationReason?: BackgroundProcessTerminationReason;
};

export function BackgroundProcessPanel(props: Props) {
  const [revision, setRevision] = createSignal(0);
  const snapshot = () => {
    revision();
    return getBackgroundProcessSnapshot(props.sessionId, props.processId);
  };
  const [stopping, setStopping] = createSignal(false);
  const [stopError, setStopError] = createSignal<string | null>(null);

  const refresh = () => {
    setRevision((value) => value + 1);
  };

  onCleanup(subscribeBackgroundProcessStore(refresh, () => ({ sessionId: props.sessionId, processId: props.processId })));

  const running = () =>
    snapshot()?.running ??
    (hasHydratedBackgroundProcesses(props.sessionId)
      ? false
      : (props.initialRunning ?? false));

  const recordedReason = () => {
    revision();
    return (
      snapshot()?.terminationReason ??
      props.terminationReason ??
      getRecordedBackgroundTerminationReason(props.sessionId, props.processId)
    );
  };

  const isUnavailableHistorical = () =>
    !snapshot() &&
    hasHydratedBackgroundProcesses(props.sessionId) &&
    !recordedReason();

  const status = () => {
    if (snapshot()) {
      return running()
        ? "Running"
        : formatBackgroundTerminationReason(recordedReason() ?? "exited");
    }
    if (hasHydratedBackgroundProcesses(props.sessionId)) {
      const reason = recordedReason();
      return reason ? formatBackgroundTerminationReason(reason) : "No longer available";
    }
    return running() ? "Running" : "Checking…";
  };

  const onStop = async () => {
    const client = getLycaonClient();
    if (!client) {
      setStopError("Not connected");
      return;
    }
    setStopping(true);
    setStopError(null);
    try {
      await client.stopBackgroundProcess(props.sessionId, props.processId);
      refresh();
    } catch (err) {
      setStopError(err instanceof Error ? err.message : String(err));
    } finally {
      setStopping(false);
    }
  };

  return (
    <div class="den-tool-part-card-section">
      <div class="den-tool-part-card-facts">
        <div>
          <dt>Status</dt>
          <dd
            data-testid={
              !snapshot() && Boolean(recordedReason())
                ? "background-process-historical-reason"
                : undefined
            }
          >
            {status()}
          </dd>
        </div>
        <Show when={!running() && snapshot()?.exitCode != null}>
          <div>
            <dt>Exit code</dt>
            <dd>{snapshot()?.exitCode}</dd>
          </div>
        </Show>
        <div class="den-tool-part-card-facts--wide">
          <dt>Handle</dt>
          <dd>{props.processId}</dd>
        </div>
        <Show when={running()}>
          <div>
            <DenButton
              variant="secondary"
              compact
              disabled={stopping()}
              onClick={() => void onStop()}
            >
              {stopping() ? "Stopping…" : "Stop"}
            </DenButton>
          </div>
        </Show>
      </div>
      <Show when={isUnavailableHistorical()}>
        <p
          class="den-tool-part-card-note"
          data-testid="background-process-unavailable-guidance"
        >
          Background commands end when stopped or when Painted Wolf Code quits.
        </p>
      </Show>
      <Show when={stopError()}>
        <p class="den-tool-part-card-note">{stopError()}</p>
      </Show>
      <Show when={snapshot()?.truncated}>
        <p class="den-tool-part-card-note">Output truncated — use command_output for full buffer.</p>
      </Show>
      <ToolContentLink pane="process" label="Live output" content={{ kind: "process", handle: props.processId }} />
    </div>
  );
}
