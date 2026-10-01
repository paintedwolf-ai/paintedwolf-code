import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createStore } from "solid-js/store";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SecurityFullPass, SecurityOverview, WorkflowExplainMeta, WorkflowRun } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { WORKFLOW_EXPLAIN_NAME, WorkflowExplainChicklet } from "./WorkflowExplainChicklet.tsx";

const meta: WorkflowExplainMeta = {
  phase_id: "ingest",
  summary: "A full security scan runs first",
  body: "The review starts from real scanner results.",
  progress: { full_pass: { assessment_id: "pass-1", project_id: "project-1" } },
};

const runningPass = {
  assessment_id: "pass-1",
  requested_at: "2026-09-27T10:00:00Z",
  trigger: "phase_enter",
  members: [
    { scanner_id: "gitleaks", phase: "started", scan: { id: "s1", status: "complete", categories: ["secret"] } },
    {
      scanner_id: "opengrep",
      phase: "started",
      scan: { id: "s2", status: "running", categories: ["sast"], progress: { chunks: 40, completed: 14, files: 18412 } },
    },
  ],
} as unknown as SecurityFullPass;

function overview(fields: Partial<SecurityOverview>): SecurityOverview {
  return { scanners: [], ...fields } as unknown as SecurityOverview;
}

function mount(phase: string, security: SecurityOverview) {
  const [state, setState] = createStore({
    workflowRuns: [{ id: "run-1", session_id: "s1", current_phase: phase, status: "running" } as WorkflowRun],
    latestCodeScan: undefined,
  });
  const client = { getProjectSecurity: vi.fn(async () => security) };
  render(() => (
    <WorkflowExplainChicklet
      meta={meta}
      runId="run-1"
      sessionId="s1"
      entryKey="explain-1"
      client={client}
      appStore={{ state } as unknown as AppStore}
    />
  ));
  return { client, setState };
}

describe("WorkflowExplainChicklet", () => {
  beforeEach(() => resetSurfaceQueriesForTests());

  it("draws the tool chicklet with a running dot and the pass's scanner count", async () => {
    const { client } = mount("ingest", overview({ running: runningPass }));
    const chicklet = screen.getByTestId("workflow-explain-chicklet");
    expect(chicklet.classList.contains("den-tool-part")).toBe(true);
    expect(chicklet.hasAttribute("open")).toBe(false);
    expect(chicklet.querySelector(".den-tool-part-name")?.textContent).toBe(WORKFLOW_EXPLAIN_NAME);
    expect(chicklet.querySelector(".den-tool-part-status-dot")?.getAttribute("data-status")).toBe("running");
    await waitFor(() => expect(screen.getByTestId("workflow-explain-state").textContent).toBe("1 of 2 scanners done"));
    expect(client.getProjectSecurity).toHaveBeenCalledWith("project-1");
  });

  it("expands to the note and the Security page's progress rows", async () => {
    mount("ingest", overview({ running: runningPass }));
    fireEvent.click(screen.getByTestId("workflow-explain-chicklet").querySelector("summary")!);
    await waitFor(() => expect(screen.getByTestId("workflow-explain-progress")).toBeTruthy());
    expect(screen.getByTestId("workflow-explain-body").textContent).toContain(meta.body);
    const readouts = screen.getAllByTestId("scans-full-scan-readout").map((node) => node.textContent);
    expect(readouts).toContain("14 of 40 chunks · 18,412 files");
  });

  it("settles when the run leaves the phase and stops reading progress while closed", () => {
    const { client } = mount("plan", overview({ last_full: runningPass }));
    const chicklet = screen.getByTestId("workflow-explain-chicklet");
    expect(chicklet.querySelector(".den-tool-part-status-dot")?.getAttribute("data-status")).toBe("done");
    expect(client.getProjectSecurity).not.toHaveBeenCalled();
  });

  it("shows a topology phase's workers from the run, without reading the security overview", async () => {
    const [state] = createStore({
      workflowRuns: [{
        id: "run-1", session_id: "s1", current_phase: "hunt", status: "running",
        ui: {
          current_phase_label: "Hunting bugs",
          topology_legs: [
            { id: "hunt_correctness", stage: "hunt_correctness", phase_id: "hunt", label: "Correctness hunt", status: "complete" },
            { id: "hunt_edges", stage: "hunt_edges", phase_id: "hunt", label: "Edge case hunt", status: "running" },
          ],
        },
      } as WorkflowRun],
      latestCodeScan: undefined,
    });
    const client = { getProjectSecurity: vi.fn() };
    render(() => (
      <WorkflowExplainChicklet
        meta={{ phase_id: "hunt", summary: "Workers hunt for bugs in parallel", body: "Why.", progress: { topology: { stages: ["hunt_correctness", "hunt_edges"] } } }}
        runId="run-1"
        entryKey="explain-hunt"
        client={client}
        appStore={{ state } as unknown as AppStore}
      />
    ));
    expect(screen.getByTestId("workflow-explain-state").textContent).toBe("1 of 2 workers done");
    fireEvent.click(screen.getByTestId("workflow-explain-chicklet").querySelector("summary")!);
    await waitFor(() => expect(screen.getByTestId("workflow-explain-legs")).toBeTruthy());
    expect(screen.getByTestId("workflow-explain-legs").textContent).toContain("Edge case hunt");
    expect(client.getProjectSecurity).not.toHaveBeenCalled();
  });

  it("drops the bars once a newer pass replaced the one it names", async () => {
    mount("plan", overview({ last_full: { ...runningPass, assessment_id: "pass-2" } }));
    fireEvent.click(screen.getByTestId("workflow-explain-chicklet").querySelector("summary")!);
    await waitFor(() => expect(screen.getByTestId("workflow-explain-body").textContent).toContain(meta.body));
    expect(screen.queryByTestId("workflow-explain-progress")).toBeNull();
  });
});
