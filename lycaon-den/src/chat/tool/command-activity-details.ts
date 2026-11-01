import type { CommandActivityStep, CommandExecutionReceipt, ToolFeedback } from "../../api/types.ts";
import type { CommandActivityView } from "./command-activity-model.ts";

type Fact = { label: string; value: string };
const EFFECTS = { none: "No effect", completed: "Completed effect", attempted: "Attempted, outcome unknown", unknown: "Unknown outcome" };

export function commandActivityFacts(view: CommandActivityView, executions: readonly CommandExecutionReceipt[], feedback?: readonly ToolFeedback[]): Fact[] {
  const facts: Fact[] = [{ label: view.fact.job_handle && view.calls.some(call => call.tool === "command") ? "Launch outcome" : "Outcome", value: view.fact.outcome }];
  const fact = view.fact;
  if (fact.execution_count) facts.push({ label: "Executions", value: fact.execution_count.toLocaleString() });
  if (fact.failed_count) facts.push({ label: "Reported failures", value: String(fact.failed_count) });
  const program = view.calls.find(call => call.commandProgram)?.commandProgram;
  if (program) facts.push({ label: "Enclosing command", value: `${program.state}${program.status_code !== undefined ? ` · exit ${program.status_code}` : ""}` });
  if (fact.uncertain_effects || executions.some(row => row.effect_certainty === "attempted" || row.effect_certainty === "unknown")) {
    facts.push({ label: "Effects", value: fact.completed_effects || executions.some(row => row.effect_certainty === "completed") ? "Partial effects; unknown outcome" : "Unknown outcome" });
  }
  for (const entry of feedback ?? []) {
    facts.push({ label: "Code", value: entry.code });
    const operation = entry.details?.native_operation;
    if (typeof operation === "string") facts.push({ label: "Operation", value: operation });
  }
  if (view.fact.job_handle) facts.push({ label: "Background job", value: view.fact.job_handle });
  if (view.fact.directory) facts.push({ label: "Directory", value: view.fact.directory });
  if (view.members.length > 1) facts.push({ label: "Observations", value: `${view.members.length} reads; each keeps its own receipt` });
  for (const row of executions) {
    if (row.stop_code) facts.push({ label: "Code", value: row.stop_code });
    if (row.host_stop_code && row.host_stop_code !== row.stop_code) facts.push({ label: "Host stop", value: row.host_stop_code });
    if (row.effect_certainty !== "none") facts.push({ label: row.command, value: EFFECTS[row.effect_certainty] });
    if (row.owner_started && !row.owner_invoked && row.state !== "completed") facts.push({ label: "Owner", value: "Started, invocation unknown" });
    if (row.evidence_ref) facts.push({ label: "Evidence", value: row.evidence_ref });
    if (row.approval_ref) facts.push({ label: "Approval", value: row.approval_ref });
    if (row.revision_conflict) {
      facts.push({ label: "Conflict", value: row.revision_conflict.path }, { label: "Expected revision", value: row.revision_conflict.expected_revision }, { label: "Observed revision", value: row.revision_conflict.observed_revision });
    }
    if (row.accepted_revision) facts.push({ label: "Accepted revision", value: row.accepted_revision });
    if (row.published_revision) facts.push({ label: "Published revision", value: row.published_revision });
    else if (row.accepted_revision) facts.push({ label: "Publication", value: row.publication_eligible ? "Accepted, not yet published" : "Accepted, not published" });
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
