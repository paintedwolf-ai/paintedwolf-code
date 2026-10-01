import type {
  CoordinatorBatchPhase,
  Message,
  ProgressChange,
  ProgressChangeKind,
  ProgressCompleteMeta,
  ProgressState,
  ProgressStep,
  ProgressUpdateSummary,
} from "../../api/types.ts";

type ProgressHistoryEntry = {
  key: string;
  kind: ProgressChangeKind | "completed" | "summary";
  label: string;
  state: ProgressState | "summary";
  ts: string;
};

function itemCountLabel(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

export function progressUpdateSummaryLabel(
  summary: ProgressUpdateSummary,
  initial: boolean,
): string {
  const parts = [
    initial
      ? `${itemCountLabel(summary.total_steps, "progress item")} added`
      : `${itemCountLabel(summary.change_count, "progress item")} updated`,
  ];
  if (!initial) parts.push(`${summary.total_steps} total`);
  if (summary.pending > 0) parts.push(`${summary.pending} pending`);
  if (summary.done > 0) parts.push(`${summary.done} done`);
  if (summary.na > 0) parts.push(`${summary.na} not applicable`);
  return parts.join(" · ");
}

function progressChangeLabel(change: ProgressChange): string {
  const prev = change.prev_label?.trim();
  if (change.kind === "updated" && prev) {
    return `${prev} → ${change.label}`;
  }
  return change.label;
}

function progressChangeDisplayState(
  change: ProgressChange,
): ProgressState {
  if (change.kind === "removed") return "na";
  return change.state;
}

export function progressChangesToSteps(
  changes: readonly ProgressChange[],
): ProgressStep[] {
  return changes.map((change) => ({
    label: progressChangeLabel(change),
    state: progressChangeDisplayState(change),
  }));
}

function progressChangeEntries(
  message: Message,
  changes: readonly ProgressChange[],
): ProgressHistoryEntry[] {
  return changes.map((change, index) => ({
    key: `${message.id}:change:${index}`,
    kind: change.kind,
    label: progressChangeLabel(change),
    state: progressChangeDisplayState(change),
    ts: message.created_at,
  }));
}

function progressCompleteEntries(
  message: Message,
  complete: ProgressCompleteMeta,
): ProgressHistoryEntry[] {
  return complete.steps.map((step, index) => ({
    key: `${message.id}:complete:${index}`,
    kind: "completed",
    label: step.label,
    state: step.state,
    ts: message.created_at,
  }));
}

export function progressHistoryFromMessages(
  messages: readonly Message[],
): ProgressHistoryEntry[] {
  const updates = messages
    .filter((message) =>
      message.progress_update !== undefined ||
      message.progress_complete !== undefined
    )
    .slice()
    .sort((a, b) => {
      if (typeof a.ord === "number" && typeof b.ord === "number") {
        return a.ord - b.ord;
      }
      return 0;
    });

  const entries: ProgressHistoryEntry[] = [];
  for (const msg of updates) {
    if (msg.progress_complete) {
      entries.push(...progressCompleteEntries(msg, msg.progress_complete));
      continue;
    }

    const meta = msg.progress_update;
    if (!meta) continue;
    const ts = msg.created_at;
    if (meta.summary) {
      entries.push({
        key: `${msg.id}:summary`,
        kind: "summary",
        label: progressUpdateSummaryLabel(meta.summary, meta.initial === true),
        state: "summary",
        ts,
      });
      continue;
    }
    if (meta.initial) {
      const steps = meta.steps ?? [];
      steps.forEach((step, i) => {
        entries.push({
          key: `${msg.id}:step:${i}`,
          kind: "created",
          label: step.label,
          state: step.state,
          ts,
        });
      });
      continue;
    }
    entries.push(...progressChangeEntries(msg, meta.changes ?? []));
  }
  return entries;
}

/** Terminal rows move from live progress into the transcript. */
export function isTerminalProgressStep(state: string): boolean {
  return state === "done" || state === "na";
}

export function liveProgressItems(steps: readonly ProgressStep[]): ProgressStep[] {
  return steps.filter((s) => !isTerminalProgressStep(s.state));
}

function progressStepsMatch(
  a: readonly ProgressStep[],
  b: readonly ProgressStep[],
): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const left = a[i]!;
    const right = b[i]!;
    if (left.label !== right.label || left.state !== right.state) return false;
  }
  return true;
}

function latestProgressCompleteMessage(
  messages: readonly Message[],
): ProgressCompleteMeta | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]!;
    if (m.progress_complete) return m.progress_complete;
  }
  return undefined;
}

export function isProgressArchivedInTranscript(
  steps: readonly ProgressStep[],
  messages: readonly Message[],
): boolean {
  if (steps.length === 0) return false;
  if (steps.some((s) => !isTerminalProgressStep(s.state))) return false;
  const complete = latestProgressCompleteMessage(messages);
  if (!complete || complete.steps.length === 0) return false;
  return progressStepsMatch(complete.steps, steps);
}

/** A closed batch has no live phase. */
export type VisibleBatchPhase = Exclude<CoordinatorBatchPhase, "closed">;

export function visibleBatchPhase(
  phase: CoordinatorBatchPhase | undefined,
): VisibleBatchPhase | undefined {
  if (!phase || phase === "closed") return undefined;
  return phase;
}
