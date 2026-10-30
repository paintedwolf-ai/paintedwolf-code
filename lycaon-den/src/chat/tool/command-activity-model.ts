import type { CommandActivity, CommandExecutionReceipt, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";

export type CommandActivityView = {
  fact: CommandActivity;
  calls: ToolPartView[];
  members: CommandActivity[];
  outputReads?: number;
  continuation?: { cursor: string };
};

type Assignment = { program: string; key: string; label: string; group: string; subject: string };

function status(fact: CommandActivity): ToolPartView["status"] {
  if (fact.state === "running" || fact.state === "waiting") return "running";
  if (fact.state === "failed" || fact.state === "refused" || fact.state === "interrupted" || (fact.status_code ?? 0) !== 0) return "error";
  return "completed";
}

/** Assignments are sealed when a fact first enters the visible projection. */
export function createCommandActivityProjection() {
  const assignments = new Map<string, Assignment>();
  const positions = new Map<string, number>();
  const outputAssignments = new Map<string, string>();
  const knownFacts = new Map<string, Map<string, CommandActivity>>();
  const remembered = new Map<string, CommandProgramReceipt>();
  let nextPosition = 0;
  return (entries: readonly ActivitySpanEntry[], live: (part: ToolPartView) => CommandProgramReceipt | undefined = () => undefined): ActivitySpanEntry[] => {
    const result: ActivitySpanEntry[] = [];
    const rows = new Map<string, ToolPartView>();
    let previous: ToolPartView | undefined;
    for (const entry of entries) {
      if (entry.kind !== "tool") { result.push(entry); previous = undefined; continue; }
      const canonical = entry.part.commandProgram;
      let program = canonical && canonical.state !== "running" && canonical.state !== "waiting" ? canonical : live(entry.part) ?? remembered.get(entry.part.toolCallId) ?? canonical;
      const retainedProgram = remembered.get(entry.part.toolCallId);
      if (program && !program.activities && retainedProgram?.id === program.id && retainedProgram.activities) {
        program = { ...program, activities: retainedProgram.activities, activity_executions: retainedProgram.activity_executions, activities_cursor: retainedProgram.activities_cursor, activities_omitted: retainedProgram.activities_omitted };
      }
      if (program) remembered.set(entry.part.toolCallId, program);
      let facts = program?.activities;
      if (program?.activities) {
        const retained = knownFacts.get(program.id) ?? new Map<string, CommandActivity>();
        const current = new Set(program.activities.map(fact => fact.id));
        for (const fact of program.activities) retained.set(fact.id, fact);
        for (const fact of retained.values()) {
          if (current.has(fact.id)) continue;
          const outcome = program.activity_executions?.find(execution => execution.id === fact.id);
          if (outcome) retained.set(fact.id, { ...fact, state: outcome.state, outcome: outcome.outcome, status_code: outcome.status_code });
        }
        knownFacts.set(program.id, retained);
        facts = [...retained.values()];
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
          rows.set(row.id, row); result.push({ ...entry, part: row }); previous = row;
          continue;
        }
      }
      if (!program || !facts?.length) {
        if (!(entry.part.tool === "command" && entry.part.status === "running")) result.push(entry);
        previous = undefined; continue;
      }
      for (const [index, fact] of facts.entries()) {
        const identity = `${program.id}:${fact.id}`;
        let assigned = assignments.get(identity);
        if (!assigned) {
          const prior = previous?.commandActivity;
          const joins = fact.merge_key && prior?.fact.merge_key === fact.merge_key;
          assigned = joins
            ? { program: program.id, key: previous!.id, label: prior!.fact.label, group: prior!.fact.group, subject: prior!.fact.subject }
            : { program: program.id, key: index === 0 ? entry.part.id : `${entry.part.id}:activity:${fact.id}`, label: fact.label, group: fact.group, subject: fact.subject };
          assignments.set(identity, assigned);
        }
        let row = rows.get(assigned.key);
        if (!row) {
          const frozen = { ...fact, label: assigned.label, group: assigned.group, subject: assigned.subject };
          row = { ...entry.part, id: assigned.key, title: frozen.subject, commandProgram: program,
            status: status(fact), process: entry.part.process?.handle === fact.job_handle ? entry.part.process : undefined, invocation: undefined, error: null, commandActivity: { fact: frozen, calls: [], members: [] } };
          rows.set(assigned.key, row);
          result.push({ ...entry, part: row });
        }
        const view = row.commandActivity!;
        if (!view.calls.some(call => call.id === entry.part.id)) view.calls.push({ ...entry.part, commandProgram: program });
        if (!view.members.some(member => member.id === fact.id)) view.members.push(fact);
        if (status(fact) === "error" || status(fact) === "running") {
          row.status = status(fact);
          view.fact = { ...view.fact, state: fact.state, outcome: fact.outcome, status_code: fact.status_code };
        }
        const failures = view.members.filter(member => status(member) === "error").length;
        const running = view.members.some(member => status(member) === "running");
        if (view.members.length > 1) {
          row.status = running ? "running" : failures ? "error" : "completed";
          view.fact = { ...view.fact, outcome: `${running ? "Running" : "Completed"}${failures ? ` · ${failures} failed` : ""}` };
        }
        previous = row;
      }
      if (program.activities_cursor && program.activities_omitted) {
        const id = `${entry.part.id}:activity:remaining`;
        if (!rows.has(id)) {
          const fact: CommandActivity = { id, group: "Working on the project", label: "More operations", subject: `${program.activities_omitted} additional execution records`, state: program.state === "running" || program.state === "waiting" ? program.state : "completed", outcome: "Recorded", execution_ids: [], spans: [] };
          const row: ToolPartView = { ...entry.part, id, commandProgram: program, commandActivity: { fact, calls: [{ ...entry.part, commandProgram: program }], members: [], continuation: { cursor: program.activities_cursor } } };
          rows.set(id, row); result.push({ ...entry, part: row }); previous = undefined;
        }
      }
    }
    const key = (entry: ActivitySpanEntry) => entry.kind === "tool" ? entry.part.id : entry.key;
    for (const entry of result) if (!positions.has(key(entry))) positions.set(key(entry), nextPosition++);
    result.sort((a, b) => positions.get(key(a))! - positions.get(key(b))!);
    const activeCalls = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.toolCallId] : []));
    const activePartIds = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.id] : []));
    for (const id of outputAssignments.keys()) if (!activePartIds.has(id)) outputAssignments.delete(id);
    for (const call of remembered.keys()) if (!activeCalls.has(call)) remembered.delete(call);
    const activePrograms = new Set([...remembered.values()].map(program => program.id));
    for (const program of knownFacts.keys()) if (!activePrograms.has(program)) knownFacts.delete(program);
    for (const [identity, assigned] of assignments) if (!activePrograms.has(assigned.program)) assignments.delete(identity);
    const activeRows = new Set(result.map(key));
    for (const row of positions.keys()) if (!activeRows.has(row)) positions.delete(row);
    return result;
  };
}

export function activityExecutions(view: CommandActivityView, executions: readonly CommandExecutionReceipt[]): CommandExecutionReceipt[] {
  const ids = new Set(view.members.flatMap(member => member.execution_ids));
  return executions.filter(execution => ids.has(execution.id));
}

export type CommandSourceSegment = { text: string; highlighted: boolean };

/** Parser byte coordinates apply only to the exact unredacted root source. */
export function commandSourceSegments(source: string, program: CommandProgramReceipt, fact: CommandActivity, redacted = false): CommandSourceSegment[] {
  if (redacted) return [{ text: source, highlighted: false }];
  const bytes = new TextEncoder().encode(source);
  const spans = fact.spans.filter(span => span.source_ref === program.source_ref && span.start_byte >= 0 && span.end_byte <= bytes.length && span.end_byte >= span.start_byte)
    .sort((a, b) => a.start_byte - b.start_byte);
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const result: CommandSourceSegment[] = [];
  let offset = 0;
  try {
    for (const span of spans) {
      const start = Math.max(offset, span.start_byte);
      if (span.end_byte <= offset) continue;
      if (start > offset) result.push({ text: decoder.decode(bytes.subarray(offset, start)), highlighted: false });
      result.push({ text: decoder.decode(bytes.subarray(start, span.end_byte)), highlighted: true });
      offset = span.end_byte;
    }
    if (offset < bytes.length) result.push({ text: decoder.decode(bytes.subarray(offset)), highlighted: false });
    return result;
  } catch { return [{ text: source, highlighted: false }]; }
}

/** The group follows active work without moving its activity rows. */
export function commandActivityHeadline(entries: readonly ActivitySpanEntry[], fallback: string): string {
 const activities = entries.flatMap(entry => entry.kind === "tool" && entry.part.commandActivity ? [entry.part.commandActivity.fact] : []);
 if (!activities.length) return fallback;
 const active = activities.filter(fact => fact.state === "running" || fact.state === "waiting");
 return (active.at(-1) ?? activities.at(-1))!.group;
}


/** Receipts without an activity snapshot use the same card and retain all operations. */
export function commandActivityPart(part: ToolPartView): ToolPartView {
 if (part.commandActivity || !part.commandProgram) return part;
 const program = part.commandProgram;
 const state = program.state === "rejected" ? "refused" : program.state;
 const outcome = state === "completed" && program.status_code !== undefined ? `Exited ${program.status_code}` : ({ running:"Running",waiting:"Waiting",refused:"Refused",failed:"Failed",interrupted:"Interrupted",completed:"Completed" }[state]);
 const fact: CommandActivity = { id:`${program.id}:result`, group:"Running commands", label:"Run command", subject:program.stop_code ?? part.title ?? "Recorded command", state, outcome, status_code:program.status_code, execution_ids:[], spans:[] };
 return {...part,commandActivity:{fact,calls:[part],members:[],continuation:{cursor:""}}};
}
