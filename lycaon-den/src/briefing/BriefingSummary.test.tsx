import { render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import type { BriefingSnapshot } from "../api/types.ts";
import { clearLetterRevealOnceKeys, hasLetterRevealOnceKey } from "../components/primitives/RandomLetterReveal.tsx";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../platform/navigation/open-files-surface.ts";
import { BriefingEvidenceLink, BriefingSummary, snapshotSections, type SummarySection } from "./BriefingSummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";

afterEach(() => {
  clearLetterRevealOnceKeys();
  resetOpenFilesSurfaceForTests();
});

const marks = (root: HTMLElement) => root.querySelectorAll(".den-briefing-mark").length;

describe("BriefingSummary", () => {
  it("marks model text only, and shows waiting text per pending section", () => {
    const { container } = render(() => (
      <BriefingSummary recordId="r1" sections={[
        { id: "purpose", text: "Pushes the fix the user asked for.", source: "summarizer" },
        { id: "outcome", text: "Exited 0.", source: "deterministic" },
        { id: "fallback", text: "Changed a binary file.", source: "fallback" },
        { id: "change" },
      ]} />
    ));
    expect(marks(container)).toBe(2);
    const waiting = container.querySelector('[data-section="change"]');
    expect(waiting?.textContent).toContain(briefingCopy.waiting);
    expect(container.querySelector('[data-section="outcome"] .den-briefing-mark')).toBeNull();
    expect(container.textContent).toContain(briefingCopy.disclaimer);
  });

  it("reveals once for text that arrives while watched and at once for settled text", () => {
    const [sections, setSections] = createSignal<SummarySection[]>([{ id: "purpose" }]);
    render(() => <BriefingSummary recordId="r2" sections={sections()} />);
    expect(hasLetterRevealOnceKey("r2\0purpose")).toBe(false);
    setSections([{ id: "purpose", text: "Moves the token check.", source: "summarizer" }]);
    render(() => <BriefingSummary recordId="r3" sections={[{ id: "purpose", text: "Already settled.", source: "summarizer" }]} />);
    expect(hasLetterRevealOnceKey("r3\0purpose")).toBe(true);
  });

  it("uses the surface's own disclaimer when given", () => {
    const { container } = render(() => (
      <BriefingSummary recordId="r" disclaimer="Decision help" sections={[{ id: "purpose", text: "Text.", source: "summarizer" }]} />
    ));
    expect(container.querySelector(".den-briefing-mark")?.getAttribute("data-tip")).toBe("Decision help");
  });

  it("keeps a label in the same flow as its text, waiting or written", () => {
    const { container } = render(() => (
      <BriefingSummary recordId="r" sections={[
        { id: "done", label: "Contribution", text: "Listed the packages.", source: "summarizer" },
        { id: "next", label: "Remaining" },
      ]} />
    ));
    for (const [id, label] of [["done", "Contribution:"], ["next", "Remaining:"]]) {
      const text = container.querySelector(`[data-section="${id}"] .den-briefing-summary__text`);
      expect(text?.querySelector(".den-briefing-summary__label")?.textContent).toBe(label);
    }
  });
});

describe("snapshotSections", () => {
  const base: BriefingSnapshot = {
    profile_id: "tool_activity", profile_version: "1", target_key: "k", scope_fingerprint: "k", target: {}, cache_epoch: "1",
    revision: "2", status: "pending", pending_sections: ["purpose", "change"], outcome: { state: "unknown" }, error_code: "", error_message: "",
    result: { summary: [{ section_id: "purpose", text: "Reused.", manifest_hash: "h", source: "summarizer", reused: true, provider_id: "", model: "", evidence_quality: [], created_at: "" }],
      evidence_ref: { project_id: "p", record_id: "r" } },
  };

  it("lists results then each section still being written", () => {
    expect(snapshotSections(base).map((section) => [section.id, section.text])).toEqual([["purpose", "Reused."], ["change", undefined]]);
  });

  it("stops waiting once stalled or settled", () => {
    expect(snapshotSections(base, true).map((section) => section.id)).toEqual(["purpose"]);
    expect(snapshotSections({ ...base, status: "failed" }).map((section) => section.id)).toEqual(["purpose"]);
  });
});

describe("BriefingEvidenceLink", () => {
  it("opens its record in Files under the accessible name", () => {
    const opened: unknown[] = [];
    registerOpenFilesSurfaceSink((req) => opened.push(req));
    const { getByRole } = render(() => <BriefingEvidenceLink reference={{ project_id: "p", record_id: "rec" }} />);
    getByRole("button", { name: briefingCopy.evidenceLinkName }).click();
    expect(opened).toEqual([{ kind: "briefing-evidence", projectId: "p", recordId: "rec" }]);
  });
});
