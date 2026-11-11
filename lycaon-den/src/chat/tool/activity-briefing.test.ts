import { describe, expect, it } from "vitest";
import type { ToolPartView } from "./tool-part-model.ts";
import { activityBriefingTarget } from "./activity-briefing.tsx";

function commandPart(state: string): ToolPartView {
  return {
    id: "row",
    commandActivity: {
      fact: { state },
      calls: [
        { id: "call-1", commandProgram: { id: "prog-1" } },
        { id: "read-output" },
        { id: "call-2", commandProgram: { id: "prog-2" } },
      ],
      members: [
        { id: "act-1", execution_ids: ["e1"] },
        { id: "act-2", execution_ids: [] },
      ],
      programIds: ["prog-1", "prog-2"],
    },
  } as unknown as ToolPartView;
}

describe("activityBriefingTarget", () => {
  it("names program activities with their programs and every call", () => {
    expect(activityBriefingTarget(commandPart("completed"), "s")).toEqual({
      profileId: "tool_activity",
      ref: {
        session_id: "s",
        tool_call_ids: ["call-1", "read-output", "call-2"],
        members: [{ program_id: "prog-1", activity_id: "act-1" }],
        activity_revision: "completed",
      },
    });
  });

  it("gives a settled card a new target", () => {
    expect(activityBriefingTarget(commandPart("running"), "s")).not.toEqual(activityBriefingTarget(commandPart("completed"), "s"));
  });

  it("names a non-command call alone, and nothing without a session", () => {
    const part = { id: "call-9" } as ToolPartView;
    expect(activityBriefingTarget(part, "s")).toEqual({ profileId: "tool_activity", ref: { session_id: "s", tool_call_ids: ["call-9"] } });
    expect(activityBriefingTarget(part, undefined)).toBeNull();
  });
});
