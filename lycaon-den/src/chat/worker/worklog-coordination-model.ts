import type { BoardView } from "../../api/types.ts";

export type WorklogReservationGroup = {
  jobId: string;
  agentType: string;
  paths: string[];
};

/** Fold board roster reservations by worker job id. */
export function reservationsByWorker(
  board?: BoardView,
): WorklogReservationGroup[] {
  const roster = board?.roster;
  if (!Array.isArray(roster) || roster.length === 0) return [];
  const groups: WorklogReservationGroup[] = [];
  for (const row of roster) {
    const paths = row.reservations?.filter((p) => p.trim()) ?? [];
    if (paths.length === 0) continue;
    groups.push({
      jobId: row.worker_id,
      agentType: row.agent_type?.trim() || "worker",
      paths,
    });
  }
  return groups;
}
