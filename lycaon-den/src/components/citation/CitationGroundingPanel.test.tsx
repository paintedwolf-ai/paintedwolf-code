import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import {
  buildCitationGroundingView,
  type CitationGroundingView,
} from "../../chat/grounding/citation-grounding-model.ts";
import {
  GROUNDING_HEADLINE_GROUNDED,
  GROUNDING_PANEL_SCOPE,
  GROUNDING_SOURCE_AGENT,
} from "../../chat/grounding/grounding-copy.ts";
import { CitationGroundingPanel } from "./CitationGroundingPanel.tsx";

describe("CitationGroundingPanel render-by-shape", () => {
  it.each(["findings", "cited_evidence"] as const)(
    "preserves mixed host verdicts in %s without promising exact matches",
    (field) => {
      const verdicts = ["matched", "traced", "unverifiable"] as const;
      const view = buildCitationGroundingView({
        traced: false,
        [field]: verdicts.map((verdict, index) => ({
          path: `source-${index}.txt`,
          line: 1,
          excerpt: `Excerpt ${index}`,
          verdict,
        })),
      });
      render(() => <CitationGroundingPanel view={view} />);
      expect(screen.getAllByTestId("citation-grounding-citation").map(
        (row) => row.getAttribute("data-verdict"),
      )).toEqual(verdicts);
      expect(screen.getAllByTestId("citation-grounding-verdict").map(
        (label) => label.textContent,
      )).toEqual(["Traced — exact", "Traced — confirm", "Not traced"]);
      expect(screen.getByText(/references that need review are labeled below/)).toBeTruthy();
      expect(screen.queryByText(/excerpts are matched verbatim/)).toBeNull();
    },
  );

  const baseView = (): CitationGroundingView => ({
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
    checks: [
      {
        id: "typed_citations",
        label: "Typed citations from EvidenceMeta",
        status: "passed" as const,
        matched: [],
        failed: [],
      },
    ],
    hadTraceableWork: true,
    evidenceRecords: [],
  });

  it("renders opaque MCP evidence with trust label", () => {
    const view = {
      ...baseView(),
      evidenceRecords: [
        {
          handle: "customfetch#1",
          kind: "customfetch",
          shape: "opaque",
          fidelity: "opaque",
          tool: "mcp_custom_fetch",
          excerpt: "payload bytes captured verbatim",
          urls: [],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    const record = screen.getByTestId("citation-grounding-record");
    expect(record.getAttribute("data-shape")).toBe("opaque");
    expect(
      screen.getByText("Opaque — verbatim capture, no semantics claimed"),
    ).toBeTruthy();
    expect(screen.getByText("payload bytes captured verbatim")).toBeTruthy();
  });

  it("renders file_region evidence", () => {
    const view = {
      ...baseView(),
      evidenceRecords: [
        {
          handle: "read#1",
          kind: "read",
          shape: "file_region",
          fidelity: "structured",
          path: "src/main.go",
          line: 42,
          urls: [],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    expect(screen.getByText("src/main.go:42")).toBeTruthy();
    expect(screen.getByText("Structured")).toBeTruthy();
  });

  it("renders artifact scan evidence with flagged path", () => {
    const view = {
      ...baseView(),
      evidenceRecords: [
        {
          handle: "scan#1",
          kind: "scan",
          shape: "artifact",
          fidelity: "structured",
          tool: "scan_query",
          path: "internal/auth/handler.go",
          excerpt: "SQL injection via string concatenation",
          urls: [],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    expect(screen.getByTestId("citation-grounding-record").getAttribute("data-shape")).toBe(
      "artifact",
    );
    expect(screen.getByText("internal/auth/handler.go")).toBeTruthy();
    expect(screen.getByText("Structured")).toBeTruthy();
  });

  it("renders every grounded url from a web_search record", () => {
    const view = {
      ...baseView(),
      evidenceRecords: [
        {
          handle: "web#1",
          kind: "web",
          shape: "url",
          fidelity: "structured",
          tool: "web_search",
          urls: [
            "https://github.com/cowrie/cowrie",
            "https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action",
          ],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    const urlLinks = screen.getAllByTestId("source-url-link");
    expect(urlLinks.map((el) => el.textContent)).toEqual([
      "https://github.com/cowrie/cowrie",
      "https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action",
    ]);
    expect(screen.getByText("+1 more")).toBeTruthy();
  });

  it("renders unknown shape via generic fallback", () => {
    const view = {
      ...baseView(),
      evidenceRecords: [
        {
          handle: "novel#1",
          kind: "novel",
          shape: "future_shape",
          fidelity: "opaque",
          excerpt: "generic fallback content",
          urls: [],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    expect(screen.getByTestId("citation-grounding-record").getAttribute("data-shape")).toBe(
      "future_shape",
    );
    expect(screen.getByText(/generic fallback content/)).toBeTruthy();
  });

  it("maps wire evidence_records through buildCitationGroundingView", () => {
    const view = buildCitationGroundingView({
      traced: true,
      checks: [
        {
          id: "typed_citations",
          label: "From registry",
          status: "passed" as const,
        },
      ],
      evidence_records: [
        {
          handle: "customfetch#1",
          shape: "opaque",
          fidelity: "opaque",
          tool: "mcp_custom_fetch",
          excerpt: "wire excerpt",
        },
      ],
    });
    render(() => <CitationGroundingPanel view={view} />);
    expect(screen.getByText("wire excerpt")).toBeTruthy();
  });
});

describe("CitationGroundingPanel provenance presentation", () => {
  const tracedView = (): CitationGroundingView => ({
    outcome: "traced",
    headline: GROUNDING_HEADLINE_GROUNDED,
    hostAssembled: false,
    sourceLabel: GROUNDING_SOURCE_AGENT,
    observedPathsSample: [],
    observedURLsSample: [],
    findings: [],
    citedURLs: [],
    citedEvidence: [
      {
        path: "src/a.go",
        line: 10,
        excerpt: "fn main()",
        verdict: "matched",
        verdictLabel: "Traced — exact",
      },
    ],
    proseLeaksSample: [],
    proseAdvisoriesSample: [],
    checks: [
      {
        id: "typed_citations",
        label: "Typed citations",
        status: "passed" as const,
        matched: [],
        failed: [],
      },
    ],
    hadTraceableWork: true,
    evidenceRecords: [],
  });

  it("renders woven scope subhead without callout styling", () => {
    render(() => <CitationGroundingPanel view={tracedView()} />);
    const scope = screen.getByTestId("citation-grounding-scope");
    expect(scope.classList.contains("den-citation-grounding-scope")).toBe(true);
    expect(scope.classList.contains("den-citation-grounding-context--advisory")).toBe(
      false,
    );
    expect(scope.textContent).toBe(GROUNDING_PANEL_SCOPE);
  });

  it("renders styled citation rows with path, excerpt, and verdict", () => {
    const { container } = render(() => (
      <CitationGroundingPanel view={tracedView()} projectId="proj-cite" />
    ));
    expect(screen.getByText("Cited evidence")).toBeTruthy();
    const row = screen.getByTestId("citation-grounding-citation");
    expect(row.classList.contains("den-citation-grounding-citation")).toBe(true);
    expect(row.getAttribute("data-path")).toBe("src/a.go");
    expect(row.getAttribute("data-line")).toBe("10");
    expect(row.getAttribute("data-project-id")).toBe("proj-cite");
    expect(row.querySelector('[data-testid="source-path-link"]')?.textContent).toBe(
      "src/a.go:10",
    );
    expect(screen.getByText("fn main()")).toBeTruthy();
    expect(screen.getByText("Traced — exact")).toBeTruthy();
    expect(container.querySelector(".den-citation-grounding-citations")).toBeTruthy();
  });

  it("uses neutral provenance mark on passed checks, not a green seal", () => {
    render(() => <CitationGroundingPanel view={tracedView()} />);
    const check = screen.getByTestId("citation-grounding-check");
    expect(check.classList.contains("den-citation-grounding-check--passed")).toBe(true);
    expect(check.querySelector(".den-citation-grounding-check-mark")?.textContent).toBe(
      "↳",
    );
  });

  it("keeps attention styling on failed checks", () => {
    const view: CitationGroundingView = {
      ...tracedView(),
      outcome: "partial",
      checks: [
        {
          id: "path_citations",
          label: "Path citations",
          status: "failed" as const,
          matched: [],
          failed: ["missing.go"],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    const check = screen.getByTestId("citation-grounding-check");
    expect(check.classList.contains("den-citation-grounding-check--failed")).toBe(true);
    expect(check.querySelector(".den-citation-grounding-check-mark")?.textContent).toBe(
      "✕",
    );
  });

  it("renders a host-authored advisory check as neutral review, not a failure", () => {
    const view: CitationGroundingView = {
      ...tracedView(),
      checks: [
        {
          id: "url_advisory",
          label: "URL citations",
          status: "advisory" as const,
          summary:
            "1 cited excerpt(s) weren't found word-for-word in the captured source; the cited path was observed — shown for review",
          matched: [],
          failed: ["internal/foo.go:42"],
        },
      ],
    };
    render(() => <CitationGroundingPanel view={view} />);
    const check = screen.getByTestId("citation-grounding-check");
    expect(check.classList.contains("den-citation-grounding-check--advisory")).toBe(true);
    expect(check.classList.contains("den-citation-grounding-check--failed")).toBe(false);
    expect(
      check.querySelector(".den-citation-grounding-check-mark")?.textContent,
    ).not.toBe("✕");
    expect(check.querySelector(".den-citation-grounding-tokens--failed")).toBeNull();
    expect(check.querySelector(".den-citation-grounding-tokens--review")).not.toBeNull();
  });

  it("words advisory prose note as surfaced for review", () => {
    const view: CitationGroundingView = {
      ...tracedView(),
      proseAdvisoryCount: 1,
      proseAdvisoriesSample: ["shell output"],
    };
    render(() => <CitationGroundingPanel view={view} />);
    const advisory = screen.getByTestId("citation-grounding-advisory");
    expect(advisory.textContent).toContain("surfaced for review");
    expect(advisory.classList.contains("den-citation-grounding-context--advisory")).toBe(
      true,
    );
    expect(advisory.classList.contains("den-citation-grounding-check--failed")).toBe(
      false,
    );
  });

  it("labels agent-authored grounding as Agent citations with no host note", () => {
    render(() => <CitationGroundingPanel view={tracedView()} />);
    const source = screen.getByTestId("citation-grounding-source");
    expect(source.textContent).toContain("Source: Agent citations");
    expect(screen.queryByTestId("citation-grounding-source-note")).toBeNull();
  });

  it("labels host-assembled grounding as host-added, positive, with the prose-kept note", () => {
    const view = buildCitationGroundingView({
      traced: false,
      host_assembled: true,
      cited_evidence: [
        { path: "src/a.go", line: 10, excerpt: "fn main()", verdict: "matched" },
      ],
    });
    render(() => <CitationGroundingPanel view={view} />);
    const panel = screen.getByTestId("citation-grounding-panel");
    expect(panel.getAttribute("data-host-assembled")).toBe("true");
    expect(panel.classList.contains("den-citation-grounding--traced")).toBe(true);
    expect(panel.classList.contains("den-citation-grounding--partial")).toBe(false);
    const box = screen.getByTestId("citation-grounding-host-box");
    expect(box.classList.contains("den-citation-grounding-host-box")).toBe(true);
    const source = screen.getByTestId("citation-grounding-source");
    expect(source.textContent).toContain("Source: Host-added (observed)");
    expect(screen.getByTestId("citation-grounding-source-note").textContent).toContain(
      "Answer kept as-is; supporting files and pages it looked at were attached automatically.",
    );
  });

  it("mentions the retry count in the host-assembled provenance box", () => {
    const view = buildCitationGroundingView({
      traced: false,
      host_assembled: true,
      retry_count: 3,
      hint_code: "INVEST_CITATIONS_REQUIRED",
      cited_evidence: [
        { path: "src/a.go", line: 10, excerpt: "fn main()", verdict: "matched" },
      ],
    });
    render(() => <CitationGroundingPanel view={view} />);
    expect(screen.getByTestId("citation-grounding-host-box")).toBeTruthy();
    const note = screen.getByTestId("citation-grounding-source-note");
    expect(note.textContent).toContain("Answer kept as-is after 3 attempts");
    expect(screen.queryByTestId("citation-grounding-host-reason")).toBeNull();
    expect(screen.queryByTestId("citation-grounding-code")).toBeNull();
  });
});

describe("CitationGroundingPanel explore pivots", () => {
  const exploreView = (): CitationGroundingView => ({
    outcome: "traced",
    headline: GROUNDING_HEADLINE_GROUNDED,
    hostAssembled: false,
    sourceLabel: GROUNDING_SOURCE_AGENT,
    observedPathsSample: [],
    observedURLsSample: [],
    findings: [],
    citedURLs: [],
    citedEvidence: [
      {
        handle: "read#1",
        path: "src/a.go",
        line: 10,
        excerpt: "fn main()",
        verdict: "matched",
        verdictLabel: "Traced — exact",
      },
      {
        path: "pkg/b.go",
        line: 3,
        excerpt: "type B",
        verdict: "matched",
        verdictLabel: "Traced — exact",
      },
    ],
    proseLeaksSample: [],
    proseAdvisoriesSample: [],
    checks: [],
    hadTraceableWork: true,
    evidenceRecords: [],
  });

  it("omits Explore affordances when onExplore is absent", () => {
    render(() => <CitationGroundingPanel view={exploreView()} />);
    expect(screen.queryByTestId("citation-grounding-section-explore")).toBeNull();
    expect(screen.queryByTestId("citation-grounding-row-explore")).toBeNull();
  });

  it("seeds handle/path DSL per row and session/leg at section level", () => {
    const onExplore = vi.fn();
    render(() => (
      <CitationGroundingPanel
        view={exploreView()}
        exploreContext={{ sessionId: "sess-abc", legId: "leg-xyz" }}
        onExplore={onExplore}
      />
    ));
    const rowButtons = screen.getAllByTestId("citation-grounding-row-explore");
    expect(rowButtons).toHaveLength(2);
    rowButtons[0]!.click();
    expect(onExplore).toHaveBeenCalledWith("handle:read#1");
    rowButtons[1]!.click();
    expect(onExplore).toHaveBeenCalledWith("path:pkg/b.go");
    screen.getByTestId("citation-grounding-session-explore").click();
    expect(onExplore).toHaveBeenCalledWith("session:sess-abc");
    screen.getByTestId("citation-grounding-leg-explore").click();
    expect(onExplore).toHaveBeenCalledWith("leg:leg-xyz");
  });
});
