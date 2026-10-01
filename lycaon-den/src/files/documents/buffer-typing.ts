/** Last user keystroke timestamps per buffer — typing defer for hunk playback. */

import type { FileBufferKey } from "../components/project-files-model.ts";

const lastTyped = new Map<string, number>();
const burstUsed = new Map<string, number>();

function id(projectId: string, key: FileBufferKey): string {
  return `${projectId}\0${key}`;
}

export function noteBufferTyped(projectId: string, key: FileBufferKey, at = Date.now()): void {
  lastTyped.set(id(projectId, key), at);
}

export function lastBufferTypedAt(
  projectId: string,
  key: FileBufferKey,
): number | undefined {
  return lastTyped.get(id(projectId, key));
}

/** Milliseconds of burst budget already consumed in the current rapid-fire window. */
export function consumeBurstMs(
  projectId: string,
  key: FileBufferKey,
  usedMs: number,
  windowMs: number,
  now = Date.now(),
): number {
  const k = id(projectId, key);
  const prev = burstUsed.get(k) ?? 0;
  // Reset window when idle longer than the burst window itself.
  const last = lastTyped.get(`${k}:burst`) ?? 0;
  let base = prev;
  if (now - last > windowMs) base = 0;
  const next = base + usedMs;
  burstUsed.set(k, next);
  lastTyped.set(`${k}:burst`, now);
  return next;
}

export function burstRemainingMs(
  projectId: string,
  key: FileBufferKey,
  budgetMs: number,
  windowMs: number,
  now = Date.now(),
): number {
  const k = id(projectId, key);
  const last = lastTyped.get(`${k}:burst`) ?? 0;
  if (now - last > windowMs) return budgetMs;
  const used = burstUsed.get(k) ?? 0;
  return Math.max(0, budgetMs - used);
}
