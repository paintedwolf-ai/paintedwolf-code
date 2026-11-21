import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { CommandReceiptScope } from "../transcript/content/chat-content-document.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { commandProgramActive, preferredCommandProgram } from "./command-activity-snapshots.ts";
import type { ToolPartView, ToolPartStatus } from "./tool-part-model.ts";

export const FORM_PROGRAM_TOOLS: ReadonlySet<string> = new Set(["command", "summarize", "survey_repo", "code_rewrite"]);

export type CommandActivityView = {
  fact: CommandActivity;
  calls: ToolPartView[];
  members: CommandActivity[];
  receiptScopes: CommandReceiptScope[];
  outputReads?: number;
  outputReadFailed?: boolean;
  decisions?: Extract<ActivitySpanEntry, { kind: "turn_load" }>[];
};

export function receiptScope(program: string, fact?: CommandActivity): CommandReceiptScope {
  return fact?.execution_ids.length ? { programId: program, activityId: fact.id } : { programId: program };
}

/** A whole-program scope covers, and replaces, that program's activity scopes. */
export function addReceiptScope(scopes: CommandReceiptScope[], scope: CommandReceiptScope): void {
  const at = scopes.findIndex(known => known.programId === scope.programId);
  if (at < 0) {
    scopes.push(scope);
    return;
  }
  if (scopes.some(known => known.programId === scope.programId && (known.activityId === undefined || known.activityId === scope.activityId))) return;
  if (scope.activityId !== undefined) {
    scopes.push(scope);
    return;
  }
  const kept = scopes.filter(known => known.programId !== scope.programId);
  kept.splice(at, 0, scope);
  scopes.splice(0, scopes.length, ...kept);
}

function status(fact: CommandActivity): ToolPartStatus {
  if (fact.state === "running" || fact.state === "waiting") return "running";
  if (fact.state === "failed" || fact.state === "refused" || fact.state === "interrupted" || (fact.status_code ?? 0) !== 0) return "error";
  return "completed";
}

export function commandActivityStatus(activity: CommandActivityView): ToolPartStatus {
  if (activity.members.some(member => status(member) === "running")
    || (activity.outputReads && activity.calls.some(call => call.tool === "command_output" && call.status === "running"))) return "running";
  if (activity.outputReadFailed || activity.members.some(member => status(member) === "error")) return "error";
  return status(activity.fact);
}

/** Preserve visible facts missing from a receipt while keeping the receipt's current state. */
export function resolveCommandActivityProgram(
  part: ToolPartView,
  live: CommandProgramReceipt | undefined,
  remembered: CommandProgramReceipt | undefined,
): CommandProgramReceipt | undefined {
  const program = preferredCommandProgram(part.commandProgram, live);
  if (!program || !remembered?.activities?.length || program.id !== remembered.id) return program ?? remembered;
  const received = new Map(program.activities?.map(fact => [fact.id, fact]));
  const activities = remembered.activities.map(previous => {
    const current = received.get(previous.id);
    if (current) {
      received.delete(previous.id);
      return current;
    }
    if (!commandProgramActive(program) && status(previous) === "running") {
      return { ...previous, state: "unknown" as const, outcome: "Outcome unavailable", status_code: undefined };
    }
    return previous;
  });
  return { ...program, activities: [...activities, ...received.values()] };
}

function programOutcome(part: ToolPartView, program: CommandProgramReceipt): CommandActivity {
  const state = program.state === "rejected" ? "refused" : program.state;
  const outcome = state === "completed" && program.status_code !== undefined
    ? `Exited ${program.status_code}`
    : { running: "Running", waiting: "Waiting", refused: "Refused", failed: "Failed", interrupted: "Interrupted", completed: "Completed" }[state];
  return {
    id: `${program.id}:result`, group: "Running commands", label: "Run command",
    subject: program.stop_code ?? part.title ?? "Recorded command", phase: "execute", state, outcome,
    status_code: program.status_code, execution_ids: [], spans: [],
  };
}

export function programActivityFacts(
  part: ToolPartView,
  program: CommandProgramReceipt,
  outcomeWasShown: boolean,
): readonly CommandActivity[] | undefined {
  const facts = program.activities;
  if (!facts?.length || !["failed", "rejected", "interrupted"].includes(program.state)) return facts;
  return outcomeWasShown || !facts.some(fact => status(fact) === "error")
    ? [...facts, programOutcome(part, program)] : facts;
}

/** Summarizes a program when no activity row was projected. */
export function commandActivityPart(part: ToolPartView): ToolPartView {
  if (part.commandActivity || !part.commandProgram) return part;
  const fact = programOutcome(part, part.commandProgram);
  return {
    ...part,
    commandActivity: {
      fact, calls: [part], members: [fact], receiptScopes: [receiptScope(part.commandProgram.id)],
    },
  };
}
