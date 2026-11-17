import type {
  BoardView,
  FindingsDigest,
  Finding,
  WorkerTask,
} from "../../api/types.ts";
import { boardWorkerForTask } from "../../store/workers-board-sync.ts";

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

/** Paths this worker's live round holds. */
export function reservationsFor(
  board: BoardView | undefined,
  worker: WorkerTask,
): string[] {
  const paths = boardWorkerForTask(board, worker)?.reservations ?? [];
  return paths.filter((path) => path.trim());
}

/** Label for a peer note's author job: its worker's name, else agent type, else a short job id. */
export function findingAgentLabel(
  workers: readonly WorkerTask[] | undefined,
  agent: string | undefined,
): string {
  const id = agent?.trim() ?? "";
  if (!id) return "";
  const row = workers?.find((w) => w.id === id);
  const label = row?.worker_name?.trim() || row?.agent_type?.trim();
  if (label) return label;
  return id.length > 8 ? id.slice(0, 8) : id;
}
