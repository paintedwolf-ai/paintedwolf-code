/**
 * Detect installed external editors through the native bridge.
 */

import type { FoundEditor } from "../../../shared/app-state-types.ts";
import { isTauriRuntime } from "../runtime.ts";

export type DetectEditorsOptions = {
  /** Override native detection for tests. */
  isTauri?: boolean;
  /** Inject the invoke call (tests). */
  invokeDetect?: () => Promise<FoundEditor[]>;
};

async function defaultInvoke(): Promise<FoundEditor[]> {
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<FoundEditor[]>("detect_editors");
}

/**
 * List installed editors in catalog order. Returns null without native support.
 */
export async function detectEditors(
  options?: DetectEditorsOptions,
): Promise<FoundEditor[] | null> {
  const tauri =
    options?.isTauri !== undefined ? options.isTauri : isTauriRuntime();
  if (!tauri) return null;
  const invoke = options?.invokeDetect ?? defaultInvoke;
  return await invoke();
}
