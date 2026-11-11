import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandActivityStep, CommandExecutionOutput, CommandExecutionReceipt } from "../../api/types.ts";
import type { CommandActivityView } from "./command-activity-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { ACTIVITY_OUTPUT_LINES, activityExceptions, activityOutput, activityResultLine, activityStepsDiffer } from "./command-activity-details.ts";

function view(fact: Partial<CommandActivity>, calls: Partial<ToolPartView>[] = []): CommandActivityView {
  const base: CommandActivity = { id: "activity", group: "Group", phase: "inspect", label: "Label", subject: "subject", state: "completed", outcome: "Finished", execution_ids: ["execution"], spans: [] };
  return { fact: { ...base, ...fact }, members: [], calls: calls as ToolPartView[], programIds: [] };
}

function output(stream: "stdout" | "stderr", text: string, extra: Partial<CommandExecutionOutput> = {}): CommandExecutionOutput {
  return { stream, ref: `${stream}-ref`, size_bytes: text.length, complete: true, redacted: false, preview: text, preview_truncated: false, tail_preview: "", ...extra };
}

function execution(state: CommandExecutionReceipt["state"], outputs: CommandExecutionOutput[], extra: Partial<CommandExecutionReceipt> = {}): CommandExecutionReceipt {
  return { id: "execution", program_id: "program", node_id: "node", parent_execution_id: "", owner: "owner", lifecycle: "read_only", definition_digest: "d", arguments_digest: "a", arguments_ref: "a", command: "cmd", state, owner_started: true, owner_invoked: true, effect_certainty: "none", complete: true, publication_eligible: false, output_publishable: true, detail_ref: "detail", outputs, ...extra } as CommandExecutionReceipt;
}

const numbered = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => `line ${from + i}`);

describe("activity result line", () => {
  it("is absent for a clean success that adds nothing to the header", () => {
    expect(activityResultLine(view({ phase: "inspect" }), [])).toBeUndefined();
    expect(activityResultLine(view({ phase: "mutate", execution_count: 1 }), [])).toBeUndefined();
  });
  it("is absent while running or waiting", () => {
    for (const state of ["running", "waiting"] as const) expect(activityResultLine(view({ state }), [execution("completed", [output("stdout", "x", { line_count: 9 })])])).toBeUndefined();
  });
  it("carries the facts the receipts supply", () => {
    expect(activityResultLine(view({ phase: "inspect" }), [execution("completed", [output("stdout", "x", { line_count: 1234 })])])).toContain((1234).toLocaleString());
    expect(activityResultLine(view({ phase: "mutate", execution_count: 12 }), [])).toContain("12");
    expect(activityResultLine(view({ state: "failed", status_code: 7 }), [])).toContain("7");
    expect(activityResultLine(view({ state: "refused" }), [execution("refused", [], { stop_code: "SUPPLIED_STOP" })])).toContain("SUPPLIED_STOP");
    expect(activityResultLine(view({ phase: "execute", status_code: 3 }), [])).toContain("3");
  });
  it("distinguishes interruption before and after execution began", () => {
    const before = activityResultLine(view({ state: "interrupted" }), [execution("interrupted", [], { owner_started: false, owner_invoked: false })]);
    const after = activityResultLine(view({ state: "interrupted" }), [execution("interrupted", [], { owner_started: true })]);
    expect(before).toBeDefined();
    expect(after).toBeDefined();
    expect(before).not.toBe(after);
  });
  it("never reads command text or output prose", () => {
    const misleading = execution("completed", [output("stdout", "FAILED exit 1\nerror: everything broke")], { command: "rm -rf / && exit 1" });
    expect(activityResultLine(view({ phase: "mutate" }), [misleading])).toBeUndefined();
  });
});

describe("activity output", () => {
  it("shows the start of a successful output", () => {
    const lines = numbered(0, 20);
    const shown = activityOutput(view({}), [execution("completed", [output("stdout", lines.join("\n"), { line_count: 20 })])]);
    expect(shown?.tail).toBe(false);
    expect(shown?.lines).toEqual(lines.slice(0, ACTIVITY_OUTPUT_LINES));
    expect(shown?.hidden).toContain("20");
  });
  it("shows everything with no note when the output fits", () => {
    const lines = numbered(0, 2);
    expect(activityOutput(view({}), [execution("completed", [output("stdout", lines.join("\n"), { line_count: 2 })])])).toEqual({ stream: "stdout", lines, tail: false, hidden: undefined });
  });
  it("shows the stored tail of a failure, preferring stderr", () => {
    const tail = numbered(394, 400);
    const failing = execution("failed", [
      output("stdout", "progress", { size_bytes: 8 }),
      output("stderr", numbered(0, 50).join("\n"), { preview_truncated: true, size_bytes: 9000, line_count: 400, tail_preview: tail.join("\n") }),
    ], { status_code: 1 });
    const shown = activityOutput(view({ state: "failed" }), [failing]);
    expect(shown).toMatchObject({ stream: "stderr", lines: tail, tail: true });
    expect(shown?.hidden).toContain("400");
  });
  it("falls back to stdout when a failure wrote no stderr", () => {
    const shown = activityOutput(view({ state: "failed" }), [execution("failed", [output("stdout", numbered(0, 3).join("\n"))], { status_code: 1 })]);
    expect(shown).toMatchObject({ stream: "stdout", lines: numbered(0, 3), tail: true });
  });
  it("shows the host-written result of a program refused before any execution", () => {
    const result = numbered(0, 10).join("\n");
    const shown = activityOutput(view({ state: "refused", execution_ids: [] }, [{ output: result }]), []);
    expect(shown).toMatchObject({ stream: "result", lines: numbered(4, 10), tail: true });
  });
  it("is absent without output", () => {
    expect(activityOutput(view({}), [execution("completed", [])])).toBeUndefined();
  });
});

describe("activity steps", () => {
  const step = (state: string): CommandActivityStep => ({ id: state, command: state, count: 1, running: 0, waiting: 0, failed: 0, refused: 0, interrupted: 0, state, outcome: state });
  it("appear only when settled states differ", () => {
    expect(activityStepsDiffer(undefined)).toBe(false);
    expect(activityStepsDiffer([step("completed")])).toBe(false);
    expect(activityStepsDiffer([step("completed"), step("completed")])).toBe(false);
    expect(activityStepsDiffer([step("completed"), step("failed")])).toBe(true);
  });
});

describe("activity exceptions", () => {
  it("are empty for a clean success", () => {
    expect(activityExceptions(view({}), [execution("completed", [], { accepted_revision: "r1", published_revision: "r1" })])).toEqual([]);
  });
  it("carry each supplied non-default fact", () => {
    const row = execution("waiting", [], { command: "supplied-command", effect_certainty: "unknown", owner_started: true, owner_invoked: false, stop_code: "S1", host_stop_code: "S2", accepted_revision: "r2", held_version_id: "held-1", recovery_ref: "recovery-1", revision_conflict: { path: "file.rs", expected_revision: "r1", observed_revision: "r3" } });
    const facts = activityExceptions(view({ state: "waiting", job_handle: "job-1", directory: "/supplied" }), [row], [{ code: "FEEDBACK", details: { native_operation: "write-op" } }]);
    const values = facts.map(fact => fact.value);
    for (const supplied of ["S2", "held-1", "recovery-1", "file.rs", "r1", "r3", "job-1", "/supplied", "FEEDBACK", "write-op"]) expect(values).toContain(supplied);
    expect(facts.some(fact => fact.label === "supplied-command")).toBe(true);
  });
  it("count whole-activity failures beyond the loaded page", () => {
    const facts = activityExceptions(view({ execution_count: 10705, failed_count: 4, uncertain_effects: 2, completed_effects: 1 }), []);
    expect(facts.map(fact => fact.value)).toContain((4).toLocaleString());
    expect(facts.length).toBe(2);
  });
});
