import type { DenAppStateV1 } from "../../shared/app-state-types.ts";
import { persistAppState } from "./app-state-snapshot.ts";

/**
 * Writes UI state without surfacing failure: resolves once the write settles,
 * and a failed patch stays queued for the next write.
 */
export function persistAppStateInBackground(
  partial: Partial<DenAppStateV1>,
): Promise<void> {
  return persistAppState(partial).catch(() => undefined);
}
