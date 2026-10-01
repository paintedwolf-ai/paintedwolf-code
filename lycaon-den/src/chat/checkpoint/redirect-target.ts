import { createSignal } from "solid-js";
import type { PendingCheckpoint } from "./checkpoint-model.ts";

/** Explicit card focus selects the composer redirect target. */
const [lastFocusedCheckpointId, setLastFocusedCheckpointIdSignal] =
  createSignal<string | null>(null);

export { lastFocusedCheckpointId };

export function setLastFocusedCheckpointId(checkpointId: string): void {
  setLastFocusedCheckpointIdSignal(checkpointId);
}

const [dockVisibleCheckpointId, setDockVisibleCheckpointIdSignal] =
  createSignal<string | null>(null);

export { dockVisibleCheckpointId };

export function setDockVisibleCheckpointId(checkpointId: string | null): void {
  setDockVisibleCheckpointIdSignal(checkpointId);
}

/** Redirect priority is the focused pending card, docked card, then newest pending card. */
export function pickRedirectTarget(
  pending: readonly PendingCheckpoint[],
  focusedId: string | null,
  visibleId: string | null,
): PendingCheckpoint | undefined {
  const byId = (id: string | null) =>
    id
      ? pending.find((checkpoint) => checkpoint.checkpointId === id)
      : undefined;
  return byId(focusedId) ?? byId(visibleId) ?? pending[pending.length - 1];
}
