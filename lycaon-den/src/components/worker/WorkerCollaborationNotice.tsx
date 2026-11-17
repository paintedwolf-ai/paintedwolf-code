import { Show } from "solid-js";
import type {
  BoardView,
  WorkerInstructionStatus,
  WorkerTask,
} from "../../api/types.ts";
import { isTaskToolPart } from "../../chat/task/task-card-model.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import {
  findSessionWorker,
  matchWorkerForTask,
  type OpenWorkerOptions,
} from "../../chat/worker/workers-model.ts";
import { boardWorkerForTask } from "../../store/workers-board-sync.ts";

type CollaborationNoticeKind =
  | "instruction"
  | "question"
  | "answer"
  | "round";

interface WorkerCollaborationNoticeView {
  kind: CollaborationNoticeKind;
  workerLabel: string;
  targetWorkerId: string;
  headline: string;
  detail?: string;
  round?: number;
  messageSeq?: number;
  instructionStatus?: WorkerInstructionStatus;
}

type InstructReceipt = {
  worker?: string;
  outcome?: string;
  round?: number;
  instruction_seq?: number;
};

function stringArg(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function clipText(value: string, max = 140): string {
  const singleLine = value.replace(/\s+/g, " ").trim();
  if (singleLine.length <= max) return singleLine;
  return `${singleLine.slice(0, max - 1).trimEnd()}…`;
}

function parseInstructReceipt(output?: string | null): InstructReceipt | null {
  const raw = output?.trim();
  if (!raw || !raw.startsWith("{")) return null;
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    return {
      worker: typeof parsed.worker === "string" ? parsed.worker.trim() : undefined,
      outcome: typeof parsed.outcome === "string" ? parsed.outcome.trim() : undefined,
      round: typeof parsed.round === "number" ? parsed.round : undefined,
      instruction_seq:
        typeof parsed.instruction_seq === "number" ? parsed.instruction_seq : undefined,
    };
  } catch {
    return null;
  }
}

function instructionStatusLabel(status: WorkerInstructionStatus): string {
  switch (status) {
    case "applied":
      return "Applied";
    case "queued":
      return "Queued";
    case "not_applied":
      return "Not applied";
  }
}

/** Projects later collaboration activity onto a chronological notice linking to the worker. */
export function collaborationNoticeForToolPart(
  part: ToolPartView,
  opts: {
    matchedWorker?: WorkerTask;
    sessionWorkers: readonly WorkerTask[];
    board?: BoardView;
  },
): WorkerCollaborationNoticeView | null {
  if (part.tool === "instruct_worker") {
    const receipt = parseInstructReceipt(part.output);
    const argWorker = stringArg(part.args?.worker);
    const workerRef =
      part.workerSummary?.worker_record_id?.trim() ||
      part.workerSummary?.worker_name?.trim() ||
      part.workerSummary?.worker_id?.trim() ||
      part.jobId?.trim() ||
      receipt?.worker ||
      argWorker;
    const worker = findSessionWorker(opts.sessionWorkers, workerRef);
    const bw = boardWorkerForTask(opts.board, worker);
    const workerLabel =
      bw?.name?.trim() ||
      worker?.worker_name?.trim() ||
      part.workerSummary?.worker_name?.trim() ||
      receipt?.worker ||
      argWorker ||
      worker?.agent_type?.trim() ||
      "worker";
    const targetWorkerId =
      worker?.id || part.workerSummary?.worker_id?.trim() || part.jobId?.trim() || workerRef;
    const round =
      part.workerSummary?.round ??
      receipt?.round ??
      (receipt?.outcome === "started" ? worker?.round : undefined);
    const messageSeq =
      typeof receipt?.instruction_seq === "number" && receipt.instruction_seq > 0
        ? receipt.instruction_seq
        : undefined;
    const instructionStatus: WorkerInstructionStatus | undefined =
      part.status === "error" || part.outcome === "rejected" || part.outcome === "error"
        ? "not_applied"
        : receipt?.outcome === "queued"
          ? "queued"
          : receipt?.outcome === "started" || part.workerSummary != null
            ? "applied"
            : undefined;
    const decisionQuestion =
      part.workerSummary?.decision_request?.question?.trim() || bw?.question?.trim();
    const settledAsking =
      part.workerSummary?.decision_request != null ||
      part.workerSummary?.status === "held" ||
      (part.workerSummary != null && bw?.state === "asking");
    const instructions = stringArg(part.args?.instructions);
    const headline = settledAsking
      ? `${workerLabel} needs an answer`
      : instructionStatus === "not_applied"
        ? `Instruction to ${workerLabel} was not applied`
        : instructionStatus === "queued"
          ? `Queued instruction for ${workerLabel}`
          : part.status === "running" && !receipt && !part.workerSummary
            ? `Instructing ${workerLabel}…`
            : round && round > 1
              ? `Sent instruction to ${workerLabel} · Round ${round}`
              : `Sent instruction to ${workerLabel}`;
    const rawDetail =
      settledAsking && decisionQuestion
        ? decisionQuestion
        : part.error?.trim() || instructions;
    return {
      kind: settledAsking ? "question" : "instruction",
      workerLabel,
      targetWorkerId,
      headline,
      detail: rawDetail ? clipText(rawDetail) : undefined,
      round,
      messageSeq,
      instructionStatus,
    };
  }

  if (part.tool === "answer_decision") {
    const workerRef =
      stringArg(part.args?.worker) || stringArg(part.args?.job_id) || part.jobId?.trim() || "";
    const worker = findSessionWorker(opts.sessionWorkers, workerRef);
    const bw = boardWorkerForTask(opts.board, worker);
    const workerLabel =
      bw?.name?.trim() ||
      worker?.worker_name?.trim() ||
      worker?.agent_type?.trim() ||
      workerRef ||
      "worker";
    const answer = stringArg(part.args?.answer) || stringArg(part.args?.option);
    return {
      kind: "answer",
      workerLabel,
      targetWorkerId: worker?.id || workerRef,
      headline: `Answered ${workerLabel}`,
      detail: answer ? clipText(answer) : undefined,
    };
  }

  if (isTaskToolPart(part) && !opts.matchedWorker) {
    const existing = matchWorkerForTask(part, opts.sessionWorkers);
    if (!existing) return null;
    const bw = boardWorkerForTask(opts.board, existing);
    const workerLabel =
      bw?.name?.trim() || existing.worker_name?.trim() || existing.agent_type || "worker";
    const round = part.workerSummary?.round ?? existing.round;
    const decisionQuestion =
      part.workerSummary?.decision_request?.question?.trim() || bw?.question?.trim();
    const asking =
      part.workerSummary?.decision_request != null ||
      part.workerSummary?.status === "held" ||
      bw?.state === "asking";
    const detail = asking
      ? decisionQuestion
      : existing.result?.summary?.trim();
    return {
      kind: asking ? "question" : "round",
      workerLabel,
      targetWorkerId: existing.id,
      headline: asking
        ? `${workerLabel} needs an answer`
        : round && round > 1
          ? `${workerLabel} · Round ${round}`
          : `Updated ${workerLabel}`,
      detail: detail ? clipText(detail) : undefined,
      round,
    };
  }

  return null;
}

export function WorkerCollaborationNotice(props: {
  notice: WorkerCollaborationNoticeView;
  entryKey?: string;
  sessionId?: string;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
}) {
  const { bindTranscriptEntry } = useTranscriptEntry(() => {
    const entryKey = props.entryKey?.trim();
    return entryKey ? { sessionId: props.sessionId, entryKey } : undefined;
  });
  const canOpen = () => Boolean(props.onOpenWorker && props.notice.targetWorkerId);
  const open = () => {
    if (!props.onOpenWorker || !props.notice.targetWorkerId) return;
    props.onOpenWorker(props.notice.targetWorkerId, {
      scrollTo: "history",
      messageSeq: props.notice.messageSeq,
      roundOrdinal: props.notice.round,
    });
  };
  const body = () => (
    <>
      <span class="den-worker-collab-notice-headline">{props.notice.headline}</span>
      <Show when={props.notice.instructionStatus} keyed>
        {(status) => (
          <span class="den-worker-fact-badge" data-testid="worker-instruction-status-badge">
            {instructionStatusLabel(status)}
          </span>
        )}
      </Show>
      <Show when={props.notice.detail}>
        {(detail) => <span class="den-worker-collab-notice-detail">{detail()}</span>}
      </Show>
      <Show when={canOpen()}>
        <span class="den-worker-collab-notice-link">View in worker</span>
      </Show>
    </>
  );

  return (
    <Show
      when={canOpen()}
      fallback={
        <div
          ref={bindTranscriptEntry}
          role="status"
          class="den-worker-collab-notice"
          data-testid="worker-collaboration-notice"
          data-collaboration-kind={props.notice.kind}
          data-worker-name={props.notice.workerLabel}
          data-round={props.notice.round}
          data-instruction-seq={props.notice.messageSeq}
          data-instruction-status={props.notice.instructionStatus}
        >
          {body()}
        </div>
      }
    >
      <button
        type="button"
        ref={bindTranscriptEntry}
        class="den-worker-collab-notice"
        data-testid="worker-collaboration-notice"
        data-collaboration-kind={props.notice.kind}
        data-worker-name={props.notice.workerLabel}
        data-round={props.notice.round}
        data-instruction-seq={props.notice.messageSeq}
        data-instruction-status={props.notice.instructionStatus}
        onClick={open}
      >
        {body()}
      </button>
    </Show>
  );
}
