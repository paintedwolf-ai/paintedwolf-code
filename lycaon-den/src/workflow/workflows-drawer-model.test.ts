import { describe, expect, it } from "vitest";
import type { WorkflowRun, WorkflowSummary } from "../api/types.ts";
import { workflowPhaseLabel } from "./workflow-phase-labels.ts";
import {
  canArmCatalogWorkflow,
  deriveReportAvailable,
  deriveWorkflowPanelActions,
  expandCatalogPresets,
  sortWorkflowCatalog,
  workflowPickerToggleLabel,
  workflowRunHeader,
  workflowRunStepProgress,
  catalogEntryForWorkflow,
  catalogSummaryForRun,
  isBlueprintWorkflowRun,
  workflowRunDisplayName,
} from "./workflows-drawer-model.ts";

const demoCatalog: WorkflowSummary[] = [
  {
    id: "options",
    version: "1.0.0",
    name: "Design options",
    trigger: "/options",
  },
  {
    id: "security-survey",
    version: "1.0.0",
    name: "Security",
    trigger: "/security-survey",
  },
  {
    id: "recon-pack",
    version: "1.0.0",
    name: "Recon",
    trigger: "/recon",
    phases: ["plan", "execute", "reconcile", "drill_plan", "drill", "report", "done"],
  },
  {
    id: "bugbash",
    version: "1.0.0",
    name: "Bugbash",
    trigger: "/bugbash",
    phases: ["hunt", "triage", "expand", "approve", "fix", "closeout", "done"],
  },
  {
    id: "plan",
    version: "1.0.0",
    name: "Plan",
    trigger: "/plan",
    phases: ["intake", "research", "expand", "review", "approve", "execute", "done"],
  },
];

describe("workflow-phase-labels", () => {
  it("uses the host phase label and title-cases only as fallback", () => {
    expect(workflowPhaseLabel("Writing the plan")).toBe("Writing the plan");
    expect(workflowPhaseLabel(undefined)).toBe("—");
  });
});

function planRun(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    id: "run-1",
    session_id: "s",
    workflow_id: "plan",
    workflow_version: "1.0.0",
    status: "running",
    current_phase: "expand",
    ui: { current_phase_label: "Writing the plan" },
    created_at: "t",
    updated_at: "t",
    ...overrides,
	revision: overrides.revision ?? 1,
  };
}

describe("workflows-drawer-model", () => {
  it("disables start only while a catalog run is non-terminal", () => {
    const active = planRun({ id: "r1" });
    expect(canArmCatalogWorkflow(active, [active])).toBe(false);
    expect(canArmCatalogWorkflow({ ...active, status: "complete" }, [active])).toBe(true);
    expect(canArmCatalogWorkflow({ ...active, status: "canceled" }, [active])).toBe(true);
    expect(canArmCatalogWorkflow({ ...active, status: "interrupted" }, [active])).toBe(true);
  });

  it("expands a flat catalog into picker rows sorted by name", () => {
    const rows = expandCatalogPresets(demoCatalog);
    expect(rows.every((r) => !r.start_preset_id)).toBe(true);
    expect(rows.map((r) => r.id)).toEqual([
      "options",
      "security-survey",
      "recon-pack",
      "bugbash",
      "plan",
    ]);

    const sorted = sortWorkflowCatalog(demoCatalog);
    expect(sorted.map((r) => r.name)).toEqual([
      "Bugbash",
      "Design options",
      "Plan",
      "Recon",
      "Security",
    ]);
  });

  it("resolves a catalog entry by workflow id", () => {
    expect(catalogEntryForWorkflow(demoCatalog, "plan", "1.0.0")?.id).toBe("plan");
    expect(catalogEntryForWorkflow(demoCatalog, "recon-pack", "1.0.0")?.trigger).toBe("/recon");
  });

  it("resolves catalog metadata by exact workflow version", () => {
    const catalog: WorkflowSummary[] = [
      { id: "plan", version: "1.0.0", name: "Plan v1" },
      { id: "plan", version: "2.0.0", name: "Plan v2" },
    ];
    expect(catalogSummaryForRun(catalog, planRun())?.name).toBe("Plan v1");
    expect(catalogEntryForWorkflow(catalog, "plan", "2.0.0")?.name).toBe(
      "Plan v2",
    );
    expect(catalogEntryForWorkflow(catalog, "plan", "3.0.0")).toBeUndefined();
  });

  it("builds human run header with step progress", () => {
    const header = workflowRunHeader(planRun(), demoCatalog);
    expect(header.name).toBe("Plan");
    expect(header.phaseLabel).toBe("Writing the plan");
    expect(header.stepProgress).toEqual({ index: 3, total: 7 });
  });

  it("computes step progress from catalog phases", () => {
    expect(
      workflowRunStepProgress(
        { ...planRun(), workflow_id: "recon-pack", current_phase: "reconcile" },
        demoCatalog,
      ),
    ).toEqual({ index: 3, total: 7 });
  });

  it("surfaces a pending feedback question but not the approval gate as a hint", () => {
    expect(
      workflowRunHeader(
        planRun({
          current_phase: "intake",
          ui: { current_phase_label: "Waiting for input", pending_feedback: { phase_id: "intake", prompt: "What are we deciding?" } },
        }),
        demoCatalog,
      ).stateHint,
    ).toBe("What are we deciding?");
    expect(
      workflowRunHeader(
        planRun({ ui: { current_phase_label: "Waiting for approval", human_approval_awaiting: true } }),
        demoCatalog,
      ).stateHint,
    ).toBeUndefined();
  });

  it("surfaces a structured workflow failure", () => {
    const header = workflowRunHeader(planRun({
      status: "failed",
      failure: {
        code: "TOPOLOGY_STAGE_FAILED",
        message: "The workflow topology could not complete.",
        stage: "hunt_edges",
        retryable: false,
      },
    }), demoCatalog);
    expect(header.statusLabel).toBe("Failed");
    expect(header.stateHint).toBe("The workflow topology could not complete.");
  });

  it("routes an open gate to a review pointer with no lifecycle controls", () => {
    const actions = deriveWorkflowPanelActions(planRun({ ui: { current_phase_label: "Waiting for approval", human_approval_awaiting: true } }));
    expect(actions.review?.label).toBe("Review in chat");
    expect(actions.controls).toEqual([]);
    expect(actions.leave.id).toBe("leave");
  });

  it("derives pause and advance while running, resume while paused", () => {
    const running = deriveWorkflowPanelActions(planRun());
    expect(running.review).toBeUndefined();
    expect(running.controls.map((a) => a.id)).toEqual(["pause", "advance"]);

    const paused = deriveWorkflowPanelActions(planRun({ status: "paused" }));
    expect(paused.controls.map((a) => a.id)).toEqual(["resume", "advance"]);
  });

  it("never labels picker toggle Cancel", () => {
    expect(workflowPickerToggleLabel(true)).toBe("Hide workflows");
    expect(workflowPickerToggleLabel(false)).toBe("Pick a workflow");
  });

  it("sorts catalog rows by name", () => {
    expect(sortWorkflowCatalog(demoCatalog)[0]?.name).toBe("Bugbash");
  });

  it("uses the run's host-authorized report availability", () => {
    const run = planRun({ workflow_id: "security-survey", status: "complete" });
    expect(deriveReportAvailable(run)).toBe(false);
    expect(deriveReportAvailable({ ...run, ui: { current_phase_label: "Done", report_available: false } })).toBe(false);
    expect(deriveReportAvailable({ ...run, ui: { current_phase_label: "Done", report_available: true } })).toBe(true);
    // Historical runs do not depend on today's catalog or its availability.
    expect(deriveReportAvailable({ ...run, workflow_id: "removed", ui: { current_phase_label: "Done", report_available: true } })).toBe(true);
  });
});

describe("blueprint affordances follow the catalog", () => {
  const catalog: WorkflowSummary[] = [
    { id: "plan", version: "1.0.0", name: "Plan", supports_blueprints: true },
    { id: "options", version: "1.0.0", name: "Options", supports_blueprints: true },
    { id: "bugbash", version: "1.0.0", name: "Bugbash" },
  ];
  const run = (workflowId: string): WorkflowRun =>
    ({
      id: `run-${workflowId}`,
      session_id: "s1",
      workflow_id: workflowId,
      workflow_version: "1.0.0",
      revision: 1,
      status: "running",
      created_at: "t",
      updated_at: "t",
    }) as WorkflowRun;

  it("covers every blueprint-declaring recipe, not one named plan", () => {
    expect(isBlueprintWorkflowRun(run("plan"), catalog)).toBe(true);
    expect(isBlueprintWorkflowRun(run("options"), catalog)).toBe(true);
    expect(isBlueprintWorkflowRun(run("bugbash"), catalog)).toBe(false);
    expect(isBlueprintWorkflowRun(undefined, catalog)).toBe(false);
    // Nothing to read from is not evidence a run carries a blueprint.
    expect(isBlueprintWorkflowRun(run("plan"), [])).toBe(false);
  });

  it("titles a run by its blueprint only where the catalog declares one", () => {
    expect(workflowRunDisplayName(run("plan"), catalog, "Ship the thing")).toBe("Ship the thing");
    expect(workflowRunDisplayName(run("bugbash"), catalog, "Ship the thing")).toBe("Bugbash");
  });
});
