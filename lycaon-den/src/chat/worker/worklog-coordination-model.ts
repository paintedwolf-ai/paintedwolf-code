import type { BoardView } from "../../api/types.ts";

export type WorklogReservationGroup = {
  jobId: string;
  worker: string;
  paths: string[];
};

/** Reserved paths grouped by the worker whose live round holds them. */
export function reservationsByWorker(
  board?: BoardView,
): WorklogReservationGroup[] {
  const workers = board?.workers;
  if (!Array.isArray(workers) || workers.length === 0) return [];
  const groups: WorklogReservationGroup[] = [];
  for (const bw of workers) {
    const paths = bw.reservations?.filter((p) => p.trim()) ?? [];
    if (paths.length === 0) continue;
    groups.push({
      jobId: bw.latest_job_id?.trim() ?? "",
      worker: bw.name.trim() || bw.agent_type?.trim() || "worker",
      paths,
    });
  }
  return groups;
}
