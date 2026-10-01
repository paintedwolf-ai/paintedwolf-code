import type { ManagedSecretRevealResponse } from "../../api/types.ts";
import { isTauriRuntime } from "../runtime.ts";

export class ManagedSecretRevealError extends Error {
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
    this.name = "ManagedSecretRevealError";
    this.code = code;
    this.title = options?.title;
    this.suggestedAction = options?.suggestedAction;
    this.actions = options?.actions;
    this.scope = options?.scope;
    this.resolution = options?.resolution;
  }
}

type NativeRevealFailure = {
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

/** Reveals through native authentication and challenge completion. */
export async function revealManagedSecret(
  projectId: string,
  secretId: string,
): Promise<ManagedSecretRevealResponse> {
  if (!isTauriRuntime()) {
    throw new ManagedSecretRevealError(
      "managed_secret_reveal_unavailable",
      "Authenticated reveal is available only in the installed desktop app.",
      {
        title: "Secret reveal is unavailable",
        suggestedAction:
          "Open the installed desktop app with its bundled engine and try again.",
        scope: "project",
      },
    );
  }
  const { invoke } = await import("@tauri-apps/api/core");
  try {
    return await invoke<ManagedSecretRevealResponse>("reveal_managed_secret", {
      projectId,
      secretId,
    });
  } catch (error) {
    const failure = error as NativeRevealFailure;
    throw new ManagedSecretRevealError(
      typeof failure?.code === "string"
        ? failure.code
        : "managed_secret_reveal_failed",
      typeof failure?.message === "string"
        ? failure.message
        : "This secret could not be revealed.",
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
