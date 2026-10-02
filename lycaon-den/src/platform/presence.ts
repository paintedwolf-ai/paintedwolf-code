import type { CheckpointResponse, ManagedSecretRevealResponse } from "../api/types.ts";
import { isTauriRuntime } from "./runtime.ts";

/** A structured refusal from native presence verification or the engine. */
export class PresenceError extends Error {
  readonly code: string;
  readonly title?: string;
  readonly suggestedAction?: string;
  readonly actions?: readonly string[];
  readonly scope?: string;
  readonly resolution?: string;

  constructor(
    code: string,
    message: string,
    options?: {
      title?: string;
      suggestedAction?: string;
      actions?: readonly string[];
      scope?: string;
      resolution?: string;
    },
  ) {
    super(message);
    this.name = "PresenceError";
    this.code = code;
    this.title = options?.title;
    this.suggestedAction = options?.suggestedAction;
    this.actions = options?.actions;
    this.scope = options?.scope;
    this.resolution = options?.resolution;
  }
}

type NativeFailure = {
  code?: unknown;
  message?: unknown;
  title?: unknown;
  suggested_action?: unknown;
  actions?: unknown;
  scope?: unknown;
  resolution?: unknown;
};

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function optionalStringArray(value: unknown): readonly string[] | undefined {
  if (Array.isArray(value)) {
    const list = value.filter((item): item is string => typeof item === "string");
    return list.length > 0 ? list : undefined;
  }
  return undefined;
}

/** Whether this window can collect native presence at all. */
export function presenceAvailable(): boolean {
  return isTauriRuntime();
}

async function invokeWithPresence<T>(
  command: string,
  args: Record<string, string>,
  fallbackMessage: string,
): Promise<T> {
  if (!presenceAvailable()) {
    throw new PresenceError(
      "presence_unavailable",
      "Only the installed desktop app can confirm you are present.",
      {
        title: "Confirmation is unavailable",
        suggestedAction: "Open the installed desktop app with its bundled engine and try again.",
        scope: "project",
      },
    );
  }
  const { invoke } = await import("@tauri-apps/api/core");
  try {
    return await invoke<T>(command, args);
  } catch (error) {
    const failure = error as NativeFailure;
    throw new PresenceError(
      typeof failure?.code === "string" ? failure.code : "presence_failed",
      typeof failure?.message === "string" ? failure.message : fallbackMessage,
      {
        title: optionalString(failure?.title),
        suggestedAction: optionalString(failure?.suggested_action),
        actions: optionalStringArray(failure?.actions),
        scope: optionalString(failure?.scope),
        resolution: optionalString(failure?.resolution),
      },
    );
  }
}

/** Reveals a managed secret after the operating system confirms the person. */
export function revealManagedSecret(
  projectId: string,
  secretId: string,
): Promise<ManagedSecretRevealResponse> {
  return invokeWithPresence<ManagedSecretRevealResponse>(
    "reveal_managed_secret",
    { projectId, secretId },
    "This secret could not be revealed.",
  );
}

/**
 * Approves an option that releases values a person gave Painted Wolf Code.
 * The desktop shell confirms the person, signs the engine's challenge, and
 * resolves the checkpoint itself; the webview never sees the proof.
 */
export function resolveCheckpointWithPresence(
  sessionId: string,
  checkpointId: string,
  optionId: string,
): Promise<CheckpointResponse> {
  return invokeWithPresence<CheckpointResponse>(
    "resolve_checkpoint_with_presence",
    { sessionId, checkpointId, optionId },
    "This approval could not be confirmed.",
  );
}
