import { nativeUpdateState } from "../settings/system/update-state.ts";
import { RecoveryUpdates } from "./update/RecoveryUpdates.tsx";
import { Show, createSignal } from "solid-js";
import type { JSX } from "solid-js";
import {
  engineStartupPhaseLabel,
  engineStartupState,
} from "../platform/connection/engine-startup.ts";
import { cancelBackendStart } from "../platform/connection/backend.ts";
import { tauriDragRegionProps } from "../platform/runtime.ts";
import { saveStartupDiagnosticsBundle } from "../settings/system/diagnostics-export.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { WindowHost } from "./shell/WindowHost.tsx";

function durationLabel(milliseconds: number): string {
  const seconds = milliseconds / 1_000;
  if (seconds < 10) return `${seconds.toFixed(1)} seconds elapsed`;
  return `${Math.round(seconds)} seconds elapsed`;
}

export function EngineStartupStage(): JSX.Element {
  const [stopping, setStopping] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [status, setStatus] = createSignal<string>();

  const progress = () => {
    const state = engineStartupState();
    return state.status === "idle" ? undefined : state;
  };

  const title = () => {
    const state = progress();
    if (state?.status === "stalled") return "Startup needs attention";
    return "Starting Painted Wolf Code";
  };

  const message = () => {
    const state = progress();
    if (state?.status === "stalled") {
      return "The engine is still running, but it stopped reporting progress. Waiting will continue until you choose to stop it.";
    }
    return "The local engine is making progress. You can leave this window open.";
  };

  const canControl = () => {
    const state = progress();
    return state?.status === "stalled" || (state?.elapsed_ms ?? 0) >= 30_000;
  };

  const stopStarting = async () => {
    if (stopping()) return;
    setStopping(true);
    setStatus(undefined);
    try {
      const requested = await cancelBackendStart();
      if (!requested) setStatus("Startup already finished.");
    } catch {
      setStatus("Could not stop startup. Waiting will continue.");
      setStopping(false);
    }
  };

  const saveBundle = async () => {
    if (saving()) return;
    setSaving(true);
    setStatus(undefined);
    try {
      const result = await saveStartupDiagnosticsBundle();
      if (result.kind === "saved") {
        setStatus(`Saved to ${result.path}.`);
      } else if (result.kind === "failed") {
        setStatus(result.detail);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <WindowHost testId="engine-startup-host">
      <Show when={nativeUpdateState.state()?.startup_pending}>
        <div class="den-engine-startup" data-testid="update-startup" {...tauriDragRegionProps({ deep: true })}>
          <div class="den-engine-startup__pulse" aria-hidden="true" />
          <p class="den-engine-startup__title">Preparing Painted Wolf Code</p>
          <p class="den-engine-startup__message" role="status">Checking the prepared update before starting the engine.</p>
        </div>
      </Show>
      <Show when={!nativeUpdateState.state()?.startup_pending && progress()}>
        {(state) => (
          <div
            class="den-engine-startup"
            data-status={state().status}
            data-phase={state().phase}
            data-testid="engine-startup"
            // The card sits above the host drag layer; buttons still click.
            {...tauriDragRegionProps({ deep: true })}
          >
            <div class="den-engine-startup__pulse" aria-hidden="true" />
            <p class="den-engine-startup__title">{title()}</p>
            <p class="den-engine-startup__message">{message()}</p>
            <p
              class="den-engine-startup__phase"
              role="status"
              aria-live="polite"
              aria-atomic="true"
              data-testid="engine-startup-phase"
            >
              {engineStartupPhaseLabel(state().phase)}
            </p>
            <p class="den-engine-startup__elapsed" aria-live="off">
              {durationLabel(state().elapsed_ms)}
              <Show when={state().status === "stalled" && state().silence_ms > 0}>
                {` · ${Math.round(state().silence_ms / 1_000)} seconds since the last update`}
              </Show>
            </p>
            <Show when={canControl()}>
              <div class="den-engine-startup__actions">
                <DenButton
                  variant="primary"
                  data-testid="engine-startup-stop"
                  disabled={stopping()}
                  onClick={() => void stopStarting()}
                >
                  {stopping() ? "Stopping…" : "Stop starting"}
                </DenButton>
                <DenButton
                  variant="secondary"
                  data-testid="engine-startup-save"
                  disabled={saving()}
                  onClick={() => void saveBundle()}
                >
                  {saving() ? "Saving…" : "Save report bundle…"}
                </DenButton>
              </div>
            </Show>
            <Show when={canControl()}><RecoveryUpdates /></Show>
            <Show when={status()}>
              <p class="den-engine-startup__status" data-testid="engine-startup-status">
                {status()}
              </p>
            </Show>
          </div>
        )}
      </Show>
    </WindowHost>
  );
}
