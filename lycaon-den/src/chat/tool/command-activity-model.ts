import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import { activitySpanEntryKey, type ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { addReceiptScope, commandActivityStatus, programActivityFacts, receiptScope, resolveCommandActivityProgram } from "./command-activity-facts.ts";
import { createCommandActivityMemory, type ActivityCallMemory, type CommandActivityMemory } from "./command-activity-memory.ts";

export const FORM_PROGRAM_TOOLS: ReadonlySet<string> = new Set(["command", "summarize", "survey_repo", "code_rewrite"]);

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

/** Activity groups in first-appearance order. */
export function commandActivityGroups(entries: readonly ActivitySpanEntry[]): string[] {
  return [...new Set(activityFacts(entries).map(fact => fact.group))];
}

type LiveProgram = (part: ToolPartView) => CommandProgramReceipt | undefined;

export function createCommandActivityProjection(memory = createCommandActivityMemory()) {
  return (entries: readonly ActivitySpanEntry[], live: LiveProgram = () => undefined): ActivitySpanEntry[] => {
    const result: { entry: ActivitySpanEntry; key: string; sourceKey: string }[] = [];
    const rows = new Map<string, ToolPartView>();
    const jobs = new Map<string, ToolPartView>();
    const ownActivityRows = new Map<string, ToolPartView>();
    const held = new Set<string>();
    const decisions = placeDecisions(entries, memory);
    let lastRow: ToolPartView | undefined;

    const render = (entry: ActivitySpanEntry, sourceKey = activitySpanEntryKey(entry), row?: ToolPartView) => {
      result.push({ entry, key: activitySpanEntryKey(entry), sourceKey });
      if (!row?.commandActivity) return;
      rows.set(row.id, row);
      if (!ownActivityRows.has(sourceKey)) ownActivityRows.set(sourceKey, row);
      const handle = row.commandActivity.fact.job_handle;
      if (handle && !jobs.has(handle)) jobs.set(handle, row);
    };

    for (const entry of entries) {
      if (entry.kind !== "tool") {
        if (entry.kind === "turn_load") lastRow = undefined;
        render(entry);
        continue;
      }
      const part = entry.part;
      const state = memory.call(part.id);
      const programTool = FORM_PROGRAM_TOOLS.has(part.tool);
      if (!programTool) lastRow = undefined;

      if (part.tool === "command_output") {
        const output = projectOutputRead(part, state, memory.hasRow(part.id), rows, jobs);
        if (output !== undefined) {
          if (output) render({ ...entry, part: output }, part.id, output);
          continue;
        }
      }

      const program = resolveCommandActivityProgram(part, live(part), state.program);
      if (program?.activities?.length) state.program = program;
      const summaryKey = program ? activityIdentity(program.id, `${program.id}:result`) : undefined;
      const facts = program && programActivityFacts(part, program, summaryKey !== undefined && state.activities.has(summaryKey));
      if (!program || !facts?.length) {
        lastRow = undefined;
        if (!memory.hasRow(part.id) && (facts?.length === 0 || (programTool && part.status === "running"))) {
          held.add(part.id);
        } else {
          render(entry);
        }
        continue;
      }

      for (const [index, fact] of facts.entries()) {
        const identity = activityIdentity(program.id, fact.id);
        const membership = state.activities.get(identity);
        const canLead = programTool && fact.phase === "inspect" && !fact.job_handle;
        if (!canLead) lastRow = undefined;
        const preceding = index === 0 && lastRow?.commandActivity?.fact.group === fact.group ? lastRow : undefined;
        const target = membership?.folded ? rows.get(membership.rowKey)
          : !membership && !memory.hasRow(part.id) ? preceding : undefined;

        if (target?.commandActivity) {
          if (!membership) state.activities.set(identity, { folded: true, rowKey: target.id, presentation: activityPresentation(fact) });
          addActivityMember(target.commandActivity, { ...part, commandProgram: program }, program, fact);
          if (lastRow !== target) lastRow = undefined;
          continue;
        }

        const primary = index === 0 && !state.primaryActivity;
        const key = membership && !membership.folded ? membership.rowKey
          : primary ? part.id : `${part.id}:activity:${fact.id}`;
        if (primary && key === part.id) state.primaryActivity = identity;
        const presentation = membership?.presentation ?? activityPresentation(fact);
        state.activities.set(identity, { folded: false, rowKey: key, presentation });
        const row = activityRow(part, program, key, fact, presentation);
        render({ ...entry, part: row }, part.id, row);
        if (canLead) lastRow = row;
      }
    }

    for (const row of rows.values()) {
      const activity = row.commandActivity!;
      row.status = commandActivityStatus(activity);
      if (activity.outputReadFailed) activity.fact = { ...activity.fact, outcome: "Output read failed" };
    }
    return memory.orderRows(result.filter(({ entry }) => {
      if (entry.kind !== "turn_load") return true;
      const placement = decisions.get(entry.key)!;
      if (!placement.target) return true;
      const activity = ownActivityRows.get(placement.target)?.commandActivity;
      if (activity) {
        (activity.decisions ??= []).push(entry);
        return false;
      }
      if (held.has(placement.target)) return false;
      memory.showDecisionOnItsOwn(entry.key, placement);
      return true;
    }));
  };
}

function activityIdentity(program: string, fact: string): string {
  return `${program}\0${fact}`;
}

function activityPresentation(fact: CommandActivity) {
  return { label: fact.label, group: fact.group, subject: fact.subject };
}

function placeDecisions(entries: readonly ActivitySpanEntry[], memory: CommandActivityMemory) {
  let nextTool: ToolPartView | undefined;
  const decisions = new Map<string, ReturnType<CommandActivityMemory["decision"]>>();
  for (let index = entries.length - 1; index >= 0; index--) {
    const entry = entries[index]!;
    if (entry.kind === "tool") nextTool = entry.part;
    if (entry.kind === "turn_load") {
      const target = nextTool && FORM_PROGRAM_TOOLS.has(nextTool.tool) ? nextTool.id : undefined;
      decisions.set(entry.key, memory.decision(entry.key, target));
    }
  }
  return decisions;
}

function activityRow(
  part: ToolPartView,
  program: CommandProgramReceipt,
  key: string,
  fact: CommandActivity,
  presentation: ReturnType<typeof activityPresentation>,
): ToolPartView {
  return {
    ...part, id: key, title: presentation.subject, commandProgram: program,
    process: part.process?.handle === fact.job_handle ? part.process : undefined,
    invocation: undefined, error: null,
    commandActivity: {
      fact: { ...fact, ...presentation },
      calls: [{ ...part, commandProgram: program }],
      members: [fact],
      receiptScopes: [receiptScope(program.id, fact)],
    },
  };
}

function addActivityMember(
  activity: NonNullable<ToolPartView["commandActivity"]>,
  call: ToolPartView,
  program: CommandProgramReceipt,
  fact: CommandActivity,
): void {
  activity.calls.push(call);
  activity.members.push(fact);
  activity.fact = {
    ...activity.fact,
    steps: [...(activity.fact.steps ?? []), ...(fact.steps ?? [])],
    execution_count: (activity.fact.execution_count ?? 0) + (fact.execution_count ?? 0),
  };
  addReceiptScope(activity.receiptScopes, receiptScope(program.id, fact));
}

// Undefined leaves the caller to render a raw row. Null marks an attached read.
function projectOutputRead(
  part: ToolPartView,
  state: ActivityCallMemory,
  hasOwnRow: boolean,
  rows: ReadonlyMap<string, ToolPartView>,
  jobs: ReadonlyMap<string, ToolPartView>,
): ToolPartView | null | undefined {
  const handle = part.process?.handle
    ?? (typeof part.args?.process_id === "string" ? part.args.process_id : state.output?.handle);
  if (!handle) return undefined;
  const remembered = state.output?.handle === handle ? state.output : undefined;
  const target = remembered ? rows.get(remembered.rowKey) : !hasOwnRow ? jobs.get(handle) : undefined;
  const activity = target?.commandActivity;
  const scopes = part.commandProgram ? [receiptScope(part.commandProgram.id)] : [];
  if (target && activity) {
    state.output = { handle, rowKey: target.id };
    activity.calls.push(part);
    for (const scope of scopes) addReceiptScope(activity.receiptScopes, scope);
    activity.outputReads = (activity.outputReads ?? 0) + 1;
    if (part.process?.handle === handle) target.process = part.process;
    if (part.status === "error") activity.outputReadFailed = true;
    return null;
  }

  state.output = { handle, rowKey: part.id };
  const fact: CommandActivity = {
    id: part.id, group: "Following a running program", label: "Read process output",
    subject: handle, job_handle: handle, phase: "execute",
    state: part.status === "error" ? "failed" : part.status,
    outcome: { running: "Reading", error: "Read failed", completed: "Completed" }[part.status],
    execution_ids: [], spans: [],
  };
  return {
    ...part,
    commandActivity: { fact, calls: [part], members: [fact], receiptScopes: scopes, outputReads: 1 },
  };
}
