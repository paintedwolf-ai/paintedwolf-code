import { describe, expect, it } from "vitest";
import type { SecurityFullPass, SecurityOverview, WorkflowExplainMeta, WorkflowRun, WorkflowTopologyLeg } from "../api/types.ts";
import {
  topologyLegRows,
  workflowExplainLegs,
  workflowExplainPass,
  workflowExplainState,
  workflowExplainStatus,
  workflowExplainWantsLiveProgress,
} from "./workflow-explain-model.ts";

const meta: WorkflowExplainMeta = {
  phase_id: "ingest",
  summary: "A full security scan runs first",
  body: "Why.",
  progress: { full_pass: { assessment_id: "pass-1", project_id: "project-1" } },
};

function run(current_phase: string, status: WorkflowRun["status"]): WorkflowRun {
  return { id: "run-1", session_id: "s1", current_phase, status } as WorkflowRun;
}

function pass(id: string, members: SecurityFullPass["members"]): SecurityFullPass {
  return { assessment_id: id, requested_at: "t", members } as SecurityFullPass;
}

function overview(fields: Partial<SecurityOverview>): SecurityOverview {
  return { scanners: [], ...fields } as SecurityOverview;
}

describe("workflowExplainStatus", () => {
  it("runs while the run holds the note's phase", () => {
    expect(workflowExplainStatus(meta, run("ingest", "running"))).toBe("running");
    expect(workflowExplainWantsLiveProgress(meta, run("ingest", "running"))).toBe(true);
  });

  it("settles once the run leaves the phase, and fails with the run", () => {
    expect(workflowExplainStatus(meta, run("plan", "running"))).toBe("done");
    expect(workflowExplainStatus(meta, run("ingest", "failed"))).toBe("error");
    expect(workflowExplainStatus(meta, undefined)).toBe("done");
    expect(workflowExplainWantsLiveProgress(meta, run("plan", "running"))).toBe(false);
  });

  it("never asks for progress it was not given", () => {
    const plain = { ...meta, progress: undefined };
    expect(workflowExplainWantsLiveProgress(plain, run("ingest", "running"))).toBe(false);
  });
});

describe("workflowExplainPass", () => {
  const live = pass("pass-1", [
    { scanner_id: "gitleaks", phase: "started", scan: { status: "complete" } },
    { scanner_id: "opengrep", phase: "started", scan: { status: "running" } },
  ] as SecurityFullPass["members"]);

  it("reads the named pass while it runs and after it becomes the latest", () => {
    expect(workflowExplainPass(meta, overview({ running: live }))).toBe(live);
    expect(workflowExplainPass(meta, overview({ last_full: live }))).toBe(live);
    expect(workflowExplainState(live, [])).toBe("1 of 2 scanners done");
  });

  it("stops drawing a pass a newer one replaced", () => {
    expect(workflowExplainPass(meta, overview({ last_full: pass("pass-2", []) }))).toBeNull();
    expect(workflowExplainPass(meta, null)).toBeNull();
    expect(workflowExplainState(null, [])).toBe("");
  });
});

describe("topology legs", () => {
  const huntMeta: WorkflowExplainMeta = {
    phase_id: "hunt",
    summary: "Workers hunt for bugs in parallel",
    body: "Why.",
    progress: { topology: { stages: ["hunt_correctness", "hunt_edges"] } },
  };
  const legs: WorkflowTopologyLeg[] = [
    { id: "hunt_correctness", stage: "hunt_correctness", phase_id: "hunt", label: "Correctness hunt", status: "complete" },
    { id: "hunt_edges", stage: "hunt_edges", phase_id: "hunt", label: "Edge case hunt", status: "retry_pending" },
    {
      id: "triage", stage: "triage", phase_id: "triage", label: "Triage", status: "pending",
      waits_for: ["hunt_correctness", "hunt_edges"],
    },
  ];
  const huntRun = { ...run("hunt", "running"), ui: { current_phase_label: "Hunting bugs", topology_legs: legs } } as WorkflowRun;

  it("reads the legs of the stages the note names", () => {
    expect(workflowExplainLegs(huntMeta, huntRun).map((leg) => leg.id)).toEqual(["hunt_correctness", "hunt_edges"]);
    expect(workflowExplainState(null, workflowExplainLegs(huntMeta, huntRun))).toBe("1 of 2 workers done");
    expect(workflowExplainLegs(meta, huntRun)).toEqual([]);
  });

  it("says what each leg is doing, and what a waiting leg waits for", () => {
    const rows = topologyLegRows(legs, huntRun);
    expect(rows.map((row) => [row.status, row.readout, row.ratio])).toEqual([
      ["Complete", undefined, 1],
      ["Retrying", "Continuing with a larger budget", 0],
      ["Waiting", "Starts after Edge case hunt", 0],
    ]);
  });
});
