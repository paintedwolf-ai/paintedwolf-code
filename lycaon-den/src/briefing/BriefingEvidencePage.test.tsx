import { fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { BriefingRecord } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import { LycaonApiError } from "../api/http.ts";
import { BriefingEvidencePage } from "./BriefingEvidencePage.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { briefingReturnAnchor, openBriefingEvidence } from "./briefing-evidence.ts";

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
    const page = await screen.findByTestId("briefing-evidence-page");
    await waitFor(() => expect(page.textContent).toContain("SUPPLIED_INTENT"));
    const copy = briefingCopy.evidence;
    const order: string[] = [copy.summary, copy.used, copy.coverage, copy.sources, copy.generation];
    const headings = within(page).getAllByRole("heading").map((heading) => heading.textContent ?? "");
    expect(headings.filter((heading) => order.includes(heading))).toEqual(order);

    // What the summary used is listed as evidence; what it could not use is a stated limit, not an item.
    const items = [...page.querySelectorAll<HTMLElement>("[data-inclusion]")];
    expect(items.map((item) => item.dataset.inclusion)).toEqual(["included"]);
    expect(items[0]?.textContent).toContain("SUPPLIED_INTENT");
    expect(items[0]?.textContent).not.toContain("no immutable revision");
    expect(page.textContent).toContain("message:u1");
    expect(page.textContent).toContain("no immutable revision");
    expect(page.textContent).toContain(record.sections[0]!.result!.text);
    expect(page.textContent).toContain(copy.unavailableSection);
    expect(page.textContent).toContain(copy.method.mixed);
  });

  it("says when a record no longer exists", async () => {
    const missing = client(async () => {
      throw new LycaonApiError("gone", 404, "briefing_record_not_found");
    });
    render(() => <BriefingEvidencePage projectId="p" recordId="gone" client={missing} />);
    await screen.findByText(briefingCopy.evidence.expired);
    expect(screen.queryByText("SUPPLIED_INTENT")).toBeNull();
  });

  it("remembers bounded return anchors and focuses the caller on Back", async () => {
    for (let i = 0; i < 66; i++) {
      openBriefingEvidence({ project_id: "p", record_id: `rec-${i}` }, { label: `card-${i}`, focus: () => {} });
    }
    expect(briefingReturnAnchor("rec-0")).toBeUndefined();
    expect(briefingReturnAnchor("rec-65")?.label).toBe("card-65");

    const focus = vi.fn();
    openBriefingEvidence({ project_id: "p", record_id: record.record_id }, { label: "activity", focus });
    render(() => <BriefingEvidencePage projectId="p" recordId={record.record_id} client={client(async () => record)} />);
    const back = await screen.findByRole("button", { name: briefingCopy.evidence.back("activity") });
    fireEvent.click(back);
    expect(focus).toHaveBeenCalledOnce();
  });
});
