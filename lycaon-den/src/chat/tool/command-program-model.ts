import type {
  CommandExecutionReceipt,
  CommandProgramNode,
  CommandProgramReceipt,
  CommandProgramReceiptPage,
  ToolFeedback,
} from "../../api/types.ts";
import type { ToolFact } from "./tool-presentation-contract.ts";

/** One command card per model call; children are rows, never sibling cards. */
export type CommandStageRow = {
  id: string;
  /** Host compact display; here-document bodies are already elided. */
  command: string;
  state: string;
  provenance: string;
  source?: string;
  facts: ToolFact[];
  /** Retained stdout/stderr previews; the body stays behind its handle. */
  outputs: CommandStageOutput[];
};

export type CommandStageOutput = {
  stream: "stdout" | "stderr";
  label: string;
  preview: string;
  truncated: boolean;
  complete: boolean;
};

export type CommandProgramPresentation = {
  /** Summary words: failure, partial effects and pending approval. */
  summary: string[];
  program: string[];
  result: ToolFact[];
  stages: CommandStageRow[];
  changes: ToolFact[];
  remaining: ToolFact[];
};

const PROGRAM_STATE: Readonly<Record<CommandProgramReceipt["state"], string>> = {
  running: "Running",
  waiting: "Waiting",
  completed: "Completed",
  rejected: "Rejected",
  failed: "Failed",
  interrupted: "Interrupted",
};

const STAGE_STATE: Readonly<Record<CommandExecutionReceipt["state"], string>> = {
  prepared: "Prepared",
  waiting: "Waiting for approval",
  running: "Running",
  completed: "Completed",
  refused: "Refused",
  failed: "Failed",
  interrupted: "Interrupted",
};

const EFFECT: Readonly<Record<CommandExecutionReceipt["effect_certainty"], string>> = {
  none: "No effect",
  completed: "Completed effect",
  attempted: "Attempted, outcome unknown",
  unknown: "Unknown outcome",
};

const PROGRESS: Readonly<Record<CommandProgramReceipt["progress_state"], string>> = {
  unchanged: "Unchanged",
  committed: "Committed",
  rejected: "Rejected",
  unknown: "Unknown",
};

function span(node: CommandProgramNode | undefined): string | undefined {
  return node ? `Line ${node.line}, column ${node.column}` : undefined;
}

/** Provenance comes from host effect classes and native binding, never prose. */
function provenance(row: CommandExecutionReceipt): string {
  if (row.effect_classes?.includes("external_exec")) return "External process";
  return row.native_command ? `Native ${row.native_command}` : "Native";
}

function stageFacts(row: CommandExecutionReceipt): ToolFact[] {
  const facts: ToolFact[] = [{ label: "Effect", value: EFFECT[row.effect_certainty] }];
  if (row.status_code !== undefined) facts.push({ label: "Status", value: String(row.status_code) });
  if (row.stop_code) facts.push({ label: "Code", value: row.stop_code });
  if (row.host_stop_code && row.host_stop_code !== row.stop_code) facts.push({ label: "Host stop", value: row.host_stop_code });
  if (row.owner_started && !row.owner_invoked && row.state !== "completed") facts.push({ label: "Owner", value: "Started, invocation unknown" });
  if (row.approval_ref) facts.push({ label: "Approval", value: row.approval_ref });
  if (row.job_handle) facts.push({ label: "Background job", value: row.job_handle });
  return facts;
}

function revisionFacts(row: CommandExecutionReceipt): ToolFact[] {
  const facts: ToolFact[] = [];
  if (row.accepted_revision) facts.push({ label: "Accepted revision", value: row.accepted_revision });
  if (row.published_revision) {
    facts.push({ label: "Published revision", value: row.published_revision });
  } else if (row.accepted_revision) {
    facts.push({ label: "Publication", value: row.publication_eligible ? "Accepted, not yet published" : "Accepted, not published" });
  }
  const conflict = row.revision_conflict;
  if (conflict) {
    facts.push({ label: "Conflict", value: conflict.path, wide: true });
    facts.push({ label: "Expected revision", value: conflict.expected_revision });
    facts.push({ label: "Observed revision", value: conflict.observed_revision });
  }
  if (row.held_version_id) facts.push({ label: "Held version", value: row.held_version_id });
  if (row.recovery_ref) facts.push({ label: "Recovery", value: row.recovery_ref });
  return facts;
}

function failureFacts(feedback: readonly ToolFeedback[] | undefined): ToolFact[] {
  const first = feedback?.[0];
  if (!first) return [];
  const details = first.details ?? {};
  const facts: ToolFact[] = [{ label: "Code", value: first.code }];
  const operation = details["native_operation"];
  if (typeof operation === "string" && operation) facts.push({ label: "Failed operation", value: operation });
  const line = details["operation_source_line"] ?? details["source_line"];
  const column = details["operation_source_column"] ?? details["source_column"];
  if (typeof line === "number" && typeof column === "number") facts.push({ label: "Source", value: `Line ${line}, column ${column}` });
  return facts;
}

/** Builds the plain sections from host receipts and the result's feedback. */
export function buildCommandProgramPresentation(
  program: CommandProgramReceipt,
  page: CommandProgramReceiptPage | undefined,
  feedback?: readonly ToolFeedback[],
): CommandProgramPresentation {
  const executions = page?.executions ?? [];
  const nodes = new Map((page?.nodes ?? []).map((node) => [node.id, node]));
  const stages = executions.map((row) => ({
    id: row.id,
    command: row.command,
    state: STAGE_STATE[row.state],
    provenance: provenance(row),
    source: span(nodes.get(row.node_id)),
    facts: stageFacts(row),
    outputs: (row.outputs ?? []).map((output) => ({
      stream: output.stream,
      label: output.stream === "stdout" ? "Output" : "Errors",
      preview: output.preview,
      truncated: output.preview_truncated,
      complete: output.complete,
    })),
  }));
  const completed = executions.filter((row) => row.effect_certainty === "completed").length;
  const uncertain = executions.filter((row) => row.effect_certainty === "attempted" || row.effect_certainty === "unknown").length;
  const waiting = executions.some((row) => row.state === "waiting") || program.state === "waiting";
  const failed = program.state === "failed" || program.state === "rejected" || program.state === "interrupted";

  const summary: string[] = [PROGRAM_STATE[program.state]];
  if (failed && completed + uncertain > 0) summary.push("Partial effects");
  if (uncertain > 0) summary.push("Unknown outcome");
  if (waiting) summary.push("Approval pending");

  const result: ToolFact[] = [{ label: "State", value: PROGRAM_STATE[program.state] }];
  if (program.status_code !== undefined) result.push({ label: "Status", value: String(program.status_code) });
  if (program.stop_code) result.push({ label: "Stop code", value: program.stop_code });
  result.push(...failureFacts(feedback).filter((fact) => !(fact.label === "Code" && fact.value === program.stop_code)));
  result.push({ label: "Effects", value: `${completed} completed, ${uncertain} unknown` });

  const changes: ToolFact[] = [];
  for (const row of executions) {
    if (row.effect_certainty === "none" && !row.accepted_revision && !row.held_version_id && !row.revision_conflict) continue;
    changes.push({ label: row.command, value: EFFECT[row.effect_certainty], wide: true });
    changes.push(...revisionFacts(row));
  }

  const remaining: ToolFact[] = [];
  // Completion settles the program even when static nodes have no executions.
  if (program.state !== "completed") {
    const notStarted = (page?.nodes ?? []).filter((node) => node.state === "not_started");
    for (const node of notStarted) remaining.push({ label: "Not started", value: span(node) ?? node.id });
    if (page?.nodes_omitted) remaining.push({ label: "Omitted statements", value: String(page.nodes_omitted) });
    if (program.progress_state !== "unchanged") remaining.push({ label: "Progress", value: PROGRESS[program.progress_state] });
  }

  return {
    summary,
    program: [...new Set(executions.map((row) => row.command))],
    result,
    stages,
    changes,
    remaining,
  };
}
