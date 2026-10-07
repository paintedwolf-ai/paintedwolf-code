import { createSignal } from "solid-js";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { nativeUpdateService, type NativeUpdateState, type UpdateService } from "./update-service.ts";
import { updateError, type UpdateError } from "./update-error.ts";

export function createUpdateState(service: UpdateService) {
  const [state, setState] = createSignal<NativeUpdateState | null>(null);
  const [error, setError] = createSignal<UpdateError | null>(null);
  const [running, setRunning] = createSignal(false);
  const [confirming, setConfirming] = createSignal(false);
  // A pending command or an open restart confirmation both hold the controls.
  const busy = () => running() || confirming();
  const retired = new Set<string>();
  let users = 0;
  let epoch = 0;
  let unlisten = () => {};

  const accept = (next: NativeUpdateState) => {
    const previous = state();
    if (retired.has(next.service_instance_id)) return;
    if (previous?.service_instance_id === next.service_instance_id && next.revision < previous.revision) return;
    if (previous && previous.service_instance_id !== next.service_instance_id) retired.add(previous.service_instance_id);
    setState(next);
  };

  const subscribe = async (generation: number) => {
    try {
      const stop = await service.subscribe((next) => {
        if (epoch === generation) accept(next);
      });
      if (epoch !== generation) {
        stop();
        return;
      }
      unlisten = stop;
    } catch (failure) {
      if (epoch === generation) setError(updateError(failure));
    }
    if (epoch !== generation) return;
    try {
      const next = await service.getState();
      if (epoch === generation) accept(next);
    } catch (failure) {
      if (epoch === generation) setError(updateError(failure));
    }
  };

  const mount = () => {
    users += 1;
    if (users === 1) void subscribe(++epoch);
    let disposed = false;
    return () => {
      if (disposed) return;
      disposed = true;
      if (--users === 0) {
        epoch += 1;
        unlisten();
        unlisten = () => {};
      }
    };
  };

  const run = async (operation: () => Promise<NativeUpdateState | void>) => {
    if (running()) return;
    setRunning(true);
    setError(null);
    try {
      const next = await operation();
      if (next) accept(next);
    } catch (failure) {
      setError(updateError(failure));
      try {
        accept(await service.getState());
      } catch {
        // Native events retain the last known state.
      }
    } finally {
      setRunning(false);
    }
  };

  const restart = async () => {
    const current = state();
    const id = current?.staged_release_id;
    if (!id || !current.capabilities.can_restart_to_update || busy()) return;
    setConfirming(true);
    let confirmed = false;
    try {
      confirmed = await confirmDestructive({
        title: "Restart to update?",
        message: "Restart to update Painted Wolf Code? Drafts will be preserved. Running work and terminals will be interrupted.",
        okLabel: "Restart to update",
        cancelLabel: "Keep working",
        destructive: false,
      });
    } finally {
      setConfirming(false);
    }
    if (confirmed && state()?.staged_release_id === id) await run(() => service.restart(id));
  };

  return { state, error, busy, mount, run, restart, service };
}

export const nativeUpdateState = createUpdateState(nativeUpdateService);
