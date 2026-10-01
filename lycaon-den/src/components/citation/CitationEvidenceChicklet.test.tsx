import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { CitationEvidenceChicklet } from "./CitationEvidenceChicklet.tsx";

describe("CitationEvidenceChicklet", () => {
  it("starts collapsed and expands to the worker-drawer evidence panel", () => {
    render(() => (
      <CitationEvidenceChicklet
        grounding={{
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation" as const,
              summary: "1 citation(s) matched leg evidence",
              matched: ["`engine.py`"],
            },
          ],
        }}
      />
    ));
    expect(screen.getByTestId("citation-evidence-chicklet")).toBeTruthy();
    expect(screen.getByText("Grounded")).toBeTruthy();
    const details = screen.getByTestId("citation-evidence-chicklet") as HTMLDetailsElement;
    expect(details.open).toBe(false);

    details.querySelector("summary")!.click();
    expect(details.open).toBe(true);
    expect(screen.getByTestId("citation-grounding-panel")).toBeTruthy();
    expect(screen.getByText("Path citations")).toBeTruthy();
    expect(screen.getByText("1 citation(s) matched leg evidence")).toBeTruthy();
    expect(
      screen.getByTestId("citation-evidence-chicklet-panel").classList.contains(
        "den-worker-transcript-evidence",
      ),
    ).toBe(true);
  });

  it("uses the shared measured open/close animation", async () => {
    render(() => (
      <CitationEvidenceChicklet
        grounding={{
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation" as const,
              summary: "1 citation(s) matched leg evidence",
              matched: ["`engine.py`"],
            },
          ],
        }}
      />
    ));

    const details = screen.getByTestId(
      "citation-evidence-chicklet",
    ) as HTMLDetailsElement;
    const summary = details.querySelector("summary")!;
    const animations: Animation[] = [];
    Object.defineProperty(details, "scrollHeight", { value: 180 });
    details.getBoundingClientRect = () =>
      ({ height: details.open ? 180 : 24 }) as DOMRect;
    summary.getBoundingClientRect = () => ({ height: 24 }) as DOMRect;
    details.animate = vi.fn(() => {
      const animation = {
        cancel: vi.fn(),
        onfinish: null,
      } as unknown as Animation;
      animations.push(animation);
      return animation;
    });

    summary.click();
    expect(details.open).toBe(true);
    expect(details.dataset.animating).toBe("true");
    expect(details.style.overflow).toBe("hidden");
    expect(details.style.clipPath).toBe(
      `inset(0 round ${details.style.borderRadius})`,
    );
    expect(details.animate).toHaveBeenLastCalledWith(
      { height: ["24px", "180px"] },
      expect.objectContaining({ duration: 240, fill: "forwards" }),
    );

    animations[0]!.onfinish?.call(
      animations[0]!,
      new Event("finish") as AnimationPlaybackEvent,
    );
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

    summary.click();
    expect(details.open).toBe(true);
    expect(details.dataset.closing).toBe("true");
    expect(details.dataset.animating).toBe("true");
    expect(details.style.overflow).toBe("hidden");
    expect(details.animate).toHaveBeenLastCalledWith(
      { height: ["180px", "24px"] },
      expect.objectContaining({ duration: 240, fill: "forwards" }),
    );
  });

  it("does not render when every check was vacuous", () => {
    render(() => (
      <CitationEvidenceChicklet
        grounding={{
          traced: true,
          checks: [
            {
              id: "typed_citations",
              label: "Typed citations",
              status: "passed" as const,
              kind: "citation" as const,
              vacuous: true,
              summary: "No typed citations in synthesis report",
            },
          ],
        }}
      />
    ));
    expect(screen.queryByTestId("citation-evidence-chicklet")).toBeNull();
  });

  it("renders when synthesis cited_evidence is populated", () => {
    render(() => (
      <CitationEvidenceChicklet
        grounding={{
          traced: true,
          cited_evidence: [{ handle: "read#1", path: "src/a.go", line: 12, excerpt: "fn main" }],
          checks: [
            {
              id: "cited_evidence",
              label: "Leg evidence citations",
              status: "passed" as const,
              kind: "citation" as const,
              summary: "1 citation(s) matched leg union ledger",
              matched: ["src/a.go:12"],
            },
          ],
        }}
      />
    ));
    expect(screen.getByTestId("citation-evidence-chicklet")).toBeTruthy();
  });

  it("does not render when every check is vacuous", () => {
    render(() => (
      <CitationEvidenceChicklet
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
    expect(screen.queryByTestId("citation-evidence-chicklet")).toBeNull();
  });
});
