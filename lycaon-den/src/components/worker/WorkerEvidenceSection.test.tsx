import { describe, expect, it, vi } from "vitest";
import { fireEvent, isInaccessible, render, screen } from "@solidjs/testing-library";
import type { WorkerTask } from "../../api/types.ts";
import {
  type OpenInSearchRequest,
  registerOpenInSearchSink,
} from "../../search/search-nav.ts";
import { WorkerEvidenceSection } from "./WorkerEvidenceSection.tsx";

function tracedWorkerWithCitation(
  overrides: Partial<WorkerTask> = {},
): WorkerTask {
  return {
    id: "job-explore",
    agent_type: "path-explorer",
    status: "complete",
    created_at: "2026-01-01T00:00:00Z",
    project_id: "proj-1",
    child_session_id: "sess-9",
    leg_id: "leg-7",
    result: {
      status: "complete",
      grounding: {
        traced: true,
        cited_evidence: [{ path: "src/a.go", line: 12, verdict: "matched" }],
        checks: [
          {
            id: "path_citations",
            label: "Path citations",
            status: "passed" as const,
            summary: "1 citation(s) matched tool evidence",
            matched: ["src/a.go:12"],
          },
        ],
      },
    },
    ...overrides,
  };
}

describe("WorkerEvidenceSection", () => {
  it("selects a visible section without presenting a collapse control", () => {
    const onSelect = vi.fn();
    render(() => (
      <WorkerEvidenceSection worker={tracedWorkerWithCitation()} onSelect={onSelect} />
    ));

    const header = screen.getByRole("button", { name: /Evidence/ });
    expect(header.hasAttribute("aria-expanded")).toBe(false);
    fireEvent.click(header);
    expect(onSelect).toHaveBeenCalledOnce();
    expect(isInaccessible(screen.getByText("Path citations"))).toBe(false);
  });

  it("renders per-check verification detail", () => {
    render(() => (
      <WorkerEvidenceSection
        onSelect={vi.fn()}
        worker={{
          id: "job-1",
          agent_type: "path-explorer",
          status: "complete",
          created_at: "2026-01-01T00:00:00Z",
          result: {
            status: "complete",
            grounding: {
              traced: true,
              observed_path_count: 1,
              observed_paths_sample: ["src/a.go"],
              checks: [
                {
                  id: "path_citations",
                  label: "Path citations",
                  status: "passed" as const,
                  summary: "1 citation(s) matched tool evidence",
                  matched: ["src/a.go:12"],
                },
              ],
            },
          },
        }}
        summaryMeta={{
          worker_id: "job-1",
          child_session_id: "child-1",
          agent_type: "path-explorer",
          status: "complete",
          envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="path-explorer" state="complete"></task>',
          grounding: {
            traced: true,
            observed_path_count: 1,
            observed_paths_sample: ["src/a.go"],
            checks: [
              {
                id: "path_citations",
                label: "Path citations",
                status: "passed" as const,
                summary: "1 citation(s) matched tool evidence",
                matched: ["src/a.go:12"],
              },
            ],
          },
        }}
      />
    ));
    expect(screen.getByText("Path citations")).toBeTruthy();
    expect(screen.getByText("1 citation(s) matched tool evidence")).toBeTruthy();
    expect(screen.getByText("src/a.go:12")).toBeTruthy();
    expect(screen.getByText(/1 path in leg evidence/)).toBeTruthy();
  });

  it("renders lifecycle-only grounding in the worker drawer", () => {
    render(() => (
      <WorkerEvidenceSection
        onSelect={vi.fn()}
        worker={{
          id: "job-2",
          agent_type: "path-explorer",
          status: "complete",
          created_at: "2026-01-01T00:00:00Z",
          result: {
            status: "complete",
            grounding: {
              traced: true,
              checks: [
                {
                  id: "scout_survey",
                  label: "Survey tool activity",
                  status: "passed" as const,
                  summary: "At least one successful survey handle in leg evidence",
                },
                {
                  id: "typed_citations",
                  label: "Typed citations",
                  status: "passed" as const,
                  summary: "No typed citations in report",
                },
              ],
            },
          },
        }}
      />
    ));
    expect(screen.getByTestId("worker-evidence-section")).toBeTruthy();
    expect(screen.getByText("Survey tool activity")).toBeTruthy();
    expect(screen.getByText("No citations to trace")).toBeTruthy();
  });

  it("settles the Evidence cue to complete once grounding is traced", () => {
    render(() => (
      <WorkerEvidenceSection
        onSelect={vi.fn()}
        worker={{
          id: "job-1",
          agent_type: "path-explorer",
          status: "complete",
          created_at: "2026-01-01T00:00:00Z",
          result: {
            status: "complete",
            grounding: {
              traced: true,
              checks: [
                {
                  id: "path_citations",
                  label: "Path citations",
                  status: "passed" as const,
                  summary: "matched",
                  matched: ["src/a.go:12"],
                },
              ],
            },
          },
        }}
      />
    ));
    expect(
      screen.getByTestId("worker-section-status").dataset.status,
    ).toBe("complete");
  });

  it("pivots a cited path to search from the traced worker panel", () => {
    const requests: OpenInSearchRequest[] = [];
    const unregister = registerOpenInSearchSink((req) => requests.push(req));
    try {
      render(() => (
        <WorkerEvidenceSection onSelect={vi.fn()} worker={tracedWorkerWithCitation()} />
      ));
      fireEvent.click(screen.getByTestId("citation-grounding-row-explore"));
      expect(requests).toEqual([
        { originProjectId: "proj-1", query: "path:src/a.go" },
      ]);
    } finally {
      unregister();
    }
  });

  it("pivots by session and leg from the traced worker panel", () => {
    const requests: OpenInSearchRequest[] = [];
    const unregister = registerOpenInSearchSink((req) => requests.push(req));
    try {
      render(() => (
        <WorkerEvidenceSection onSelect={vi.fn()} worker={tracedWorkerWithCitation()} />
      ));
      fireEvent.click(screen.getByTestId("citation-grounding-session-explore"));
      fireEvent.click(screen.getByTestId("citation-grounding-leg-explore"));
      expect(requests).toEqual([
        { originProjectId: "proj-1", query: "session:sess-9" },
        { originProjectId: "proj-1", query: "leg:leg-7" },
      ]);
    } finally {
      unregister();
    }
  });

  it("omits Explore pivots when the worker has no project context", () => {
    render(() => (
      <WorkerEvidenceSection
        onSelect={vi.fn()}
        worker={tracedWorkerWithCitation({ project_id: undefined })}
      />
    ));
    expect(screen.queryByTestId("citation-grounding-row-explore")).toBeNull();
    expect(screen.queryByTestId("citation-grounding-section-explore")).toBeNull();
  });

  it("pulses the Evidence cue as working while verification is pending", () => {
    render(() => (
      <WorkerEvidenceSection
        onSelect={vi.fn()}
        worker={{
          id: "job-3",
          agent_type: "path-explorer",
          status: "running",
          created_at: "2026-01-01T00:00:00Z",
        }}
      />
    ));
    expect(
      screen.getByTestId("worker-section-status").dataset.status,
    ).toBe("working");
  });
});
