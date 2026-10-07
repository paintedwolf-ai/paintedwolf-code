import type {
  BlueprintSummary,
  WorkflowRun,
  WorkflowRunStatus,
  WorkflowSummary,
} from "../api/types.ts";
import { catalogWorkflowRun } from "./workflow-run-stack.ts";
import { workflowPhaseLabel } from "./workflow-phase-labels.ts";

export const BUILD_POSTURE_LABEL = "Build";

export function isBlueprintWorkflowRun(
  run: WorkflowRun | null | undefined,
  catalog: readonly WorkflowSummary[],
): boolean {
  if (!run) return false;
  return workflowSummaryForRun(catalog, run)?.supports_blueprints === true;
}

/** Picker row — manifest entry or a virtual preset row for drawer start. */
export type CatalogPickerRow = WorkflowSummary & { start_preset_id?: string };

export type WorkflowStepProgress = { index: number; total: number };

export type WorkflowRunHeader = {
  name: string;
  phaseLabel: string;
  statusLabel: string;
  stepProgress?: WorkflowStepProgress;
  stateHint?: string;
};

export type WorkflowPanelActionId =
  | "pause"
  | "resume"
  | "advance"
  | "review"
  | "leave";

export type WorkflowPanelAction = {
  id: WorkflowPanelActionId;
  label: string;
  testId: string;
};

export type WorkflowPanelActions = {
  /** Primary pointer to the in-chat approval card while a gate is open. */
  review?: WorkflowPanelAction;
  /** Lifecycle controls (pause/resume/advance); empty while a gate is open. */
  controls: WorkflowPanelAction[];
  /** Always-present quiet exit. */
  leave: WorkflowPanelAction;
};

const STATUS_LABELS: Record<WorkflowRunStatus, string> = {
  running: "Running",
  paused: "Paused",
  paused_on_child: "Waiting on nested work",
  complete: "Complete",
  failed: "Failed",
  canceled: "Canceled",
  interrupted: "Interrupted",
};

export function workflowStatusLabel(status: WorkflowRunStatus): string {
  return STATUS_LABELS[status];
}

export function workflowSummaryForRun(
  catalog: readonly WorkflowSummary[],
  run: WorkflowRun,
): WorkflowSummary | undefined {
  return run.ui?.definition ?? catalog.find(
    (row) =>
      row.id === run.workflow_id && row.version === run.workflow_version,
  );
}

export function isTerminalWorkflowRunStatus(status: WorkflowRunStatus): boolean {
  return (
    status === "complete" ||
    status === "failed" ||
    status === "canceled" ||
    status === "interrupted"
  );
}

export function deriveReportAvailable(run: WorkflowRun): boolean {
  return run.ui?.report_available === true;
}

export function workflowRunDisplayName(
  run: WorkflowRun,
  catalog: readonly WorkflowSummary[],
  blueprintName?: string,
): string {
  if (blueprintName?.trim() && isBlueprintWorkflowRun(run, catalog)) {
    return blueprintName.trim();
  }
  const entry = workflowSummaryForRun(catalog, run);
  return entry?.name?.trim() || run.workflow_id;
}

export function workflowRunStepProgress(
  run: WorkflowRun,
  catalog: readonly WorkflowSummary[],
): WorkflowStepProgress | undefined {
  const phases = workflowSummaryForRun(catalog, run)?.phases;
  if (!phases?.length) return undefined;
  const phase = run.current_phase?.trim();
  if (!phase) return undefined;
  const index = phases.indexOf(phase);
  if (index < 0) return undefined;
  return { index: index + 1, total: phases.length };
}

/** Running feedback prompt shown inline. */
export function workflowRunStateHint(run: WorkflowRun): string | undefined {
  const failureMessage = run.failure?.message?.trim();
  if (run.status === "failed" && failureMessage) {
    return failureMessage;
  }
  const pendingPrompt = run.ui?.pending_feedback?.prompt?.trim();
  if (run.status === "running" && pendingPrompt) {
    return pendingPrompt;
  }
  return undefined;
}

export function workflowRunHeader(
  run: WorkflowRun,
  catalog: readonly WorkflowSummary[],
  blueprintName?: string,
): WorkflowRunHeader {
  return {
    name: workflowRunDisplayName(run, catalog, blueprintName),
    phaseLabel: workflowPhaseLabel(run.ui?.current_phase_label),
    statusLabel: workflowStatusLabel(run.status),
    stepProgress: workflowRunStepProgress(run, catalog),
    stateHint: workflowRunStateHint(run),
  };
}

/** Actions shown while approval or lifecycle controls are available. */
export function deriveWorkflowPanelActions(run: WorkflowRun): WorkflowPanelActions {
  const leave: WorkflowPanelAction = {
    id: "leave",
    label: "Leave",
    testId: "workflow-leave",
  };

  if (run.ui?.human_approval_awaiting) {
    return {
      review: {
        id: "review",
        label: "Review in chat",
        testId: "workflow-review-in-chat",
      },
      controls: [],
      leave,
    };
  }

  const controls: WorkflowPanelAction[] = [];
  if (run.status === "running") {
    controls.push({ id: "pause", label: "Pause", testId: "workflow-pause" });
    controls.push({ id: "advance", label: "Advance", testId: "workflow-advance" });
  } else if (run.status === "paused") {
    controls.push({ id: "resume", label: "Resume", testId: "workflow-resume" });
    controls.push({ id: "advance", label: "Advance", testId: "workflow-advance" });
  }

  return { controls, leave };
}

export function workflowPickerToggleLabel(pickerOpen: boolean): string {
  return pickerOpen ? "Hide workflows" : "Pick a workflow";
}

export function expandCatalogPresets(
  catalog: readonly WorkflowSummary[],
): CatalogPickerRow[] {
  const out: CatalogPickerRow[] = [];
  for (const wf of catalog) {
    out.push(wf);
    for (const preset of wf.presets ?? []) {
      out.push({
        ...wf,
        name: preset.name ?? preset.id,
        description: preset.description ?? wf.description,
        trigger: preset.trigger ?? wf.trigger,
        start_preset_id: preset.id,
      });
    }
  }
  return out;
}

export function sortWorkflowCatalog(
  catalog: readonly WorkflowSummary[],
): CatalogPickerRow[] {
  return expandCatalogPresets(catalog).sort((a, b) => {
    const an = (a.name ?? a.id).toLowerCase();
    const bn = (b.name ?? b.id).toLowerCase();
    return an.localeCompare(bn);
  });
}

export function workflowPickerRowTitle(w: CatalogPickerRow): string {
  return w.name ?? w.id;
}

export function workflowPickerRowTrigger(w: CatalogPickerRow): string | undefined {
  const trigger = w.trigger?.trim();
  return trigger || undefined;
}

export function canArmCatalogWorkflow(
  leaf?: WorkflowRun | null,
  runs: readonly WorkflowRun[] = [],
): boolean {
  const blocking = catalogWorkflowRun(leaf ?? undefined, runs);
  if (!blocking) return true;
  return isTerminalWorkflowRunStatus(blocking.status);
}

export function blueprintNameForRun(
  run: WorkflowRun | undefined,
  blueprintsById: Record<string, BlueprintSummary>,
): string | undefined {
  if (!run?.blueprint_path) return undefined;
  return blueprintsById[run.blueprint_path]?.title;
}

export function catalogEntryForWorkflow(
  catalog: readonly WorkflowSummary[],
  workflowId: string,
  workflowVersion: string,
  presetId?: string,
): CatalogPickerRow | undefined {
  const rows = expandCatalogPresets(catalog);
  if (presetId?.trim()) {
    const pid = presetId.trim();
    return rows.find(
      (r) =>
        r.id === workflowId &&
        r.version === workflowVersion &&
        r.start_preset_id === pid,
    );
  }
  return rows.find(
    (r) =>
      r.id === workflowId &&
      r.version === workflowVersion &&
      !r.start_preset_id,
  );
}
