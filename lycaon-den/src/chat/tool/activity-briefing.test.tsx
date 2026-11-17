// @vitest-environment jsdom
import { render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BriefingSnapshot } from "../../api/types.ts";
import { briefingCopy } from "../../briefing/briefing-copy.ts";
import {
  applyBriefingEvent,
  resetBriefingLiveForTests,
  setBriefingClient,
} from "../../briefing/briefing-live.ts";
import { clearSummaryFades } from "../../briefing/summary-fade.ts";
import { activityBriefingTarget, useActivityBriefingSlots } from "./activity-briefing.tsx";
import type { ToolPartView } from "./tool-part-model.ts";

vi.mock("../../settings/editor/file-summary-settings.ts", () => ({
  fileSummariesEnabled: () => true,
  fileSummariesSettingKnown: () => true,
}));

vi.mock("../transcript/presentation/transcript-disclosure.tsx", () => ({
  useTranscriptDisclosure: () => ({ userOpen: () => true }),
}));

function commandPart(state: string): ToolPartView {
  return {
    id: "row",
    status: state === "running" ? "running" : "completed",
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

function snapshot(over: Partial<BriefingSnapshot>, summary: Record<string, string> = {}): BriefingSnapshot {
  return {
    profile_id: "tool_activity",
    profile_version: "2",
    target_key: "k",
    scope_fingerprint: "k",
    target: {},
    cache_epoch: "1",
    revision: "1",
    status: "complete",
    pending_sections: [],
    outcome: { state: "unknown" },
    error_code: "",
    error_message: "",
    result: Object.keys(summary).length
      ? {
          evidence_ref: { project_id: "p", record_id: "rec" },
          summary: Object.entries(summary).map(([section_id, text]) => ({
            section_id,
            text,
            manifest_hash: section_id,
            source: "summarizer",
            reused: false,
            provider_id: "",
            model: "",
            evidence_quality: [],
            created_at: "",
          })),
        }
      : undefined,
    ...over,
  };
}

afterEach(() => {
  resetBriefingLiveForTests();
  clearSummaryFades();
});

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

describe("useActivityBriefingSlots", () => {
  it("renders waiting while pending, missing on empty or failed settlement, and ready text with evidence", async () => {
    let nextReply = snapshot({ target_key: "k1", status: "pending", pending_sections: ["purpose", "change"] });
    setBriefingClient(() => ({
      requestBriefing: async () => nextReply,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));

    const [part, setPart] = createSignal(commandPart("running"));
    const view = render(() => {
      const slots = useActivityBriefingSlots({ part, sessionId: () => "s", projectId: () => "p" });
      return (
        <div>
          {slots.summary}
          {slots.evidence}
        </div>
      );
    });

    await waitFor(() => expect(view.container.textContent).toContain(briefingCopy.waiting));

    applyBriefingEvent("p", {
      kind: "snapshot",
      snapshot: snapshot({ target_key: "k1", status: "pending", pending_sections: ["change"] }, { purpose: "Runs the suite." }),
    });
    await waitFor(() => expect(view.container.textContent).toContain("Runs the suite."));

    nextReply = snapshot({ target_key: "k2", status: "complete" }, { outcome: "Completed" });
    setPart(commandPart("completed"));
    await waitFor(() => expect(view.container.textContent).toContain(briefingCopy.notArrived));
    expect(view.container.textContent).toContain("Runs the suite.");

    applyBriefingEvent("p", {
      kind: "snapshot",
      snapshot: snapshot({ target_key: "k2", status: "complete" }, { purpose: "Runs the suite.", change: "All passed." }),
    });
    await waitFor(() => expect(view.container.textContent).toContain("All passed."));
    applyBriefingEvent("p", {
      kind: "snapshot",
      snapshot: snapshot({ target_key: "k2", status: "pending", pending_sections: ["purpose"] }, { change: "Outcome arrived first." }),
    });
    await waitFor(() => expect(view.container.textContent).toContain("Outcome arrived first."));
    expect(view.container.querySelector('[data-section="intent"]')?.textContent).toContain("Intent: Runs the suite.");
    expect(view.container.querySelector('[data-section="outcome"]')?.textContent).toContain("Outcome: Outcome arrived first.");

  });
});
