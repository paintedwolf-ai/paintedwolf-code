import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandActivityStep, CommandProgramReceipt } from "../../api/types.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { commandActivityHeadline, commandActivityProgression, createCommandActivityProjection } from "./command-activity-model.ts";

function fact(id: string, overrides: Partial<CommandActivity> = {}): CommandActivity {
  return { id, group: "Exploring project files", phase: "inspect", label: "Read source", subject: "source.rs", state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: [id], spans: [], ...overrides };
}
function step(id: string): CommandActivityStep {
  return { id, command: `run ${id}`, count: 1, running: 0, waiting: 0, failed: 0, refused: 0, interrupted: 0, state: "completed", outcome: "Exited 0" };
}
function entry(id: string, facts?: CommandActivity[]): ActivitySpanEntry {
  const program = facts ? { id: `program:${id}`, activities: facts, state: "completed", source_ref: "source" } as CommandProgramReceipt : undefined;
  return { kind: "tool", part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: "result", tool: "command", kind: "command", status: "completed", commandProgram: program } };
}
function decision(key: string): ActivitySpanEntry {
  return { kind: "turn_load", key, row: { key, role: "tools", assistantMessageId: "assistant", anchorMessageId: "result", sub: 8, load: {} } as never };
}
function parts(entries: ActivitySpanEntry[]): ToolPartView[] { return entries.flatMap(entry => entry.kind === "tool" ? [entry.part] : []); }

describe("command activity composition", () => {
 it("splits one call without changing the first row identity", () => {
  const project = createCommandActivityProjection();
  const rows = parts(project([entry("call", [fact("build"), fact("test")])]));
  expect(rows.map(row => row.id)).toEqual(["call", "call:activity:test"]);
 });
 it("coalesces consecutive read-only calls into the established row", () => {
  const project = createCommandActivityProjection();
  const first = entry("a", [fact("read-a")]);
  const before = parts(project([first]));
  const after = parts(project([first, entry("b", [fact("read-b")])]));
  expect(after.map(row => row.id)).toEqual([before[0]!.id]);
  expect(after[0]!.commandActivity!.calls.map(call => call.toolCallId)).toEqual(["a", "b"]);
  expect(after[0]!.commandActivity!.members.map(member => member.id)).toEqual(["read-a", "read-b"]);
  expect(after[0]!.commandActivity!.programIds).toEqual(["program:a", "program:b"]);
 });
 it("keeps coalescing idempotent across re-projections", () => {
  const project = createCommandActivityProjection();
  const entries = [entry("a", [fact("a", { execution_count: 2, steps: [step("s1")] })]), entry("b", [fact("b", { execution_count: 3, steps: [step("s2")] })])];
  const once = parts(project(entries));
  const twice = parts(project(entries));
  expect(twice.map(row => row.id)).toEqual(once.map(row => row.id));
  expect(twice[0]!.commandActivity!.fact.steps?.map(item => item.id)).toEqual(["s1", "s2"]);
  expect(twice[0]!.commandActivity!.fact.execution_count).toBe(5);
 });
 it("keeps a coalesced call after it fails and rolls the row to error", () => {
  const project = createCommandActivityProjection();
  const first = entry("a", [fact("a")]);
  project([first]);
  const failed = entry("b", [fact("b", { state: "failed", outcome: "Exited 1", status_code: 1 })]);
  const rows = parts(project([first, failed]));
  expect(rows).toHaveLength(1);
  expect(rows[0]!.status).toBe("error");
  expect(rows[0]!.commandActivity!.calls.map(call => call.toolCallId)).toEqual(["a", "b"]);
 });
 it("updates repeated work in one row without retaining individual executions", () => {
  const project = createCommandActivityProjection();
  const before = parts(project([entry("a", [fact("find", { execution_count: 1 })])]));
  const after = parts(project([entry("a", [fact("find", { execution_count: 10705, failed_count: 1 })])]));
  expect(after.map(row => row.id)).toEqual(before.map(row => row.id));
  expect(after[0]!.commandActivity!.fact.execution_count).toBe(10705);
  expect(after[0]!.commandActivity!.fact.failed_count).toBe(1);
 });
 it("keeps labels, targets and positions stable while outcomes change", () => {
  const project = createCommandActivityProjection();
  project([entry("a", [fact("a", { state: "running", outcome: "Running" })])]);
  const [row] = parts(project([entry("a", [fact("a", { label: "Different", subject: "different", state: "failed", outcome: "Failed" })])]));
  expect(row!.commandActivity!.fact.label).toBe("Read source");
  expect(row!.title).toBe("source.rs");
  expect(row!.status).toBe("error");
 });
 it("does not fold across a mutation or other transcript boundary", () => {
  const project = createCommandActivityProjection();
  const rows = project([entry("a", [fact("a", {})]), entry("edit", [fact("edit", { label: "Change files", phase: "mutate" })]), entry("b", [fact("b", {})])]);
  expect(rows).toHaveLength(3);
 });
 it("starts a new row below a mutation when a later call inspects again", () => {
  const project = createCommandActivityProjection();
  const rows = parts(project([
   entry("a", [fact("a")]),
   entry("b", [fact("b", { group: "Changing files", label: "Change files", phase: "mutate" })]),
   entry("c", [fact("c")]),
  ]));
  expect(rows.map(row => row.id)).toEqual(["a", "b", "c"]);
 });
 it("does not fold an inspect call into a background job launch", () => {
  const project = createCommandActivityProjection();
  const rows = parts(project([entry("launch", [fact("job", { label: "Run process", job_handle: "job-1" })]), entry("a", [fact("a")])]));
  expect(rows.map(row => row.id)).toEqual(["launch", "a"]);
 });
 it("does not coalesce across a decision row", () => {
  const project = createCommandActivityProjection();
  const rows = parts(project([entry("a", [fact("a")]), decision("d"), entry("b", [fact("b")])]));
  expect(rows.map(row => row.id)).toEqual(["a", "b"]);
  expect(rows[1]!.commandActivity!.decisions?.length).toBe(1);
 });
 it("uses live facts before the canonical result arrives", () => {
  const project = createCommandActivityProjection();
  const row = entry("a");
  if (row.kind === "tool") row.part.status = "running";
  expect(project([row])).toHaveLength(0);
  const rows = parts(project([row], () => ({ id: "live", activities: [fact("live")], state: "running" }) as CommandProgramReceipt));
  expect(rows[0]!.id).toBe("a");
 });
});

it("appends late starts after visible rows instead of inserting above them", () => {
 const project = createCommandActivityProjection();
 const a = entry("a", [fact("a")]);
 const b = entry("b", [fact("b", { group: "Changing files", label: "Change files", phase: "mutate" })]);
 project([a,b]);
 const rows = parts(project([entry("a", [fact("a"),fact("late")]),b]));
 expect(rows.map(row=>row.id)).toEqual(["a","b","a:activity:late"]);
});

it("updates an established job activity with output reads", () => {
 const project = createCommandActivityProjection();
 const launch = entry("launch", [fact("process", { label: "Run process", job_handle: "job-1" })]);
 project([launch]);
 const read = entry("read");
 if (read.kind === "tool") { read.part.tool = "command_output"; read.part.args = { process_id: "job-1" }; }
 const rows = parts(project([launch, read]));
 expect(rows).toHaveLength(1);
 expect(rows[0]!.id).toBe("launch");
 expect(rows[0]!.commandActivity!.calls.map(call=>call.toolCallId)).toEqual(["launch","read"]);
 expect(rows[0]!.commandActivity!.outputReads).toBe(1);
});

it("folds output observations when the launch is outside the transcript window", () => {
 const project = createCommandActivityProjection();
 const reads = ["a","b"].map(id => {
  const row = entry(id);
  if (row.kind === "tool") { row.part.tool = "command_output"; row.part.args = { process_id: "job-1" }; }
  return row;
 });
 const rows = parts(project(reads));
 expect(rows).toHaveLength(1);
 expect(rows[0]!.commandActivity!.outputReads).toBe(2);
});

it("does not absorb an already visible output row when launch facts arrive later", () => {
 const project = createCommandActivityProjection();
 const read = entry("read");
 if (read.kind === "tool") { read.part.tool = "command_output"; read.part.args = { process_id: "job-1" }; }
 const launch = entry("launch");
 if (launch.kind === "tool") launch.part.status = "running";
 expect(parts(project([launch,read])).map(row=>row.id)).toEqual(["read"]);
 const rows=parts(project([entry("launch",[fact("job",{ job_handle:"job-1" })]),read]));
 expect(rows.map(row=>row.id)).toEqual(["read","launch"]);
});

it("retains live activity rows when the terminal receipt has no presentation snapshot", () => {
 const project=createCommandActivityProjection();
 project([entry("a",[fact("read",{state:"running",outcome:"Running"})])]);
 const terminal=entry("a");
 if(terminal.kind==="tool") terminal.part.commandProgram={id:"program:a",state:"completed"} as CommandProgramReceipt;
 const row=parts(project([terminal]))[0]!;
 expect(row.commandActivity!.fact.id).toBe("read");
 expect(row.status).toBe("completed");
});


it("advances the headline by phase and never downgrades", () => {
 const project = createCommandActivityProjection();
 const reads = entry("read", [fact("read")]);
 const readRows = project([reads]);
 expect(commandActivityHeadline(readRows, "Running commands")).toBe("Exploring project files");
 const build = entry("build", [fact("build", { group: "Running programs", phase: "execute", state: "running" })]);
 const rows = project([reads, build]);
 expect(rows).toHaveLength(2);
 expect(commandActivityHeadline(rows, "Running commands")).toBe("Running programs");
 expect(commandActivityHeadline(readRows, "Running commands")).toBe("Exploring project files");
 expect(commandActivityHeadline([], "Inspecting changes")).toBe("Inspecting changes");
});

it("orders the progression by first appearance and counts steps per chip run", () => {
 const project = createCommandActivityProjection();
 const entries = [
  entry("a", [fact("a", { steps: [step("s1"), step("s2")] })]),
  entry("b", [fact("b", { steps: [step("s3")] })]),
  entry("c", [fact("c", { group: "Changing files", label: "Change files", phase: "mutate" })]),
 ];
 const progression = commandActivityProgression(project(entries));
 expect(progression.groups).toEqual(["Exploring project files", "Changing files"]);
 expect(progression.chips).toEqual([
  { group: "Exploring project files", steps: 3 },
  { group: "Changing files", steps: 0 },
 ]);
});

it("keeps a program-level failure visible after successful child activities", () => {
 const project=createCommandActivityProjection();
 const before=entry("call",[fact("read")]);
 project([before]);
 const failed=entry("call",[fact("read")]);
 if(failed.kind==="tool") failed.part.commandProgram={...failed.part.commandProgram!,state:"failed",stop_code:"PROGRAM_TRACE_LIMIT"};
 const rows=parts(project([failed]));
 expect(rows.map(row=>row.id)).toEqual(["call","call:activity:program:call:result"]);
 expect(rows[0]!.status).toBe("completed");
 expect(rows[1]!.status).toBe("error");
 expect(rows[1]!.commandActivity!.fact.subject).toBe("PROGRAM_TRACE_LIMIT");
});
