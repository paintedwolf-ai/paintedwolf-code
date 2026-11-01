import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";

export type CommandActivityView = {
  fact: CommandActivity;
  calls: ToolPartView[];
  members: CommandActivity[];
  outputReads?: number;
  decisions?: Extract<ActivitySpanEntry, { kind: "turn_load" }>[];
};

type Assignment = { program: string; key: string; label: string; group: string; subject: string };

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
  let nextPosition = 0;
  return (entries: readonly ActivitySpanEntry[], live: (part: ToolPartView) => CommandProgramReceipt | undefined = () => undefined): ActivitySpanEntry[] => {
    const result: ActivitySpanEntry[] = [];
    const rows = new Map<string, ToolPartView>();
    let decisions: Extract<ActivitySpanEntry, { kind: "turn_load" }>[] = [];
    for (const entry of entries) {
      if (entry.kind === "turn_load") { decisions.push(entry); continue; }
      if (entry.kind !== "tool") { result.push(entry); continue; }
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
          const fact: CommandActivity = { id: entry.part.id, group: "Following a running program", label: "Read process output", subject: handle, job_handle: handle, state: entry.part.status === "running" ? "running" : entry.part.status === "error" ? "failed" : "completed", outcome: entry.part.status === "running" ? "Reading" : entry.part.status === "error" ? "Read failed" : "Completed", execution_ids: [], spans: [] };
          const row: ToolPartView = { ...entry.part, commandActivity: { fact, calls: [entry.part], members: [fact], outputReads: 1 } };
          rows.set(row.id, row); result.push({ ...entry, part: row });
          continue;
        }
      }

      if (!program || !facts?.length) {
        if (program?.activities?.length === 0 || (entry.part.tool === "command" && entry.part.status === "running")) continue;
        result.push(entry); continue;
      }
      for (const [index, fact] of facts.entries()) {
        const identity = `${program.id}:${fact.id}`;
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
          commandActivity: { fact: frozen, calls: [{ ...entry.part, commandProgram: program }], members: [fact], decisions } };
        decisions = [];
        rows.set(row.id, row);
        result.push({ ...entry, part: row });
      }
    }
    const key = (entry: ActivitySpanEntry) => entry.kind === "tool" ? entry.part.id : entry.key;
    for (const entry of result) if (!positions.has(key(entry))) positions.set(key(entry), nextPosition++);
    result.sort((a, b) => (positions.get(key(a)) ?? 0) - (positions.get(key(b)) ?? 0));
    const activeCalls = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.toolCallId] : []));
    const activePartIds = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.id] : []));
    for (const id of outputAssignments.keys()) if (!activePartIds.has(id)) outputAssignments.delete(id);
    for (const call of remembered.keys()) if (!activeCalls.has(call)) remembered.delete(call);
    const activePrograms = new Set([...remembered.values()].map(program => program.id));
    for (const [identity, assigned] of assignments) if (!activePrograms.has(assigned.program)) assignments.delete(identity);
    const activeRows = new Set(result.map(key));
    for (const row of positions.keys()) if (!activeRows.has(row)) positions.delete(row);
    return result;
  };
}

/** The first activity anchors the group title. */
export function commandActivityHeadline(entries: readonly ActivitySpanEntry[], fallback: string): string {
 const activities = entries.flatMap(entry => entry.kind === "tool" && entry.part.commandActivity ? [entry.part.commandActivity.fact] : []);
 if (!activities.length) return fallback;
 return activities[0]!.group;
}


/** Receipts without an activity snapshot use the same card and retain all operations. */
export function commandActivityPart(part: ToolPartView): ToolPartView {
 if (part.commandActivity || !part.commandProgram) return part;
 const program = part.commandProgram;
 const state = program.state === "rejected" ? "refused" : program.state;
 const outcome = state === "completed" && program.status_code !== undefined ? `Exited ${program.status_code}` : ({ running:"Running",waiting:"Waiting",refused:"Refused",failed:"Failed",interrupted:"Interrupted",completed:"Completed" }[state]);
 const fact: CommandActivity = { id:`${program.id}:result`, group:"Running commands", label:"Run command", subject:program.stop_code ?? part.title ?? "Recorded command", state, outcome, status_code:program.status_code, execution_ids:[], spans:[] };
 return {...part,commandActivity:{fact,calls:[part],members:[fact]}};
}
