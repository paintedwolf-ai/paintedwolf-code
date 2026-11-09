import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

export type CommandActivityView = {
  fact: CommandActivity;
  calls: ToolPartView[];
  members: CommandActivity[];
  programIds: string[];
  outputReads?: number;
  decisions?: Extract<ActivitySpanEntry, { kind: "turn_load" }>[];
};

type Assignment = { program: string; key: string; label: string; group: string; subject: string };
type CallAssignment = { row: string; program: string };

/** Tools whose calls run host command programs and take part in activity projection. */
const FORM_PROGRAM_TOOLS = new Set(["command", "summarize", "survey_repo", "code_rewrite"]);

/** Wire phase base rank (mechanics §3.1); group sub-tiers offset 0..2 within the phase. */
const ACTIVITY_PHASE_RANK: Readonly<Record<string, number>> = { inspect: 10, execute: 20, mutate: 30 };
const ACTIVITY_GROUP_TIER: Readonly<Record<string, number>> = {
 "Processing output": 0, "Inspecting environment": 0, "Inspecting processes": 0, "Checking file metadata": 0,
 "Searching codebase": 1, "Reading files": 1, "Comparing files": 1, "Inspecting Git": 1, "Exploring project files": 1, "Inspecting version history": 1,
 "Mapping codebase": 2, "Summarizing source": 2,
 "Running commands": 0, "Controlling processes": 0, "Accessing the network": 0, "Syncing Git remote": 0,
 "Running programs": 1, "Following a running program": 1, "Running tests": 1,
 "Managing files": 0, "Archiving files": 0, "Managing Git branches": 0,
 "Editing files": 1, "Recording Git changes": 1, "Changing files": 1, "Recording changes": 1,
};

/**
 * Two-level monotonic rank: phase base plus the group sub-tier. Unknown groups
 * default to the inspect discovery tier and the execute/mutate base; unknown
 * phases rank 0 and never advance a headline.
 */
export function commandActivityRank(fact: CommandActivity): number {
 const base = ACTIVITY_PHASE_RANK[fact.phase ?? ""] ?? 0;
 if (base === 0) return 0;
 return base + (ACTIVITY_GROUP_TIER[fact.group] ?? (fact.phase === "inspect" ? 1 : 0));
}

function activityFacts(entries: readonly ActivitySpanEntry[]): CommandActivity[] {
  return entries.flatMap(entry => entry.kind === "tool" && entry.part.commandActivity ? [entry.part.commandActivity.fact] : []);
}

function status(fact: CommandActivity): ToolPartView["status"] {
  if (fact.state === "running" || fact.state === "waiting") return "running";
  if (fact.state === "failed" || fact.state === "refused" || fact.state === "interrupted" || (fact.status_code ?? 0) !== 0) return "error";
  return "completed";
}

/** Host identities determine membership; visible keys and presentation stay fixed. */
export function createCommandActivityProjection() {
  const assignments = new Map<string, Assignment>();
  const positions = new Map<string, number>();
  const outputAssignments = new Map<string, string>();
  const remembered = new Map<string, CommandProgramReceipt>();
  const callAssignments = new Map<string, CallAssignment>();
  let nextPosition = 0;
  return (entries: readonly ActivitySpanEntry[], live: (part: ToolPartView) => CommandProgramReceipt | undefined = () => undefined): ActivitySpanEntry[] => {
    const result: ActivitySpanEntry[] = [];
    const rows = new Map<string, ToolPartView>();
    let decisions: Extract<ActivitySpanEntry, { kind: "turn_load" }>[] = [];
    let lastRow: ToolPartView | undefined;
    for (const entry of entries) {
      if (entry.kind === "turn_load") { decisions.push(entry); lastRow = undefined; continue; }
      if (entry.kind !== "tool") { result.push(entry); continue; }
      if (!FORM_PROGRAM_TOOLS.has(entry.part.tool) || !entry.part.commandProgram) lastRow = undefined;
      const canonical = entry.part.commandProgram;
      const observed = live(entry.part);
      let program = observed && (!canonical || (observed.activity_revision ?? 0) >= (canonical.activity_revision ?? 0)) ? observed : canonical;
      const previous = remembered.get(entry.part.toolCallId);
      if (program && previous?.id === program.id && !program.activities && previous.activities?.length) {
        program = { ...program, activities: previous.activities.map(fact =>
          fact.state === "running" || fact.state === "waiting"
            ? { ...fact, state: "unknown" as const, outcome: "Outcome unavailable", status_code: undefined }
            : fact) };
      }
      program ??= previous;
      if (program) remembered.set(entry.part.toolCallId, program);
      let facts = program?.activities;
      if (program && facts?.length && ["failed", "rejected", "interrupted"].includes(program.state) && !facts.some(fact => status(fact) === "error")) {
        facts = [...facts, commandActivityPart({ ...entry.part, commandProgram: program }).commandActivity!.fact];
      }
      if (entry.part.tool === "command_output") {
        const handle = entry.part.process?.handle ?? (typeof entry.part.args?.process_id === "string" ? entry.part.args.process_id : undefined);
        let assigned = outputAssignments.get(entry.part.id);
        if (!assigned && handle) {
          assigned = [...rows.values()].find(row => row.commandActivity?.fact.job_handle === handle)?.id ?? entry.part.id;
          outputAssignments.set(entry.part.id, assigned);
        }
        const established = assigned ? rows.get(assigned) : undefined;
        if (established?.commandActivity) {
          established.commandActivity.calls.push(entry.part);
          established.commandActivity.outputReads = (established.commandActivity.outputReads ?? 0) + 1;
          if (entry.part.process?.handle === handle) established.process = entry.part.process;
          if (entry.part.status === "error") {
            established.status = "error";
            established.commandActivity.fact = { ...established.commandActivity.fact, outcome: "Output read failed" };
          }
          continue;
        }
        if (handle) {
          const fact: CommandActivity = { id: entry.part.id, group: "Following a running program", label: "Read process output", subject: handle, job_handle: handle, phase: "execute", state: entry.part.status === "running" ? "running" : entry.part.status === "error" ? "failed" : "completed", outcome: entry.part.status === "running" ? "Reading" : entry.part.status === "error" ? "Read failed" : "Completed", execution_ids: [], spans: [] };
          const row: ToolPartView = { ...entry.part, commandActivity: { fact, calls: [entry.part], members: [fact], programIds: entry.part.commandProgram ? [entry.part.commandProgram.id] : [], outputReads: 1 } };
          rows.set(row.id, row); result.push({ ...entry, part: row });
          continue;
        }
      }

      if (!program || !facts?.length) {
        if (program?.activities?.length === 0 || (FORM_PROGRAM_TOOLS.has(entry.part.tool) && entry.part.status === "running")) continue;
        result.push(entry); continue;
      }
      for (const [index, fact] of facts.entries()) {
        const identity = `${program.id}:${fact.id}`;
        if (fact.phase !== "inspect" || fact.job_handle) lastRow = undefined;
        const foldTarget = index === 0
          ? callAssignments.get(identity)?.row ?? (lastRow && lastRow.commandActivity?.fact.group === fact.group ? lastRow.id : undefined)
          : undefined;
        const target = foldTarget ? rows.get(foldTarget) : undefined;
        if (target?.commandActivity) {
          if (!assignments.has(identity)) assignments.set(identity, { program: program.id, key: target.id, label: fact.label, group: fact.group, subject: fact.subject });
          if (!callAssignments.has(identity)) callAssignments.set(identity, { row: target.id, program: program.id });
          const established = target.commandActivity;
          established.calls.push({ ...entry.part, commandProgram: program });
          established.members.push(fact);
          established.fact = { ...established.fact,
            steps: [...(established.fact.steps ?? []), ...(fact.steps ?? [])],
            execution_count: (established.fact.execution_count ?? 0) + (fact.execution_count ?? 0) };
          if (!established.programIds.includes(program.id)) established.programIds.push(program.id);
          if (status(fact) === "error") target.status = "error";
          continue;
        }
        let assigned = assignments.get(identity);
        if (!assigned) {
          assigned = { program: program.id, key: index === 0 && ![...assignments.values()].some(value => value.key === entry.part.id)
            ? entry.part.id : `${entry.part.id}:activity:${fact.id}`, label: fact.label, group: fact.group, subject: fact.subject };
          assignments.set(identity, assigned);
        }
        const frozen = { ...fact, label: assigned.label, group: assigned.group, subject: assigned.subject };
        const row: ToolPartView = { ...entry.part, id: assigned.key, title: frozen.subject, commandProgram: program,
          status: status(fact), process: entry.part.process?.handle === fact.job_handle ? entry.part.process : undefined,
          invocation: undefined, error: null,
          commandActivity: { fact: frozen, calls: [{ ...entry.part, commandProgram: program }], members: [fact], programIds: [program.id], decisions } };
        decisions = [];
        rows.set(row.id, row);
        result.push({ ...entry, part: row });
        if (fact.phase === "inspect" && !fact.job_handle) lastRow = row;
      }
    }
    const key = (entry: ActivitySpanEntry) => entry.kind === "tool" ? entry.part.id : entry.key;
    for (const entry of result) if (!positions.has(key(entry))) positions.set(key(entry), nextPosition++);
    result.sort((a, b) => {
      const left = positions.get(key(a));
      const right = positions.get(key(b));
      if (left === undefined || right === undefined) throw new Error("Activity row has no assigned position");
      return left - right;
    });
    const activeCalls = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.toolCallId] : []));
    const activePartIds = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.id] : []));
    for (const id of outputAssignments.keys()) if (!activePartIds.has(id)) outputAssignments.delete(id);
    for (const call of remembered.keys()) if (!activeCalls.has(call)) remembered.delete(call);
    const activePrograms = new Set([...remembered.values()].map(program => program.id));
    for (const [identity, assigned] of assignments) if (!activePrograms.has(assigned.program)) assignments.delete(identity);
    for (const [identity, assigned] of callAssignments) if (!activePrograms.has(assigned.program)) callAssignments.delete(identity);
    const activeRows = new Set(result.map(key));
    for (const row of positions.keys()) if (!activeRows.has(row)) positions.delete(row);
    return result;
  };
}

/** The highest present (phase, group) rank anchors the group title; entries only grow, so it never oscillates. */
export function commandActivityHeadline(entries: readonly ActivitySpanEntry[], fallback: string): string {
 const activities = activityFacts(entries);
 if (!activities.length) return fallback;
 let headline = activities[0]!.group;
 let rank = commandActivityRank(activities[0]!);
 for (const fact of activities) {
  const candidate = commandActivityRank(fact);
  if (candidate > rank) { headline = fact.group; rank = candidate; }
 }
 return headline;
}

export type CommandActivityProgressionChip = { group: string; steps: number };
export type CommandActivityProgression = { groups: string[]; chips: CommandActivityProgressionChip[] };

/** First-appearance group order with consecutive chip runs and their step counts. */
export function commandActivityProgression(entries: readonly ActivitySpanEntry[]): CommandActivityProgression {
  const groups: string[] = [];
  const chips: CommandActivityProgressionChip[] = [];
  for (const entry of entries) {
    if (entry.kind !== "tool") continue;
    const fact = entry.part.commandActivity?.fact;
    const key = fact ? fact.group : entry.part.tool.trim().toLowerCase() || "tool activity";
    const steps = fact?.steps?.length ?? 0;
    if (fact && !groups.includes(fact.group)) groups.push(fact.group);
    const last = chips[chips.length - 1];
    if (last?.group === key) last.steps += steps;
    else chips.push({ group: key, steps });
  }
  return { groups, chips };
}

export function formatCommandActivityProgression(progression: CommandActivityProgression): string {
  return progression.chips.map(chip => `${formatSentenceCase(chip.group)} · ${chip.steps} ${chip.steps === 1 ? "step" : "steps"}`).join(" · ");
}


/** Receipts without an activity snapshot use the same card and retain all operations. */
export function commandActivityPart(part: ToolPartView): ToolPartView {
 if (part.commandActivity || !part.commandProgram) return part;
 const program = part.commandProgram;
 const state = program.state === "rejected" ? "refused" : program.state;
 const outcome = state === "completed" && program.status_code !== undefined ? `Exited ${program.status_code}` : ({ running:"Running",waiting:"Waiting",refused:"Refused",failed:"Failed",interrupted:"Interrupted",completed:"Completed" }[state]);
 const fact: CommandActivity = { id:`${program.id}:result`, group:"Running commands", label:"Run command", subject:program.stop_code ?? part.title ?? "Recorded command", phase:"execute", state, outcome, status_code:program.status_code, execution_ids:[], spans:[] };
 return {...part,commandActivity:{fact,calls:[part],members:[fact],programIds:[program.id]}};
}
