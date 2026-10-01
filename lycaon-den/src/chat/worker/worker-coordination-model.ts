import type {
  BoardView,
  FindingsDigest,
  Finding,
} from "../../api/types.ts";

/** Notes this worker posted (record_finding agent = job id). */
export function findingsByWorker(
  findings: FindingsDigest | undefined,
  jobId: string,
): Finding[] {
  const id = jobId.trim();
  if (!id || !findings?.findings?.length) return [];
  return findings.findings.filter((row) => row.agent?.trim() === id);
}

/** Peer notes this worker receives — findings from other jobs. */
export function siblingNotesFor(
  findings: FindingsDigest | undefined,
  jobId: string,
  since?: string,
): Finding[] {
  const id = jobId.trim();
  if (!id || !findings?.findings?.length) return [];
  const sinceMs = since ? Date.parse(since) : Number.NaN;
  return findings.findings.filter((row) => {
    const agent = row.agent?.trim();
    if (!agent || agent === id) return false;
    if (Number.isFinite(sinceMs) && row.recorded_at) {
      const tsMs = Date.parse(row.recorded_at);
      if (Number.isFinite(tsMs) && tsMs <= sinceMs) return false;
    }
    return true;
  });
}

/** Reserved paths from this worker's roster entry. */
export function reservationsFor(
  roster: BoardView["roster"],
  jobId: string,
): string[] {
  const id = jobId.trim();
  if (!id || !Array.isArray(roster)) return [];
  const row = roster.find((entry) => entry.worker_id === id);
  return row?.reservations?.filter((path) => path.trim()) ?? [];
}

/** Human label for a peer note's author: its agent type, else a short job id. */
export function findingAgentLabel(
  roster: BoardView["roster"],
  agent: string | undefined,
): string {
  const id = agent?.trim() ?? "";
  if (!id) return "";
  const entry = Array.isArray(roster)
    ? roster.find((row) => row.worker_id === id)
    : undefined;
  const type = entry?.agent_type?.trim();
  if (type) return type;
  return id.length > 8 ? id.slice(0, 8) : id;
}
