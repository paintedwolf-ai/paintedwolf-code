import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { Message, WorkflowRun } from "../../../api/types.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";

const srcDir = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

const run: WorkflowRun = {
  id: "run-1",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
  revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "work",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function row(id: string, patch: Partial<Message> = {}): Message {
  return {
    id,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content: `row ${id}`,
    workflow_run_id: run.id,
    created_at: "2026-01-01T00:00:00Z",
    ...patch,
  };
}

/** Transcript rendering is total for every reachable store state. */
describe("transcript render totality", () => {
  const states: Array<{ name: string; messages: Message[]; runs: WorkflowRun[]; active?: WorkflowRun }> = [
    { name: "empty session", messages: [], runs: [] },
    { name: "rows ahead of their run", messages: [row("m1"), row("m2")], runs: [] },
    { name: "rows with no run id at all", messages: [row("m1", { workflow_run_id: undefined })], runs: [] },
    { name: "rows with a blank run id", messages: [row("m1", { workflow_run_id: "   " })], runs: [] },
    { name: "run present, no active run", messages: [row("m1")], runs: [run] },
    { name: "run and active run present", messages: [row("m1")], runs: [run], active: run },
    { name: "active run absent from the run list", messages: [row("m1")], runs: [], active: run },
    { name: "row naming a run the list does not have", messages: [row("m1", { workflow_run_id: "run-unknown" })], runs: [run], active: run },
    { name: "mixed resolved and unresolved runs", messages: [row("m1"), row("m2", { workflow_run_id: "run-unknown" })], runs: [run], active: run },
    { name: "terminal run", messages: [row("m1")], runs: [{ ...run, status: "complete" }] },
  ];

  for (const state of states) {
    it(`builds blocks for: ${state.name}`, () => {
      const blocks = buildChatTranscriptBlocks(
        state.messages,
        state.runs,
        state.active,
      );

      // Dropping rows silently is the same blank transcript by another route.
      const rendered = blocks.flatMap((block) => block.items).length;
      expect(rendered >= state.messages.length).toBe(true);
    });
  }

  it("workflow-spans.ts contains no throw", () => {
    const source = readFileSync(
      join(srcDir, "chat/workflow/workflow-spans.ts"),
      "utf8",
    );
    expect(source).not.toMatch(/\bthrow\b/);
  });
});
