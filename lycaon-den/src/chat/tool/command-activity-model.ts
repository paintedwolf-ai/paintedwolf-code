import type { CommandActivity, CommandExecutionReceipt, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";

export type CommandActivityView = {
  fact: CommandActivity;
  calls: ToolPartView[];
  members: CommandActivity[];
  continuation?: { cursor: string; remaining: number };
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
      const program = canonical && canonical.state !== "running" && canonical.state !== "waiting" ? canonical : live(entry.part) ?? remembered.get(entry.part.toolCallId) ?? canonical;
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
            status: status(fact), commandActivity: { fact: frozen, calls: [], members: [] } };
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
          const row: ToolPartView = { ...entry.part, id, commandProgram: program, commandActivity: { fact, calls: [{ ...entry.part, commandProgram: program }], members: [], continuation: { cursor: program.activities_cursor, remaining: program.activities_omitted } } };
          rows.set(id, row); result.push({ ...entry, part: row }); previous = undefined;
        }
      }
    }
    const key = (entry: ActivitySpanEntry) => entry.kind === "tool" ? entry.part.id : entry.key;
    for (const entry of result) if (!positions.has(key(entry))) positions.set(key(entry), nextPosition++);
    result.sort((a, b) => positions.get(key(a))! - positions.get(key(b))!);
    const activeCalls = new Set(entries.flatMap(entry => entry.kind === "tool" ? [entry.part.toolCallId] : []));
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
