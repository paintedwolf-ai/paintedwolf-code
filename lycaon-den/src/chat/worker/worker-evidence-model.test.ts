import { describe, expect, it } from "vitest";
import type { CitationGrounding } from "../../api/types.ts";
import { buildCitationGroundingView } from "../grounding/citation-grounding-model.ts";
import {
  buildWorkerEvidenceFallbackView,
  resolveCitationGrounding,
} from "./worker-evidence-model.ts";

const grounding: CitationGrounding = {
  traced: true,
  observed_path_count: 2,
  observed_paths_sample: ["src/a.go", "src/b.go"],
  findings: [{ path: "src/a.go", line: 10, excerpt: "func main", note: "entry" }],
  cited_urls: ["https://example.com/doc"],
  checks: [
    {
      id: "path_citations",
      label: "Path citations",
      status: "passed" as const,
      summary: "2 citation(s) matched tool evidence",
      matched: ["src/a.go:10", "`src/b.go`"],
    },
    {
      id: "scout_survey",
      label: "Survey tool activity",
      status: "passed" as const,
      summary: "At least one successful survey tool ran",
    },
  ],
};

describe("buildWorkerEvidenceFallbackView", () => {
  it("returns undefined when host grounding audit is present", () => {
    expect(
      buildWorkerEvidenceFallbackView(
        {
          worker_id: "job-1",
          child_session_id: "child-1",
          agent_type: "path-explorer",
          status: "complete",
          envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="path-explorer" state="complete"></task>',
          grounding,
        },
        {
          id: "job-1",
          agent_type: "path-explorer",
          status: "complete",
          created_at: "2026-01-01T00:00:00Z",
          result: { status: "complete", grounding },
        },
      ),
    ).toBeUndefined();
  });

  it("structured grounding is rendered via citation-grounding-model", () => {
    const resolved = resolveCitationGrounding(
      {
        worker_id: "job-1",
        child_session_id: "child-1",
        agent_type: "path-explorer",
        status: "complete",
        envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="path-explorer" state="complete"></task>',
        grounding,
      },
      {
        id: "job-1",
        agent_type: "path-explorer",
        status: "complete",
        created_at: "2026-01-01T00:00:00Z",
        result: { status: "complete", grounding },
      },
    );
    const view = buildCitationGroundingView(resolved!);
    expect(view.outcome).toBe("traced");
    expect(view.findings).toHaveLength(1);
    expect(view.findings[0]?.path).toBe("src/a.go");
    expect(view.citedURLs).toEqual(["https://example.com/doc"]);
    expect(view.checks).toHaveLength(2);
    expect(view.checks[0]?.matched).toEqual(["src/a.go:10", "`src/b.go`"]);
    expect(view.observedPathCount).toBe(2);
  });

  it("shows structured failure copy when worker closeout is exhausted", () => {
    const view = buildWorkerEvidenceFallbackView(
      undefined,
      {
        id: "job-fail",
        agent_type: "code-reviewer",
        status: "failed" as const,
        created_at: "2026-01-01T00:00:00Z",
        failure: {
          code: "worker_closeout_exhausted",
          title: "Worker could not finish",
          message: "The worker used every tool turn and both closing attempts.",
          suggested_action: "Retry with a narrower task() brief.",
        },
      },
      "job-fail",
    );
    expect(view?.outcome).toBe("failed");
    expect(view?.headline).toBe("Worker could not finish");
    expect(view?.hintCode).toBe("worker_closeout_exhausted");
    expect(view?.statusLines[0]).toContain("closing attempts");
  });

  it("does not infer traced from summary status alone", () => {
    const view = buildWorkerEvidenceFallbackView(
      {
        worker_id: "job-3",
        child_session_id: "child-3",
        agent_type: "implementer",
        status: "complete",
        envelope: '<task job_id="job-3" child_session_id="child-3" agent_type="implementer" state="complete"></task>',
      },
      {
        id: "job-3",
        agent_type: "implementer",
        status: "complete",
        created_at: "2026-01-01T00:00:00Z",
        result: { status: "complete" },
      },
    );
    expect(view?.outcome).toBe("pending");
    expect(view?.headline).toBe("Verification audit unavailable");
  });
});
