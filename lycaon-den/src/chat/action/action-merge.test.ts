import { describe, expect, it } from "vitest";
import type { ActionDescriptor, ActionFact, CommandActivity } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import { mergeActions } from "./action-merge.ts";
import { foldActionEntries, partAction } from "./part-action.ts";

function action(over: Partial<ActionDescriptor> = {}): ActionDescriptor {
  return {
    label: "Run command", status: "done", ran: { text: "go test ./...", noun: "command" },
    facts: [{ kind: "result", outcome: "passed", exit_code: 0 }], row: ["result"], links: [{ kind: "input" }], ...over,
  };
}

function step(name: string, over: Partial<ActionDescriptor> = {}): ActionDescriptor {
  return action({ label: "Terminal session", ran: { text: "python3 game.py", noun: "command" }, fold_key: "terminal:t",
    facts: [{ kind: "steps", count: 1, unit: "actions", sequence: [name] }], row: ["steps"], ...over });
}

function part(id: string, descriptor?: ActionDescriptor): ToolPartView {
  return { id, toolCallId: id, assistantMessageId: "a", messageId: id, tool: "terminal_send", kind: "generic", status: "completed", action: descriptor };
}

const tool = (p: ToolPartView): ActivitySpanEntry => ({ kind: "tool", part: p, batchOrder: 0 }) as ActivitySpanEntry;

describe("mergeActions", () => {
  it("keeps a single descriptor as it is", () => {
    const only = action();
    expect(mergeActions([only])).toBe(only);
    expect(mergeActions([])).toBeUndefined();
  });

  it("names a session by its first call, ends it with the last, and keeps every step in order", () => {
    const merged = mergeActions([step("open"), step("send", { ran: { text: "1", noun: "command" } }), step("send"), step("close", { facts: [{ kind: "result", outcome: "completed" }, { kind: "steps", count: 1, unit: "actions", sequence: ["close"] }], row: ["result", "steps"] })])!;
    expect(merged.label).toBe("Terminal session");
    expect(merged.ran.text).toBe("python3 game.py");
    expect(merged.more).toBe(1);
    expect(merged.facts.map((fact) => fact.kind)).toEqual(["result", "steps"]);
    expect(merged.facts.find((fact) => fact.kind === "steps")?.sequence).toEqual(["open", "send", "send", "close"]);
  });

  it("counts folded calls and their failures, and gathers every receipt once", () => {
    const failing = action({ status: "error", facts: [{ kind: "result", outcome: "failed", exit_code: 1 }],
      links: [{ kind: "receipt", receipts: [{ program_id: "p", activity_id: "b" }] }],
      preview: { kind: "output", receipts: [{ program_id: "p", activity_id: "b" }] } });
    const first = action({ links: [{ kind: "receipt", receipts: [{ program_id: "p", activity_id: "a" }] }], preview: { kind: "output", receipts: [{ program_id: "p", activity_id: "a" }] } });
    const merged = mergeActions([first, failing, failing])!;
    expect(merged.status).toBe("error");
    expect(merged.facts.find((fact) => fact.kind === "steps")).toMatchObject({ count: 3, failed: 2, unit: "calls" });
    expect(merged.links).toEqual([{ kind: "receipt", receipts: [{ program_id: "p", activity_id: "a" }, { program_id: "p", activity_id: "b" }] }]);
    expect(merged.preview?.receipts).toHaveLength(2);
    expect(merged.facts.length).toBeLessThanOrEqual(4);
  });

  it("is running while any member runs", () => {
    expect(mergeActions([action({ status: "error" }), action({ status: "running" })])?.status).toBe("running");
  });
});

describe("folds and part actions", () => {
  it("folds adjacent calls that share a key, and stops at a different key or another entry", () => {
    const entries = [
      tool(part("1", step("open"))), tool(part("2", step("send"))),
      tool(part("3", action())),
      tool(part("4", step("read"))), tool(part("5", step("close", { fold_key: "terminal:other" }))),
    ];
    const folded = foldActionEntries(entries);
    expect(folded).toHaveLength(4);
    const first = folded[0] as Extract<ActivitySpanEntry, { kind: "tool" }>;
    expect(first.part.id).toBe("1");
    expect(first.part.foldMembers?.map((member) => member.id)).toEqual(["2"]);
    expect(partAction(first.part)?.facts.find((fact) => fact.kind === "steps")?.sequence).toEqual(["open", "send"]);
  });

  it("leaves calls without a fold key standing alone", () => {
    const entries = [tool(part("1", action())), tool(part("2", action()))];
    expect(foldActionEntries(entries)).toHaveLength(2);
  });

  it("merges a command row's member activities", () => {
    const row: ToolPartView = { ...part("row"), tool: "command", commandActivity: {
      fact: { id: "f", group: "g", label: "Run command", subject: "go", outcome: "", state: "completed", execution_ids: [], spans: [] },
      calls: [],
      members: [
        { id: "a", group: "g", label: "Run command", subject: "go", outcome: "", state: "completed", execution_ids: [], spans: [], action: action() },
        { id: "b", group: "g", label: "Run command", subject: "go", outcome: "", state: "failed", execution_ids: [], spans: [], action: action({ status: "error" }) },
      ],
      receiptScopes: [],
    } };
    expect(partAction(row)?.status).toBe("error");
    expect(partAction(row)?.facts.find((fact) => fact.kind === "steps")?.count).toBe(2);
  });

  it("keeps the recorded observation when a live activity supplies the descriptor", () => {
    const observation: ActionFact = {
      kind: "source_observation",
      source_coverage: [{ folder: "/shared", coverage: "not_observed", reason: "io_error" }],
    };
    const fact: CommandActivity = {
      id: "live", group: "Running commands", label: "Run command", subject: "go",
      outcome: "Passed", state: "completed", execution_ids: [], spans: [], action: action(),
    };
    const row: ToolPartView = { ...part("row"), tool: "command", commandActivity: {
      fact,
      calls: [{ ...part("call"), sourceObservation: observation }],
      members: [fact],
      receiptScopes: [],
    } };
    const view = partAction(row);
    expect(view?.row).toEqual(["result", "source_observation"]);
    expect(view?.facts).toContainEqual(expect.objectContaining(observation));
    expect(view?.facts).toContainEqual(fact.action!.facts[0]);
  });
});
