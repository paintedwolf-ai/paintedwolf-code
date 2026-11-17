import { render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import type { BriefingSnapshot } from "../api/types.ts";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../platform/navigation/open-files-surface.ts";
import { BriefingEvidenceLink, BriefingSummary, snapshotSections, type SummarySection } from "./BriefingSummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { SUMMARY_FADE_CLASS, clearSummaryFades } from "./summary-fade.ts";

afterEach(() => {
  clearSummaryFades();
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

  it("fades text in once when it arrives while watched, whole and styled from the first frame", () => {
    const text = "Moves the `checkToken` call.";
    const [sections, setSections] = createSignal<SummarySection[]>([{ id: "purpose" }]);
    const first = render(() => <BriefingSummary recordId="r2" sections={sections()} />);
    setSections([{ id: "purpose", text, source: "summarizer" }]);
    const arrived = first.container.querySelector('[data-section="purpose"]')!;
    expect(arrived.textContent).toContain("Moves the checkToken call.");
    expect(arrived.querySelector(".den-briefing-summary__code")?.textContent).toBe("checkToken");
    expect(arrived.querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(1);
    first.unmount();

    const again = render(() => <BriefingSummary recordId="r2" sections={[{ id: "purpose", text, source: "summarizer" }]} />);
    expect(again.container.querySelector(`.${SUMMARY_FADE_CLASS}`)).toBeNull();
    again.unmount();

    const settled = render(() => <BriefingSummary recordId="r3" sections={[{ id: "purpose", text: "Already settled.", source: "summarizer" }]} />);
    expect(settled.container.querySelector(`.${SUMMARY_FADE_CLASS}`)).toBeNull();
    settled.unmount();

    const [next, setNext] = createSignal<SummarySection[]>([{ id: "purpose" }]);
    const newer = render(() => <BriefingSummary recordId="r4" sections={next()} />);
    setNext([{ id: "purpose", text, source: "summarizer" }]);
    expect(newer.container.querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(1);
  });

  it("uses the surface's own disclaimer when given", () => {
    const { container } = render(() => (
      <BriefingSummary recordId="r" disclaimer="Decision help" sections={[{ id: "purpose", text: "Text.", source: "summarizer" }]} />
    ));
    expect(container.querySelector(".den-briefing-mark")?.getAttribute("data-tip")).toBe("Decision help");
  });

  it("shows code spans as code", () => {
    const { container } = render(() => (
      <BriefingSummary recordId="r" sections={[{ id: "change", text: "Reads `FIXTURE_LISTEN_ADDR` first.", source: "summarizer" }]} />
    ));
    expect(container.querySelector(".den-briefing-summary__code")?.textContent).toBe("FIXTURE_LISTEN_ADDR");
    expect(container.textContent).not.toContain("`");
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
