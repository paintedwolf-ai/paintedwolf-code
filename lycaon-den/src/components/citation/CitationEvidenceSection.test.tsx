import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { CitationEvidenceSection } from "./CitationEvidenceSection.tsx";

describe("CitationEvidenceSection", () => {
  it("renders structured checks from wire grounding", () => {
    render(() => (
      <CitationEvidenceSection
        grounding={{
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              summary: "1 citation(s) matched leg evidence",
              matched: ["`engine.py`"],
            },
          ],
        }}
        sectionClass="den-worker-transcript-evidence"
        testId="worker-evidence-section"
      />
    ));
    expect(screen.getByTestId("worker-evidence-section")).toBeTruthy();
    expect(screen.getByText("Path citations")).toBeTruthy();
    expect(screen.getByText("1 citation(s) matched leg evidence")).toBeTruthy();
  });

  it("renders vacuous grounding without traced styling", () => {
    render(() => (
      <CitationEvidenceSection
        grounding={{
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              summary: "No path citations in prose",
            },
          ],
        }}
        sectionClass="den-worker-transcript-evidence"
        testId="worker-evidence-section"
      />
    ));
    const section = screen.getByTestId("worker-evidence-section");
    expect(section).toBeTruthy();
    expect(screen.getByText("No citations to trace")).toBeTruthy();
    expect(
      screen.getByTestId("citation-grounding-panel").classList.contains(
        "den-citation-grounding--traced",
      ),
    ).toBe(false);
  });
});
