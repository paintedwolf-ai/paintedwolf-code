import { describe, expect, it } from "vitest";
import {
  buildCitationGroundingView,
  citationGroundingHadTraceableWork,
  citationGroundingPresent,
} from "./citation-grounding-model.ts";
import {
  GROUNDING_HEADLINE_EVIDENCE_OPTIONAL,
  GROUNDING_HEADLINE_GROUNDED,
} from "./grounding-copy.ts";

describe("citation-grounding-model", () => {
  // Each shape is one the host writes for speech with no observed source behind it.
  describe.each([
    ["host progress note", { traced: true, evidence_optional: true }],
    [
      "exempt reply with its vacuous check",
      {
        traced: true,
        evidence_optional: true,
        checks: [
          {
            id: "evidence_optional",
            label: "Citations",
            status: "passed" as const,
            kind: "citation" as const,
            vacuous: true,
            summary: "No citations required: no tool observations in this turn",
          },
        ],
      },
    ],
    [
      "exempt reply with a verification assessment",
      {
        traced: true,
        evidence_optional: true,
        verification: { method: "inspection" as const, reason: "Nothing to run." },
      },
    ],
  ])("AC-COMPAT-05 exempt speech: %s", (_name, grounding) => {
    it("is never presented as a checked source", () => {
      const view = buildCitationGroundingView(grounding);
      expect(citationGroundingHadTraceableWork(grounding)).toBe(false);
      expect(view.outcome).toBe("evidence_optional");
      expect(view.headline).toBe(GROUNDING_HEADLINE_EVIDENCE_OPTIONAL);
      expect(view.headline).not.toBe(GROUNDING_HEADLINE_GROUNDED);
      expect(view.checks).toEqual([]);
    });

    it("still counts as the host's accept signal for its row", () => {
      expect(citationGroundingPresent(grounding)).toBe(true);
    });
  });

  it("carries a cited range to the citation row", () => {
    const view = buildCitationGroundingView({
      traced: true,
      cited_evidence: [
        { handle: "read#3", path: "a.go", line: 42, end_line: 58, verdict: "traced" },
        { handle: "read#3", path: "b.go", line: 7, verdict: "traced" },
      ],
    });
    expect(view.citedEvidence.map((row) => [row.line, row.endLine])).toEqual([
      [42, 58],
      [7, undefined],
    ]);
  });

  it("hides chicklets when every check was vacuous", () => {
    const grounding = {
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
        {
          id: "url_citations",
          label: "URL citations",
          status: "passed" as const,
          kind: "citation" as const,
          vacuous: true,
          summary: "No URL citations in prose",
        },
      ],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(false);
    expect(buildCitationGroundingView(grounding).headline).toBe(
      "No citations to trace",
    );
  });

  it("hides chicklets for vacuous typed-citation-only audits", () => {
    const grounding = {
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
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(false);
  });

  it("hides chicklets when only worker lifecycle checks passed", () => {
    const grounding = {
      traced: true,
      checks: [
        {
          id: "scout_survey",
          label: "Survey tool activity",
          status: "passed" as const,
          kind: "lifecycle" as const,
          summary: "At least one successful survey handle in leg evidence",
        },
        {
          id: "typed_citations",
          label: "Typed citations",
          status: "passed" as const,
          kind: "citation" as const,
          vacuous: true,
          summary: "No typed citations in report",
        },
      ],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(false);
  });

  it("shows chicklets when synthesis cited_evidence populated with non-vacuous check", () => {
    const grounding = {
      traced: true,
      cited_evidence: [{ handle: "read#1", path: "src/a.go", line: 12, excerpt: "fn main" }],
      checks: [
        {
          id: "cited_evidence",
          label: "Leg evidence citations",
          status: "passed" as const,
          kind: "citation" as const,
          summary: "1 citation(s) matched leg union ledger",
          matched: ["src/a.go:12 (\"fn main\")"],
        },
      ],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(true);
  });

  it("shows chicklets when typed findings were traced on the wire", () => {
    const grounding = {
      traced: true,
      findings: [{ path: "src/a.go", line: 10, excerpt: "fn main" }],
      checks: [
        {
          id: "finding_citations",
          label: "Finding citations",
          status: "passed" as const,
          kind: "citation" as const,
          summary: "1 finding(s) matched tool evidence",
          matched: ["grep#1"],
        },
      ],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(true);
  });

  it("shows chicklets when at least one check matched evidence", () => {
    const grounding = {
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
        {
          id: "url_citations",
          label: "URL citations",
          status: "passed" as const,
          kind: "citation" as const,
          vacuous: true,
          summary: "No URL citations in prose",
        },
      ],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(true);
  });

  it("counts synthesis cited evidence with no formal check as verifiable work", () => {
    const grounding = {
      traced: false,
      cited_evidence: [{ handle: "read#1", path: "src/a.go", line: 12 }],
    };
    expect(citationGroundingHadTraceableWork(grounding)).toBe(true);
  });

  it("maps matched and traced verdict labels on findings", () => {
    const view = buildCitationGroundingView({
      traced: true,
      findings: [
        {
          path: "src/a.go",
          line: 10,
          excerpt: "fn main",
          verdict: "matched",
          handle: "read#1",
        },
        {
          path: "src/b.go",
          line: 3,
          excerpt: "paraphrase",
          verdict: "traced",
          handle: "grep#2",
        },
      ],
    });
    expect(view.findings[0]?.verdictLabel).toBe("Traced — exact");
    expect(view.findings[1]?.verdictLabel).toBe("Traced — confirm");
  });

  it("maps prose leak duplication onto the view", () => {
    const view = buildCitationGroundingView({
      traced: true,
      prose_leak_count: 2,
      prose_leaks_sample: ["f.go:42", "https://example.com/a"],
      checks: [
        {
          id: "prose_duplication",
          label: "Narrative prose",
          status: "passed" as const,
          kind: "citation" as const,
          summary: "2 citation(s) duplicated in narrative — already in typed fields",
          matched: ["f.go:42", "https://example.com/a"],
        },
      ],
    });
    expect(view.proseLeakCount).toBe(2);
    expect(view.proseLeaksSample).toEqual(["f.go:42", "https://example.com/a"]);
  });

  it.each(["url_advisory", "survey_advisory"])(
    "uses the host-authored advisory status for %s",
    (id) => {
      const view = buildCitationGroundingView({
        traced: true,
        checks: [
          {
            id,
            label: "Review citation",
            status: "advisory" as const,
            kind: "citation",
            failed: ["review-me"],
          },
        ],
      });

      expect(view.checks[0]?.status).toBe("advisory");
      expect(view.headline).toBe(GROUNDING_HEADLINE_GROUNDED);
    },
  );
});
