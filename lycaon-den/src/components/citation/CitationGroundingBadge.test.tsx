import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { CitationGroundingBadge } from "./CitationGroundingBadge.tsx";

const grounding = {
  traced: true,
  checks: [
    {
      id: "path_citations",
      label: "Path citations",
      status: "passed" as const,
      kind: "citation" as const,
      summary: "2 citation(s) matched leg evidence",
      matched: ["src/a.go:10"],
    },
  ],
};

describe("CitationGroundingBadge", () => {
  it("renders a compact grounded chicklet matching task-card status scale", () => {
    render(() => <CitationGroundingBadge grounding={grounding} />);
    const badge = screen.getByTestId("citation-grounding-badge");
    expect(badge.querySelector(".den-citation-grounding-chicklet--traced")).toBeTruthy();
    expect(badge.textContent).toContain("Grounded");
    expect(badge.textContent).toContain("↳");
    expect(screen.queryByTestId("citation-grounding-panel")).toBeNull();
  });

  it("opens worker evidence when clicked on a task card", () => {
    const onOpenDetail = vi.fn();
    render(() => (
      <CitationGroundingBadge grounding={grounding} onOpenDetail={onOpenDetail} />
    ));
    screen.getByTestId("citation-grounding-badge").click();
    expect(onOpenDetail).toHaveBeenCalledTimes(1);
  });

  it("does not render when grounding is absent", () => {
    render(() => <CitationGroundingBadge grounding={undefined} />);
    expect(screen.queryByTestId("citation-grounding-badge")).toBeNull();
  });

  it("uses the grounded state for host-assembled evidence", () => {
    render(() => (
      <CitationGroundingBadge
        grounding={{
          traced: false,
          host_assembled: true,
          cited_evidence: [
            { path: "src/a.go", line: 10, excerpt: "fn main()", verdict: "matched" },
          ],
        }}
      />
    ));
    const badge = screen.getByTestId("citation-grounding-badge");
    expect(badge.querySelector(".den-citation-grounding-chicklet--traced")).toBeTruthy();
    expect(badge.querySelector(".den-citation-grounding-chicklet--partial")).toBeNull();
    expect(badge.textContent).toContain("Grounded");
    expect(badge.textContent).toContain("↳");
  });

  it("does not render when every check was vacuous", () => {
    render(() => (
      <CitationGroundingBadge
        grounding={{
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation" as const,
              vacuous: true,
              summary: "No path citations in prose",
            },
          ],
        }}
      />
    ));
    expect(screen.queryByTestId("citation-grounding-badge")).toBeNull();
  });
});
