import { describe, expect, it } from "vitest";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { activitySpanSummary } from "./activity-span-summary.ts";
import type { ToolPartStatus } from "./tool-part-model.ts";

function tool(id: string, title: string, status: ToolPartStatus): ActivitySpanEntry {
  return {
    kind: "tool",
    part: {
      id,
      toolCallId: id,
      assistantMessageId: "assistant",
      messageId: id,
      tool: "command",
      kind: "command",
      title,
      status,
    },
  };
}

describe("activity span summary", () => {
  const label = "Running programs";
  const failure = tool("failed", "Probe failed", "error");
  const success = tool("passed", "Verification passed", "completed");

  it.each([false, true])("leads with the unresolved failure (verbose=%s)", (verboseMode) => {
    expect(activitySpanSummary([success, failure], { label, verboseMode })).toEqual({
      status: "error",
      failureCount: 1,
      hint: "Probe failed, +1",
    });
  });

  it("summarizes recovered work according to the current mode", () => {
    const entries = [
      failure,
      tool("other-failure", "Probe failed", "error"),
      success,
      tool("same-title", "Verification passed", "error"),
      tool("final", label, "completed"),
    ];
    expect(activitySpanSummary(entries, { label, verboseMode: false })).toEqual({
      status: "done",
      failureCount: 0,
      hint: "Verification passed, +1",
    });
    expect(activitySpanSummary(entries, { label, verboseMode: true })).toEqual({
      status: "done",
      failureCount: 3,
      hint: "Probe failed, +1",
    });
  });

  it("reports running work before and after its activity row arrives", () => {
    expect(activitySpanSummary([failure, success], { label, verboseMode: false, running: true })).toEqual({
      status: "running",
      failureCount: 0,
      hint: "Probe failed, +1",
    });
    const running = tool("running", "Verification in progress", "running");
    expect(activitySpanSummary([running, failure, success], { label, verboseMode: true })).toEqual({
      status: "running",
      failureCount: 1,
      hint: "Probe failed, +2",
    });
  });
});
