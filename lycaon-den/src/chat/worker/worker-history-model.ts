import type {
  WorkerHistoryMessage,
  WorkerHistoryRound,
  WorkerInstructionStatus,
  WorkerMessageSenderRole,
} from "../../api/types.ts";
import {
  workerDispositionPresentation,
  type WorkerDispositionBadge,
} from "./worker-state-presentation.ts";

export const WORKER_HISTORY_PAGE_LIMIT = 20;

export type WorkerHistoryFocusTarget = {
  messageSeq?: number;
  roundOrdinal?: number;
};

export function workerHistorySectionId(workerId: string): string {
  return `worker-history-${workerId}`;
}

export function workerHistorySenderLabel(role: WorkerMessageSenderRole): string {
  switch (role) {
    case "coordinator":
      return "Coordinator";
    case "worker":
      return "Worker";
    case "person":
      return "You";
    case "host":
      return "Host";
  }
}

export function workerInstructionStatusLabel(status: WorkerInstructionStatus): string {
  switch (status) {
    case "applied":
      return "Applied";
    case "queued":
      return "Queued";
    case "not_applied":
      return "Not applied";
  }
}

export function mergeWorkerHistoryMessages(
  existing: readonly WorkerHistoryMessage[],
  incoming: readonly WorkerHistoryMessage[],
): WorkerHistoryMessage[] {
  const bySeq = new Map<number, WorkerHistoryMessage>();
  for (const row of existing) bySeq.set(row.seq, row);
  for (const row of incoming) bySeq.set(row.seq, row);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

export function mergeWorkerHistoryRounds(
  existing: readonly WorkerHistoryRound[],
  incoming: readonly WorkerHistoryRound[],
): WorkerHistoryRound[] {
  const byOrdinal = new Map<number, WorkerHistoryRound>();
  for (const row of existing) byOrdinal.set(row.ordinal, row);
  for (const row of incoming) byOrdinal.set(row.ordinal, row);
  return [...byOrdinal.values()].sort((a, b) => a.ordinal - b.ordinal);
}

export function workerRoundDisposition(
  round: WorkerHistoryRound,
  agentType: string,
): WorkerDispositionBadge | undefined {
  return workerDispositionPresentation({
    id: round.worker_id,
    agent_type: agentType,
    status: round.status ?? "complete",
    created_at: round.created_at,
    result_handle: round.result_handle,
    result_disposition: round.result_disposition,
    shelve_reason: round.shelve_reason,
    result: round.result,
  });
}
