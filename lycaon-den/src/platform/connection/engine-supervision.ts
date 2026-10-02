import { invoke } from "@tauri-apps/api/core";
import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

/** Mirrors `EngineExit` in `src-tauri/src/sidecar/supervisor.rs`. */
export type EngineExit = {
  code?: number;
  signal?: number;
  /** The operating system's account, such as "killed by signal 9 (SIGKILL)". */
  description: string;
};

/** Mirrors `EngineState` in `src-tauri/src/sidecar/supervisor.rs`. */
export type EngineState =
  | { state: "idle" }
  | { state: "running"; generation: number }
  | { state: "restarting"; exit: EngineExit; attempt: number }
  | { state: "stopped"; exit: EngineExit; failure?: string };

export const ENGINE_STATE_EVENT = "engine-state";

const [engineState, setEngineState] = createSignal<EngineState>({ state: "idle" });

/** The shell's engine as this window last heard it. */
export { engineState };

/** The shell knows its engine is not serving: restarting it, or stopped retrying. */
export function engineDown(state: EngineState = engineState()): boolean {
  return state.state === "restarting" || state.state === "stopped";
}

function validExit(value: unknown): value is EngineExit {
  if (!value || typeof value !== "object") return false;
  const exit = value as Partial<EngineExit>;
  return (
    typeof exit.description === "string" &&
    (exit.code === undefined || Number.isInteger(exit.code)) &&
    (exit.signal === undefined || Number.isInteger(exit.signal))
  );
}

export function parseEngineState(value: unknown): EngineState | null {
  if (!value || typeof value !== "object") return null;
  const candidate = value as Record<string, unknown>;
  switch (candidate.state) {
    case "idle":
      return { state: "idle" };
    case "running":
      return Number.isInteger(candidate.generation) && (candidate.generation as number) > 0
        ? { state: "running", generation: candidate.generation as number }
        : null;
    case "restarting":
      return validExit(candidate.exit) && Number.isInteger(candidate.attempt)
        ? { state: "restarting", exit: candidate.exit, attempt: candidate.attempt as number }
        : null;
    case "stopped":
      if (!validExit(candidate.exit)) return null;
      if (candidate.failure !== undefined && typeof candidate.failure !== "string") return null;
      return candidate.failure === undefined
        ? { state: "stopped", exit: candidate.exit }
        : { state: "stopped", exit: candidate.exit, failure: candidate.failure };
    default:
      return null;
  }
}

/**
 * Follows the shell's engine for this window: every published change, then
 * the current state for a window that opened after the last one.
 */
export async function watchEngineState(onChange: (state: EngineState) => void): Promise<() => void> {
  if (!isTauriRuntime()) return () => {};
  const apply = (value: unknown) => {
    const next = parseEngineState(value);
    if (!next) return;
    setEngineState(next);
    onChange(next);
  };
  let heard = false;
  const stop = await listenHostEvent<unknown>(ENGINE_STATE_EVENT, ({ payload }) => {
    heard = true;
    apply(payload);
  });
  try {
    const current = await invoke<unknown>("engine_state");
    // A change published while the read was in flight is newer than it.
    if (!heard) apply(current);
  } catch {
    /* A shell without supervision reports nothing; the window keeps its connection. */
  }
  return stop;
}

/** Replaces the observed state; tests only. */
export function setEngineStateForTest(state: EngineState): void {
  setEngineState(state);
}
