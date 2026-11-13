import { render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { BriefingRecord } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import { LycaonApiError } from "../api/http.ts";
import { BriefingEvidencePage } from "./BriefingEvidencePage.tsx";
import { briefingCopy } from "./briefing-copy.ts";

const record: BriefingRecord = {
  record_id: "r".repeat(64), profile_id: "tool_activity", profile_version: "1", method: "mixed", created_at: "2026-11-11T10:00:00Z",
  sections: [
    {
      section_id: "purpose", manifest_hash: "a".repeat(64), available: true, template_id: "action_purpose", template_version: "2",
      result: { section_id: "purpose", text: "Ships the fix the user asked for.", manifest_hash: "a".repeat(64), source: "summarizer", reused: false,
        provider_id: "provider", model: "model", evidence_quality: [], created_at: "2026-11-11T10:00:00Z" },
      items: [
        { key: "user_intent", role: "goal", source_kind: "message", ref: "message:u1", inclusion: "included", text: "SUPPLIED_INTENT" },
        { key: "tracked_plan", role: "context", source_kind: "progress", inclusion: "unavailable", reason: "no immutable revision" },
      ],
    },
    { section_id: "change", manifest_hash: "b".repeat(64), available: false },
  ],
};

function client(get: BriefingsClient["getBriefingRecord"]): BriefingsClient {
  return { requestBriefing: async () => { throw new Error("unused"); }, getBriefingRecord: get };
}

describe("BriefingEvidencePage", () => {
  it("lays out summary, evidence, limits, sources and generation", async () => {
    render(() => <BriefingEvidencePage projectId="p" recordId={record.record_id} client={client(async () => record)} />);
    await waitFor(() => expect(screen.getByText("SUPPLIED_INTENT")).toBeTruthy());
    for (const heading of [briefingCopy.evidence.summary, briefingCopy.evidence.used, briefingCopy.evidence.coverage, briefingCopy.evidence.sources, briefingCopy.evidence.generation]) {
      expect(screen.getByRole("heading", { name: heading })).toBeTruthy();
    }
    expect(screen.getByText(/no immutable revision/)).toBeTruthy();
    expect(screen.getByText("message:u1")).toBeTruthy();
    expect(screen.getByText(briefingCopy.evidence.unavailableSection)).toBeTruthy();
    expect(screen.getByText(briefingCopy.evidence.method.mixed)).toBeTruthy();
  });

  it("says when a record no longer exists", async () => {
    const missing = client(async () => {
      throw new LycaonApiError("gone", 404, "briefing_record_not_found");
    });
    render(() => <BriefingEvidencePage projectId="p" recordId="gone" client={missing} />);
    await waitFor(() => expect(screen.getByText(briefingCopy.evidence.expired)).toBeTruthy());
  });
});
