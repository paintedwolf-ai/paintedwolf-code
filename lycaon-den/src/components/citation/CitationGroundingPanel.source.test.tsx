import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { CitationGroundingView } from "../../chat/grounding/citation-grounding-model.ts";
import {
  GROUNDING_HEADLINE_GROUNDED,
  GROUNDING_SOURCE_AGENT,
} from "../../chat/grounding/grounding-copy.ts";
import { CitationGroundingPanel } from "./CitationGroundingPanel.tsx";

const openSourceLocation = vi.hoisted(() => vi.fn());
const confirmAndOpenExternalLink = vi.hoisted(() => vi.fn());

vi.mock("../../platform/navigation/open-source.ts", () => ({
  openSourceLocation,
}));

vi.mock("../../platform/desktop/external-link.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/desktop/external-link.ts")>();
  return {
    ...actual,
    confirmAndOpenExternalLink,
  };
});

function baseView(
  overrides: Partial<CitationGroundingView> = {},
): CitationGroundingView {
  return {
    outcome: "traced",
    headline: GROUNDING_HEADLINE_GROUNDED,
    hostAssembled: false,
    sourceLabel: GROUNDING_SOURCE_AGENT,
    observedPathsSample: [],
    observedURLsSample: [],
    findings: [],
    citedURLs: [],
    citedEvidence: [],
    proseLeaksSample: [],
    proseAdvisoriesSample: [],
    checks: [],
    hadTraceableWork: true,
    evidenceRecords: [],
    ...overrides,
  };
}

describe("CitationGroundingPanel source clicks", () => {
  beforeEach(() => {
    openSourceLocation.mockReset();
    confirmAndOpenExternalLink.mockReset();
    confirmAndOpenExternalLink.mockResolvedValue(true);
  });

  it("opens path citations via openSourceLocation", async () => {
    render(() => (
      <CitationGroundingPanel
        projectId="proj-1"
        view={baseView({
          citedEvidence: [
            {
              handle: "read#1",
              path: "src/a.go",
              line: 10,
              excerpt: "fn",
              verdict: "matched",
              verdictLabel: "Traced",
            },
          ],
        })}
      />
    ));
    const link = screen.getByTestId("source-path-link");
    expect(link.textContent).toBe("src/a.go:10");
    fireEvent.click(link);
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "proj-1",
      path: "src/a.go",
      line: 10,
    });
  });

  it("opens cited_urls via confirmAndOpenExternalLink", async () => {
    render(() => (
      <CitationGroundingPanel
        projectId="proj-1"
        view={baseView({
          citedURLs: ["https://example.com/docs"],
        })}
      />
    ));
    const link = screen.getByTestId("source-url-link");
    fireEvent.click(link);
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://example.com/docs",
    );
  });

  it("does not open URL when confirm returns false", async () => {
    confirmAndOpenExternalLink.mockResolvedValue(false);
    render(() => (
      <CitationGroundingPanel
        view={baseView({
          citedURLs: ["https://example.com/x"],
        })}
      />
    ));
    fireEvent.click(screen.getByTestId("source-url-link"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalled();
  });

  it("renders handle-only citation as plain text", () => {
    render(() => (
      <CitationGroundingPanel
        projectId="proj-1"
        view={baseView({
          citedEvidence: [
            {
              handle: "opaque#9",
              verdict: "unverifiable",
              verdictLabel: "Unverifiable",
            },
          ],
        })}
      />
    ));
    expect(screen.queryByTestId("source-path-link")).toBeNull();
    expect(screen.getByText("opaque#9")).toBeTruthy();
  });
});
