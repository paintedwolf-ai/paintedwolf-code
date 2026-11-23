import { describe, expect, it } from "vitest";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { activitySpanSummary } from "./activity-span-summary.ts";
import type { ToolPartStatus } from "./tool-part-model.ts";

function tools(...statuses: ToolPartStatus[]): ActivitySpanEntry[] {
  return statuses.map((status, index) => {
    const id = `call-${index}`;
    return {
      kind: "tool",
      part: {
        id,
        toolCallId: id,
        assistantMessageId: "assistant",
        messageId: id,
        tool: "command",
        kind: "command",
        status,
      },
    };
  });
}

describe("activity span summary", () => {
  it.each([false, true])("shows unresolved failures (verbose=%s)", (verboseMode) => {
    expect(activitySpanSummary(tools("error", "completed", "error"), { verboseMode })).toEqual({
      status: "error",
      failureCount: 2,
    });
  });

  it("shows previous failures on recovered spans in verbose mode", () => {
    const entries = tools("error", "error", "completed");
    expect(activitySpanSummary(entries, { verboseMode: false })).toEqual({
      status: "done",
      failureCount: 0,
    });
    expect(activitySpanSummary(entries, { verboseMode: true })).toEqual({
      status: "done",
      failureCount: 2,
    });
  });

  it("reports running work before and after its activity row arrives", () => {
    expect(activitySpanSummary(tools("error", "completed"), { verboseMode: false, running: true })).toEqual({
      status: "running",
      failureCount: 0,
    });
    expect(activitySpanSummary(tools("running", "error", "completed"), { verboseMode: true })).toEqual({
      status: "running",
      failureCount: 1,
    });
  });
});
