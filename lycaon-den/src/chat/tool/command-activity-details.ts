import type { CommandActivityStep, CommandExecutionOutput, CommandExecutionReceipt, ToolFeedback } from "../../api/types.ts";
import type { CommandActivityView } from "./command-activity-model.ts";

export type ActivityFact = { label: string; value: string };

/** Lines the output region shows before pointing to the full output. */
export const ACTIVITY_OUTPUT_LINES = 6;

const SETTLED_FAILURE = new Set(["failed", "refused", "interrupted"]);

function exitCode(view: CommandActivityView): number | undefined {
  return view.fact.status_code ?? view.calls.find(call => call.commandProgram)?.commandProgram?.status_code;
}

function stopCode(view: CommandActivityView, executions: readonly CommandExecutionReceipt[]): string | undefined {
  return executions.find(row => row.stop_code)?.stop_code ?? view.calls.find(call => call.commandProgram?.stop_code)?.commandProgram?.stop_code;
}

function primaryOutput(executions: readonly CommandExecutionReceipt[]): CommandExecutionOutput | undefined {
  for (let i = executions.length - 1; i >= 0; i--) {
    const stdout = executions[i]?.outputs?.find(output => output.stream === "stdout");
    if (stdout) return stdout;
  }
  return undefined;
}

function linesOfOutput(output: CommandExecutionOutput | undefined): string | undefined {
  if (!output) return undefined;
  if (output.size_bytes === 0) return "No output";
  if (output.line_count === undefined) return undefined;
  return `${output.line_count.toLocaleString()} ${output.line_count === 1 ? "line" : "lines"} of output`;
}

/** The settled facts the header does not already state, or undefined for a clean success. */
export function activityResultLine(view: CommandActivityView, executions: readonly CommandExecutionReceipt[]): string | undefined {
  const fact = view.fact;
  switch (fact.state) {
    case "running":
    case "waiting":
      return undefined;
    case "refused": {
      const code = stopCode(view, executions);
      return code ? `Stopped by the host · ${code}` : "Stopped by the host";
    }
    case "interrupted":
      return executions.some(row => row.owner_started || row.owner_invoked) ? "Interrupted after it started" : "Interrupted before it started";
    case "failed": {
      const code = exitCode(view);
      return code === undefined ? "Failed" : `Exited ${code}`;
    }
  }
  if (fact.phase === "inspect") return linesOfOutput(primaryOutput(executions));
  if (fact.phase === "mutate") return (fact.execution_count ?? 0) > 1 ? `${fact.execution_count!.toLocaleString()} executions` : undefined;
  if (fact.job_handle) return "Started in the background";
  const code = exitCode(view);
  const lines = linesOfOutput(primaryOutput(executions));
  const exited = code === undefined ? undefined : `Exited ${code}`;
  return [exited, lines].filter(Boolean).join(" · ") || undefined;
}

export type ActivityOutput = {
  stream: "stdout" | "stderr" | "result";
  lines: string[];
  /** Whether lines are the end of the output rather than its start. */
  tail: boolean;
  /** Describes output beyond the shown lines, or undefined when all of it is shown. */
  hidden?: string;
};

function splitLines(text: string): string[] {
  const lines = text.replace(/\n$/, "").split("\n");
  return lines.length === 1 && lines[0] === "" ? [] : lines;
}

function fromExecution(output: CommandExecutionOutput, tail: boolean): ActivityOutput | undefined {
  const preview = splitLines(output.preview);
  const continues = output.preview_truncated || !output.complete;
  let lines: string[];
  if (tail && continues && output.tail_preview) lines = splitLines(output.tail_preview);
  else if (tail) lines = preview.slice(-ACTIVITY_OUTPUT_LINES);
  else lines = preview.slice(0, ACTIVITY_OUTPUT_LINES);
  if (lines.length === 0) return undefined;
  const total = output.line_count;
  let hidden: string | undefined;
  if (total !== undefined && total > lines.length) hidden = `${tail ? "Last" : "First"} ${lines.length} of ${total.toLocaleString()} lines`;
  else if (total === undefined && (continues || preview.length > lines.length)) hidden = "Output continues";
  return { stream: output.stream, lines, tail, hidden };
}

/**
 * The output worth showing inline: the failing execution's error tail for a
 * failure, otherwise the start of the last execution's output.
 */
export function activityOutput(view: CommandActivityView, executions: readonly CommandExecutionReceipt[]): ActivityOutput | undefined {
  const failing = SETTLED_FAILURE.has(view.fact.state);
  if (failing) {
    for (let i = executions.length - 1; i >= 0; i--) {
      const row = executions[i]!;
      if (!SETTLED_FAILURE.has(row.state) && !(row.status_code !== undefined && row.status_code !== 0)) continue;
      const stderr = row.outputs?.find(output => output.stream === "stderr" && output.size_bytes > 0);
      const stdout = row.outputs?.find(output => output.stream === "stdout" && output.size_bytes > 0);
      const shown = (stderr && fromExecution(stderr, true)) ?? (stdout && fromExecution(stdout, true));
      if (shown) return shown;
    }
    // A program refused before any execution has only its host-written result.
    const result = view.calls.find(call => call.output)?.output;
    if (result && executions.every(row => !row.outputs?.length)) {
      const lines = splitLines(result);
      return { stream: "result", lines: lines.slice(-ACTIVITY_OUTPUT_LINES), tail: true, hidden: lines.length > ACTIVITY_OUTPUT_LINES ? "Output continues" : undefined };
    }
    return undefined;
  }
  for (let i = executions.length - 1; i >= 0; i--) {
    const output = executions[i]?.outputs?.find(item => item.stream === "stdout" && item.size_bytes > 0)
      ?? executions[i]?.outputs?.find(item => item.stream === "stderr" && item.size_bytes > 0);
    const shown = output && fromExecution(output, view.fact.state === "running");
    if (shown) return shown;
  }
  return undefined;
}

/** Steps add information only when their settled states differ. */
export function activityStepsDiffer(steps: readonly CommandActivityStep[] | undefined): boolean {
  return !!steps && steps.length > 1 && steps.some(step => step.state !== steps[0]!.state);
}

const UNCERTAIN = { attempted: "Attempted, outcome unknown", unknown: "Unknown outcome" } as const;

/** Facts that differ from a clean success, each worth a row. */
export function activityExceptions(view: CommandActivityView, executions: readonly CommandExecutionReceipt[], feedback?: readonly ToolFeedback[]): ActivityFact[] {
  const facts: ActivityFact[] = [];
  const fact = view.fact;
  const program = view.calls.find(call => call.commandProgram)?.commandProgram;
  // The host never observes what a finished process changed, so an unknown
  // outcome is its default rather than an exception.
  const observed = !(fact.phase === "execute" && fact.state === "completed");
  const uncertain = (row: CommandExecutionReceipt) => observed && (row.effect_certainty === "attempted" || row.effect_certainty === "unknown");
  if (fact.failed_count && (fact.execution_count ?? 0) > 1) facts.push({ label: "Reported failures", value: fact.failed_count.toLocaleString() });
  if (program?.stop_code && fact.state !== "refused") facts.push({ label: "Command stop", value: program.stop_code });
  if ((observed && fact.uncertain_effects) || executions.some(uncertain)) {
    facts.push({ label: "Effects", value: fact.completed_effects || executions.some(row => row.effect_certainty === "completed") ? "Partial effects; unknown outcome" : "Unknown outcome" });
  }
  for (const entry of feedback ?? []) {
    facts.push({ label: "Code", value: entry.code });
    const operation = entry.details?.native_operation;
    if (typeof operation === "string") facts.push({ label: "Operation", value: operation });
  }
  if (fact.job_handle) facts.push({ label: "Background job", value: fact.job_handle });
  // "." is the project root, the default directory.
  if (fact.directory && fact.directory !== ".") facts.push({ label: "Directory", value: fact.directory });
  for (const row of executions) {
    if (row.host_stop_code && row.host_stop_code !== row.stop_code) facts.push({ label: "Host stop", value: row.host_stop_code });
    if (uncertain(row)) facts.push({ label: row.command, value: UNCERTAIN[row.effect_certainty as keyof typeof UNCERTAIN] });
    if (row.owner_started && !row.owner_invoked && row.state !== "completed") facts.push({ label: "Owner", value: "Started, invocation unknown" });
    if (row.revision_conflict) {
      facts.push({ label: "Conflict", value: row.revision_conflict.path }, { label: "Expected revision", value: row.revision_conflict.expected_revision }, { label: "Observed revision", value: row.revision_conflict.observed_revision });
    }
    if (row.accepted_revision && !row.published_revision) facts.push({ label: "Publication", value: row.publication_eligible ? "Accepted, not yet published" : "Accepted, not published" });
    if (row.held_version_id) facts.push({ label: "Held version", value: row.held_version_id });
    if (row.recovery_ref) facts.push({ label: "Recovery", value: row.recovery_ref });
  }
  return facts;
}

export function commandActivityStepOutcome(step: CommandActivityStep): string {
  const parts: string[] = [];
  if (step.waiting) parts.push(`${step.waiting} waiting`);
  if (step.running) parts.push(`${step.running} running`);
  if (step.failed) parts.push(`${step.failed} reported failure`);
  if (step.refused) parts.push(`${step.refused} refused`);
  if (step.interrupted) parts.push(`${step.interrupted} interrupted`);
  return parts.join(" · ") || step.outcome;
}
