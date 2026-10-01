import type { TurnClock } from "../../api/types.ts";

/** Combines banked time with the current live span. */
export function turnClockElapsedMs(
  clock: Pick<TurnClock, "active_ms" | "running" | "running_at"> | undefined,
  now: number,
): number {
  if (!clock) return 0;
  const banked = clock.active_ms ?? 0;
  if (!clock.running || !clock.running_at) return banked;
  const since = Date.parse(clock.running_at);
  if (Number.isNaN(since)) return banked;
  // Clock skew can make the live span negative.
  return banked + Math.max(0, now - since);
}

/** A resumed clock retains its last settlement, ordering page snapshots against live updates. */
export function isTurnClockAsNew(next: TurnClock, current: TurnClock | undefined): boolean {
  if (!current) return true;
  const nextSettled = next.settled_at ? Date.parse(next.settled_at) : Number.NEGATIVE_INFINITY;
  const currentSettled = current.settled_at ? Date.parse(current.settled_at) : Number.NEGATIVE_INFINITY;
  if (nextSettled !== currentSettled) return nextSettled > currentSettled;
  if (next.running !== current.running) return next.running;
  return next.active_ms >= current.active_ms;
}

/** Folds clocks into a record keyed by opening message; clocks without one are skipped. */
export function mergeTurnClocks(
  record: Record<string, TurnClock>,
  clocks: readonly TurnClock[],
): Record<string, TurnClock> {
  let next = record;
  for (const clock of clocks) {
    const opening = clock.opening_message_id?.trim();
    if (!opening || !isTurnClockAsNew(clock, next[opening])) continue;
    if (next === record) next = { ...record };
    next[opening] = clock;
  }
  return next;
}
