import type { TurnLoad } from "../../api/types.ts";

/** One receipt per (turn, trigger, model call): a repeat replaces the earlier copy. */
function turnLoadKey(load: TurnLoad): string {
  return `${load.trigger}:${load.tool_call_id ?? ""}`;
}

/** Folds receipts into a record keyed by opening message; receipts without one are skipped. */
export function mergeTurnLoads(
  record: Record<string, TurnLoad[]>,
  loads: readonly TurnLoad[],
): Record<string, TurnLoad[]> {
  let next = record;
  for (const load of loads) {
    const opening = load.opening_message_id?.trim();
    if (!opening) continue;
    const current = next[opening] ?? [];
    const key = turnLoadKey(load);
    const index = current.findIndex((entry) => turnLoadKey(entry) === key);
    if (index >= 0 && current[index] === load) continue;
    if (next === record) next = { ...record };
    const updated = current.slice();
    if (index >= 0) updated[index] = load;
    else updated.push(load);
    next[opening] = updated;
  }
  return next;
}
