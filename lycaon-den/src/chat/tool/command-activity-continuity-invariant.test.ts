import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import { activityDecision, activityFact, activityProgram, activityTool, outputRead } from "../../test/command-activity-fixtures.ts";
import { partAction } from "../action/part-action.ts";
import { activitySpanEntryKey, type ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { commandActivityMemoryFor } from "./command-activity-memory.ts";
import { createCommandActivityProjection, FORM_PROGRAM_TOOLS } from "./command-activity-model.ts";

const keys = (rows: readonly ActivitySpanEntry[]) => rows.map(activitySpanEntryKey);
const parts = (rows: readonly ActivitySpanEntry[]) => rows.flatMap(row => row.kind === "tool" ? [row.part] : []);
const settled = (id: string, tool: string, facts: CommandActivity[]) =>
  activityTool(id, tool, { commandProgram: activityProgram(id, { activities: facts }) });
const pending = (id: string, tool: string) => activityTool(id, tool, { status: "running" });
const membership = (rows: readonly ActivitySpanEntry[]) =>
  parts(rows).map(row => [row.id, row.commandActivity?.calls.map(call => call.id) ?? [row.id]]);
const decisionPlacement = (rows: readonly ActivitySpanEntry[]) => rows.flatMap(row =>
  row.kind === "turn_load" ? [[row.key, row.key]] : row.kind === "tool"
    ? (row.part.commandActivity?.decisions ?? []).map(decision => [decision.key, row.part.id]) : []);

function assertKeepsRows(before: readonly ActivitySpanEntry[], after: readonly ActivitySpanEntry[]) {
  const retained = new Set(keys(before));
  expect(keys(after).filter(key => retained.has(key))).toEqual(keys(before));
}

const arrivals = [
  [0, 1, 2], [0, 2, 1], [1, 0, 2], [1, 2, 0], [2, 0, 1], [2, 1, 0],
] as const;

let sequence = 0;
const freshScope = () => `activity-continuity-${++sequence}`;
const freshProjector = () => createCommandActivityProjection(commandActivityMemoryFor(freshScope()));

describe.each([...FORM_PROGRAM_TOOLS])("%s activity continuity", tool => {
  const phases: CommandActivity["phase"][][][] = [
    [["inspect"], ["inspect"]],
    [["inspect"], ["mutate"], ["inspect"]],
    [["execute"], ["inspect"]],
    [["inspect", "execute"], ["inspect"]],
  ];

  it.each(phases.map(phaseSequence => ({ phaseSequence })))("retains all live rows across $phaseSequence", ({ phaseSequence }) => {
    const project = freshProjector();
    const durable: ActivitySpanEntry[] = [];
    let before: ActivitySpanEntry[] = [];
    for (const [index, activityPhases] of phaseSequence.entries()) {
      const id = `call-${index}`;
      const facts = activityPhases.map((phase, ordinal) => activityFact(`${id}-${ordinal}`, { phase }));
      const call = pending(id, tool);
      const reporting = (program: CommandProgramReceipt | undefined) => {
        const after = project([...durable, call], part => part.id === id ? program : undefined);
        assertKeepsRows(before, after);
        before = after;
      };
      reporting(undefined);
      reporting(activityProgram(id, { state: "waiting", activities: facts.slice(0, 1).map(fact => ({ ...fact, state: "waiting" })) }));
      reporting(activityProgram(id, { state: "running", activities: facts.map(fact => ({ ...fact, state: "running" })) }));
      reporting(undefined);
      const saved = settled(id, tool, facts);
      durable.push(saved);
      const snapshotPending = project(durable, part => part.id === id ? activityProgram(id, { activities: [] }) : undefined);
      assertKeepsRows(before, snapshotPending);
      const snapshotComplete = project(durable);
      assertKeepsRows(snapshotPending, snapshotComplete);
      before = snapshotComplete;
    }
    expect(membership(before)).toEqual(membership(createCommandActivityProjection()(durable)));
  });

  it.each(arrivals.map(arrival => ({ arrival })))("keeps out-of-order rows for $arrival", ({ arrival }) => {
    const project = freshProjector();
    const first = settled("first", tool, [activityFact("first")]);
    const batch = [0, 1, 2].map(index => pending(`batch-${index}`, tool));
    const facts = new Map<string, CommandProgramReceipt>();
    const entries = [first, ...batch];
    let before = project(entries);
    for (const index of arrival) {
      const id = batch[index]!.part.id;
      const previous = batch[index - 1]?.part.id;
      const mustStandAlone = previous !== undefined && !facts.has(previous);
      facts.set(id, activityProgram(id, { state: "running", activities: [activityFact(id, { state: "running" })] }));
      const after = project(entries, part => facts.get(part.id));
      assertKeepsRows(before, after);
      if (mustStandAlone) expect(keys(after)).toContain(id);
      before = after;
    }
    const durable = [first, ...batch.map(({ part }) => settled(part.id, tool, [activityFact(part.id)]))];
    const after = project(durable);
    expect(membership(after)).toEqual(membership(before));
  });

  it("treats a tool result without facts as a fold boundary", () => {
    const project = freshProjector();
    const left = settled("left", tool, [activityFact("left")]);
    const raw = activityTool("raw", tool, { status: "error" });
    const right = settled("right", tool, [activityFact("right")]);
    const before = project([left, pending("raw", tool), right]);
    expect(keys(before)).toEqual(["left", "right"]);
    const after = project([left, raw, right]);
    assertKeepsRows(before, after);
    expect(keys(after)).toContain("raw");
    expect(keys(createCommandActivityProjection()([left, raw, right]))).toEqual(["left", "raw", "right"]);
  });

  it.each(["program", "raw program", "other tool", "output", "tail"] as const)(
    "places a decision before %s independently of progress order",
    follower => {
      for (const arrival of arrivals) {
        const project = freshProjector();
        const decision = activityDecision("decision");
        const batch = [0, 1, 2].map(index => pending(`batch-${index}`, tool));
        const next = follower === "raw program" ? [activityTool("raw", tool, { status: "error" })]
          : follower === "other tool" ? [activityTool("read", "read")]
          : follower === "output" ? [outputRead("output")] : [];
        const entries = follower === "tail" ? [...batch, decision] : [decision, ...next, ...batch];
        const observed = new Map<string, CommandProgramReceipt>();
        let before = project(entries);
        for (const index of arrival) {
          const id = batch[index]!.part.id;
          observed.set(id, activityProgram(id, { state: "running", activities: [activityFact(id, { state: "running" })] }));
          const after = project(entries, part => observed.get(part.id));
          assertKeepsRows(before, after);
          before = after;
        }
        const durable = entries.map(entry => entry.kind === "tool" && observed.has(entry.part.id) ?
          settled(entry.part.id, tool, [activityFact(entry.part.id)]) : entry);
        const cold = createCommandActivityProjection()(durable);
        expect(decisionPlacement(project(durable))).toEqual(decisionPlacement(cold));
        expect(decisionPlacement(cold)).toEqual([["decision", follower === "program" ? "batch-0" : "decision"]]);
      }
    },
  );

  it("keeps row assignments across remounts and regrouping, until the scope is evicted", () => {
    const scope = freshScope();
    const projector = () => createCommandActivityProjection(commandActivityMemoryFor(scope));
    const project = projector();
    const a = settled("a", tool, [activityFact("a")]);
    const b = settled("b", tool, [activityFact("b")]);
    project([pending("a", tool), b]);
    const together = project([a, b]);
    expect(keys(together)).toEqual(["b", "a"]);
    expect(projector()([a, b])).toEqual(together);
    expect(keys(projector()([a]))).toEqual(["a"]);
    expect(keys(projector()([b]))).toEqual(["b"]);
    assertKeepsRows(together, projector()([activityTool("neighbour", "read"), a, b]));
    assertKeepsRows(together, projector()([a, b, activityTool("following", "read")]));

    for (let index = 0; index < 8; index++) commandActivityMemoryFor(freshScope());
    expect(projector()([a, b])).toEqual(createCommandActivityProjection()([a, b]));
  });

  it("preserves nearby rows when a split moves the original pin anchor", () => {
    const project = freshProjector();
    const a = settled("a", tool, [activityFact("a")]);
    const b = settled("b", tool, [activityFact("b", { phase: "mutate" })]);
    const c = settled("c", tool, [activityFact("c")]);
    expect(keys(project([pending("a", tool), b, c]))).toEqual(["b", "c"]);
    const together = project([a, b, c]);
    expect(keys(together)).toEqual(["b", "c", "a"]);
    expect(keys(project([c]))).toEqual(["c"]);
    expect(keys(project([a, b]))).toEqual(["b", "a"]);
    expect(keys(project([a, b, c]))).toEqual(keys(together));
  });

  it("evicts call membership, remembered facts, and positions together", () => {
    const project = freshProjector();
    const a = settled("a", tool, [activityFact("a")]);
    const b = settled("b", tool, [activityFact("b")]);
    project([pending("a", tool), b]);
    expect(keys(project([a, b]))).toEqual(["b", "a"]);
    for (let index = 0; index < 2048; index++) {
      const id = `other-${index}`;
      project([settled(id, tool, [activityFact(id)])]);
    }
    expect(project([a, b])).toEqual(createCommandActivityProjection()([a, b]));
  });

  it("starts independent rows when split spans lose their command or output fold target", () => {
    const project = freshProjector();
    const a = settled("a", tool, [activityFact("a")]);
    const b = settled("b", tool, [activityFact("b")]);
    const launch = settled("launch", tool, [activityFact("launch", { phase: "execute", job_handle: "job" })]);
    const read = outputRead("read");
    expect(keys(project([a, b, launch, read]))).toEqual(["a", "launch"]);
    const withoutHandle = project([a, b, launch, activityTool("read", "command_output")]);
    expect(keys(withoutHandle)).toEqual(["a", "launch"]);
    expect(parts(withoutHandle)[1]!.commandActivity!.calls.map(call => call.id)).toEqual(["launch", "read"]);
    const split = project([b, read]);
    expect(keys(split)).toEqual(["b", "read"]);
    const merged = project([a, b, launch, read]);
    assertKeepsRows(split, merged);
    expect(new Set(keys(merged))).toEqual(new Set(["a", "b", "launch", "read"]));
  });

  it("retains omitted activities while accepting status and details from partial receipts", () => {
    const project = freshProjector();
    const first = activityFact("first", { state: "running" });
    const second = activityFact("second", { state: "waiting" });
    const call = pending("call", tool);
    const before = project([call], () => activityProgram("call", { state: "running", activities: [first, second] }));
    const partial = project([settled("call", tool, [{ ...first, state: "completed", execution_count: 5 }])]);
    assertKeepsRows(before, partial);
    expect(parts(partial).map(row => row.commandActivity!.fact.state)).toEqual(["completed", "unknown"]);
    expect(parts(partial)[0]!.commandActivity!.fact.execution_count).toBe(5);
    expect(parts(partial)[1]!.commandActivity!.receiptScopes).toEqual([{ programId: "program-call", activityId: "second" }]);
    const complete = project([settled("call", tool, [
      { ...first, state: "completed" }, { ...second, state: "failed", execution_count: 2 },
    ])]);
    assertKeepsRows(partial, complete);
    expect(parts(complete).map(row => row.status)).toEqual(["completed", "error"]);
  });

  it("keeps running output reads and failed output reads visible in the merged action", () => {
    const project = freshProjector();
    const descriptor = { label: "Read files", status: "done", ran: { text: "Read source", noun: "command" }, facts: [], row: [], links: [] } as const;
    const launch = settled("launch", tool, [activityFact("launch", {
      phase: "execute", job_handle: "job", action: { ...descriptor, facts: [], row: [], links: [] },
    })]);
    const active = project([launch, outputRead("failed", "error"), outputRead("reading", "running")]);
    expect(keys(active)).toEqual(["launch"]);
    expect(parts(active)[0]!.status).toBe("running");
    expect(partAction(parts(active)[0]!)?.status).toBe("running");
    const ended = project([launch, outputRead("failed", "error"), outputRead("reading")]);
    expect(parts(ended)[0]!.commandActivity!.outputReadFailed).toBe(true);
    expect(parts(ended)[0]!.status).toBe("error");
    expect(partAction(parts(ended)[0]!)?.status).toBe("error");
  });

  it("derives running status from a folded activity while keeping the lead identity", () => {
    const project = freshProjector();
    const a = settled("a", tool, [activityFact("a", {
      action: { label: "Read files", status: "done", ran: { text: "Read source", noun: "command" }, facts: [], row: [], links: [] },
    })]);
    const live = project([a, pending("b", tool)], part => part.id === "b"
      ? activityProgram("b", { state: "waiting", activities: [activityFact("b", { state: "waiting" })] }) : undefined);
    expect(keys(live)).toEqual(["a"]);
    expect(parts(live)[0]!.status).toBe("running");
    expect(partAction(parts(live)[0]!)?.status).toBe("running");
    const ended = project([a, settled("b", tool, [activityFact("b")])]);
    expect(membership(ended)).toEqual(membership(live));
    expect(parts(ended)[0]!.status).toBe("completed");
    expect(partAction(parts(ended)[0]!)?.status).toBe("done");
  });
});

it("bounds decisions that have no retained call", () => {
  const project = freshProjector();
  const decision = activityDecision("old-decision");
  const call = pending("call", "command");
  expect(project([decision, call])).toEqual([]);
  const newer = Array.from({ length: 2048 }, (_, index) => activityDecision(`decision-${index}`));
  expect(keys(project(newer))).toEqual(keys(newer));
  const tail = activityDecision("tail");
  expect(keys(project([...newer, tail]))).toEqual([...keys(newer), tail.key]);
  expect(keys(project([decision]))).toEqual([decision.key]);
});
