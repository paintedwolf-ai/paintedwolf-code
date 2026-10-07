import type { WorkflowRun } from "../api/types.ts";

export function runsByIdMap(runs: readonly WorkflowRun[]): Map<string, WorkflowRun> {
  return new Map(runs.map((r) => [r.id, r]));
}

export function latestAmbientRootRun(
  runs: readonly WorkflowRun[],
): WorkflowRun | undefined {
  return newestWorkflowRun(runs.filter(isAmbientRootRun));
}

function newestWorkflowRun(
  runs: readonly WorkflowRun[],
): WorkflowRun | undefined {
  return runs.reduce<WorkflowRun | undefined>((newest, run) => {
    if (!newest) return run;
    const created = Date.parse(run.created_at);
    const newestCreated = Date.parse(newest.created_at);
    if (created !== newestCreated) return created > newestCreated ? run : newest;
    return run.id > newest.id ? run : newest;
  }, undefined);
}

/** Leaf run for transcript and session binding. */
export function resolveLeafRun(
  active: WorkflowRun | undefined,
  runs: readonly WorkflowRun[],
): WorkflowRun | undefined {
  if (active?.id) return active;

  const liveAmbient = runs.find(
    (r) =>
      isSessionCreateAttachRun(r) &&
      (r.status === "running" || r.status === "paused"),
  );
  if (liveAmbient) return liveAmbient;

  const ambient = latestAmbientRootRun(runs);
  if (ambient) return ambient;

  return newestWorkflowRun(runs);
}

export function isSessionCreateAttachRun(run: WorkflowRun | undefined): boolean {
  return run?.attach_policy === "session_create";
}

export function isAmbientRootRun(run: WorkflowRun | undefined): boolean {
  return !!run && isSessionCreateAttachRun(run) && !run.parent_run_id;
}

/** Visible catalog parent for a subroutine leaf. */
export function resolveCatalogRun(
  leaf: WorkflowRun | undefined,
  runsById: ReadonlyMap<string, WorkflowRun>,
): WorkflowRun | undefined {
  if (!leaf) return undefined;

  if (leaf.parent_run_id) {
    const parent = runsById.get(leaf.parent_run_id);
    if (parent && !isAmbientRootRun(parent)) return parent;
    // Missing or ambient parents hide session-created leaves.
    if (isSessionCreateAttachRun(leaf)) return undefined;
    // Catalog children under ambient roots remain visible.
    if (parent && isAmbientRootRun(parent)) return leaf;
    // Unhydrated parents hide child chrome.
    return undefined;
  }

  if (isSessionCreateAttachRun(leaf)) return undefined;
  return leaf;
}

/** Visible catalog run for shell chrome and drawer selection. */
export function catalogWorkflowRun(
  leaf: WorkflowRun | undefined,
  runs: readonly WorkflowRun[],
): WorkflowRun | undefined {
  return resolveCatalogRun(leaf, runsByIdMap(runs));
}
