/** Buffers become dirty immediately; draft materialization is debounced. */

import type { FileBufferKey } from "../components/project-files-model.ts";

const SYNC_DEBOUNCE_MS = 120;

const pending = new Map<string, () => void>();
let timer: ReturnType<typeof setTimeout> | undefined;

function id(projectId: string, key: FileBufferKey): string {
  return `${projectId}\0${key}`;
}

function runPending(k: string): void {
  const write = pending.get(k);
  if (!write) return;
  pending.delete(k);
  write();
}

/** Each buffer retains only its latest pending draft write. */
export function scheduleFilesDraftSync(
  projectId: string,
  key: FileBufferKey,
  write: () => void,
): void {
  pending.set(id(projectId, key), write);
  if (timer != null) return;
  timer = setTimeout(() => {
    timer = undefined;
    flushFilesDraftSync();
  }, SYNC_DEBOUNCE_MS);
}

export function flushFilesDraftSyncFor(
  projectId: string,
  key: FileBufferKey,
): void {
  runPending(id(projectId, key));
}

export function cancelFilesDraftSyncFor(
  projectId: string,
  key: FileBufferKey,
): void {
  pending.delete(id(projectId, key));
}

export function flushFilesDraftSync(): void {
  if (timer != null) {
    clearTimeout(timer);
    timer = undefined;
  }
  for (const k of [...pending.keys()]) runPending(k);
}

export function resetFilesDraftSyncForTests(): void {
  if (timer != null) clearTimeout(timer);
  timer = undefined;
  pending.clear();
}
