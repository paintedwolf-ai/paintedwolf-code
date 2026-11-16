// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { ToolPartView } from "./tool-part-model.ts";
import type { BriefingSnapshot } from "../../api/types.ts";
import { activityBriefingTarget, activitySummaryState } from "./activity-briefing.tsx";

function commandPart(state: string): ToolPartView {
  return {
    id: "row",
    commandActivity: {
      fact: { state },
      // Card ids pair the assistant row with the call; targets name host calls.
      calls: [
        { id: "row:call-1", toolCallId: "call-1", commandProgram: { id: "prog-1" } },
        { id: "row:read-output", toolCallId: "read-output" },
        { id: "row:call-2", toolCallId: "call-2", commandProgram: { id: "prog-2" } },
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
    const part = { id: "row:call-9", toolCallId: "call-9" } as ToolPartView;
    expect(activityBriefingTarget(part, "s")).toEqual({ profileId: "tool_activity", ref: { session_id: "s", tool_call_ids: ["call-9"] } });
    expect(activityBriefingTarget(part, undefined)).toBeNull();
  });
});

function snapshot(over: Partial<BriefingSnapshot>, summary: Record<string, string> = {}): BriefingSnapshot {
  return {
    profile_id: "tool_activity", profile_version: "2", target_key: "k", scope_fingerprint: "k", target: {},
    cache_epoch: "1", revision: "1", status: "complete", pending_sections: [], outcome: { state: "unknown" },
    error_code: "", error_message: "",
    result: Object.keys(summary).length ? {
      evidence_ref: { project_id: "p", record_id: "rec" },
      summary: Object.entries(summary).map(([section_id, text]) => ({ section_id, text, manifest_hash: section_id, source: "summarizer", reused: false, provider_id: "", model: "", evidence_quality: [], created_at: "" })),
    } : undefined,
    ...over,
  };
}

describe("activitySummaryState", () => {
  it("is pending with no answer, missing once the attempt ends without text, ready with text", () => {
    expect(activitySummaryState({ snapshot: null, stalled: false }, null).kind).toBe("pending");
    expect(activitySummaryState({ snapshot: snapshot({ status: "pending", pending_sections: ["purpose", "change"] }), stalled: false }, null).kind).toBe("pending");
    expect(activitySummaryState({ snapshot: snapshot({ status: "pending" }), stalled: true }, null).kind).toBe("missing");
    expect(activitySummaryState({ snapshot: snapshot({ status: "failed", error_code: "queue_expired" }), stalled: false }, null).kind).toBe("missing");
    expect(activitySummaryState({ snapshot: snapshot({}, { purpose: "Why.", change: "What." }), stalled: false }, null)).toEqual({
      kind: "ready", purpose: "Why.", change: "What.", recordId: "rec", changePending: false,
    });
  });

  it("treats a settled record holding only the outcome as a summary that did not arrive", () => {
    expect(activitySummaryState({ snapshot: snapshot({}, { outcome: "Completed" }), stalled: false }, null).kind).toBe("missing");
  });

  it("keeps the earlier purpose while the next revision is asked for", () => {
    const held = { purpose: "Runs the suite.", recordId: "old" };
    expect(activitySummaryState({ snapshot: null, stalled: false }, held)).toEqual({ kind: "pending", purpose: "Runs the suite." });
    expect(activitySummaryState({ snapshot: snapshot({ status: "pending", pending_sections: ["change"] }, { purpose: "Runs the suite." }), stalled: false }, held)).toEqual({
      kind: "ready", purpose: "Runs the suite.", change: undefined, recordId: "rec", changePending: true,
    });
  });
});
