import { Show, createSignal, onCleanup } from "solid-js";
import type { BackgroundProcessTerminationReason } from "../../api/types.ts";
import {
  getBackgroundProcessSnapshot,
  getRecordedBackgroundTerminationReason,
  hasHydratedBackgroundProcesses,
  subscribeBackgroundProcessStore,
} from "../../chat/tool/background-process-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { formatBackgroundTerminationReason } from "../../project/background-termination-reason.ts";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  sessionId: string;
  processId: string;
  initialRunning?: boolean;
  terminationReason?: BackgroundProcessTerminationReason;
  /** The port the host attributed to the process, if any. */
  port?: number;
};

/** A background process's live state on its action card, with Stop while it runs. */
export function ActionProcess(props: Props) {
  const [revision, setRevision] = createSignal(0);
  const refresh = () => setRevision((value) => value + 1);
  onCleanup(subscribeBackgroundProcessStore(refresh, () => ({ sessionId: props.sessionId, processId: props.processId })));
  const snapshot = () => {
    revision();
    return getBackgroundProcessSnapshot(props.sessionId, props.processId);
  };
  const hydrated = () => {
    revision();
    return hasHydratedBackgroundProcesses(props.sessionId);
  };
  // Before the session's processes load, the call's own record says whether it ran on.
  const running = () => snapshot()?.running ?? (hydrated() ? false : props.initialRunning ?? false);
  const reason = () => {
    revision();
    return snapshot()?.terminationReason ?? props.terminationReason ?? getRecordedBackgroundTerminationReason(props.sessionId, props.processId);
  };
  const unavailable = () => !snapshot() && hydrated() && !reason();
  const state = () => {
    if (!snapshot() && reason()) return "historical";
    if (running()) return "running";
    if (snapshot()) return "ended";
    return hydrated() ? "unavailable" : "pending";
  };
  const status = () => {
    switch (state()) {
      case "running": return props.port !== undefined ? `Running · listening on ${props.port}` : "Running";
      case "ended": return formatBackgroundTerminationReason(reason() ?? "exited");
      case "historical": return formatBackgroundTerminationReason(reason()!);
      case "unavailable": return "No longer available";
      default: return "Checking…";
    }
  };

  const [stopping, setStopping] = createSignal(false);
  const [stopError, setStopError] = createSignal<string | null>(null);
  const stop = async () => {
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
    <div class="den-action-card__process" data-testid="action-process" data-state={state()}>
      <dl class="den-action-card__facts">
        <div data-kind="process">
          <dt>Process</dt>
          <dd data-testid={state() === "historical" ? "background-process-historical-reason" : `background-process-${state()}`}>{status()}</dd>
        </div>
        <Show when={!running() && snapshot()?.exitCode != null}>
          <div data-kind="exit">
            <dt>Exit code</dt>
            <dd data-testid="background-process-exit-code">{snapshot()?.exitCode}</dd>
          </div>
        </Show>
      </dl>
      <Show when={running()}>
        <DenButton variant="secondary" compact disabled={stopping()} data-testid="background-process-stop" onClick={() => void stop()}>
          {stopping() ? "Stopping…" : "Stop"}
        </DenButton>
      </Show>
      <Show when={unavailable()}>
        <p class="den-action-card__notice" data-testid="background-process-unavailable-guidance">
          Background commands end when stopped or when Painted Wolf Code quits.
        </p>
      </Show>
      <Show when={stopError()}>
        {(message) => <p class="den-action-card__notice">{message()}</p>}
      </Show>
    </div>
  );
}
