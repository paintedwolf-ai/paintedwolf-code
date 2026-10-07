import { beginSessionSnapshotRead } from "../../api/session-snapshot-refresh.ts";
import type { LycaonClient } from "../../api/client.ts";
import type {
  Message,
  StartWorkflowRunRequest,
  WorkflowRun,
  WorkflowSummary,
  BlueprintSummary,
} from "../../api/types.ts";
import { isApiErrorCode } from "../../api/http.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { projectIdForPath } from "../../store/app-state.ts";
import type { Project } from "../../api/types.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";

/** Refusals that mean the run moved under the caller's view of it. */
const WORKFLOW_CHANGED_CODES = [
  "workflow_revision_conflict",
  "workflow_active",
  "workflow_replacement_target_required",
  "workflow_run_not_active",
] as const;

function workflowRunIdsFromMessages(messages: readonly Message[]): string[] {
  const ids = new Set<string>();
  for (const m of messages) {
    const id = m.workflow_run_id?.trim();
    if (id) ids.add(id);
  }
  return [...ids];
}

/** Fetch runs referenced on transcript rows when list/active omit terminal history. */
export async function mergeWorkflowRunsFromMessages(
  client: LycaonClient,
  messages: readonly Message[],
  runs: readonly WorkflowRun[],
): Promise<WorkflowRun[]> {
  const byId = new Map(runs.map((r) => [r.id, r]));
  const missing = workflowRunIdsFromMessages(messages).filter((id) => !byId.has(id));
  if (missing.length === 0) return [...runs];
  const fetched = await Promise.all(
    missing.map((id) => client.getWorkflowRun(id).catch(() => null)),
  );
  for (const run of fetched) {
    if (run) byId.set(run.id, run);
  }
  return [...byId.values()].sort((a, b) => {
    const created = Date.parse(b.created_at) - Date.parse(a.created_at);
    return created || b.id.localeCompare(a.id);
  });
}

export type RefreshWorkflowOptions = {
  /** Load workflow catalog (drawer picker). Default true. */
  catalog?: boolean;
  /** Load session run history. Default true. */
  history?: boolean;
  /** Load blueprint summaries for the project library. Default true when catalog or history. */
  blueprints?: boolean;
};

export type WorkflowStateSnapshot = {
  activeWorkflowRun: WorkflowRun | undefined;
  workflowRuns: WorkflowRun[];
  workflowCatalog: WorkflowSummary[];
  blueprints: BlueprintSummary[];
};

export async function fetchWorkflowState(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
  opts?: RefreshWorkflowOptions,
): Promise<WorkflowStateSnapshot> {
  const loadCatalog = opts?.catalog !== false;
  const loadHistory = opts?.history !== false;
  const loadBlueprints = opts?.blueprints ?? (loadCatalog || loadHistory);

  const projectId = projectIdForPath(projects, projectDir);
  const activeP = client.getActiveWorkflowRun(sessionId);
  const runsP = loadHistory
    ? client.listSessionWorkflowRuns(sessionId, { limit: 50 }).then((page) => page.runs)
    : Promise.resolve(appStore.state.workflowRuns);
  // Catalog failures do not block transcript hydration.
  const catalogP = loadCatalog
    ? client.listWorkflows({ projectId, sessionId }).catch(() => [] as WorkflowSummary[])
    : Promise.resolve(appStore.state.workflowCatalog);
  const blueprintsP =
    loadBlueprints && projectId
      ? client.listBlueprints(projectId).catch(() => [] as BlueprintSummary[])
      : Promise.resolve(appStore.state.blueprints);

  const [active, runs, catalog, blueprintSummaries] = await Promise.all([
    activeP,
    runsP,
    catalogP,
    blueprintsP,
  ]);

  return {
    activeWorkflowRun: active ?? undefined,
    workflowRuns: runs,
    workflowCatalog: catalog,
    blueprints: blueprintSummaries,
  };
}

const workflowRefreshes = new WeakMap<AppStore, object>();

export async function refreshWorkflowState(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
  opts?: RefreshWorkflowOptions,
): Promise<void> {
  const epoch = appStore.state.sessionViewEpoch;
  const eventEpoch = appStore.state.workflowEventEpoch;
  const request = {};
  if (appStore.state.currentSession?.id?.trim() === sessionId.trim()) workflowRefreshes.set(appStore, request);
  const snapshot = await fetchWorkflowState(
    appStore,
    client,
    sessionId,
    projectDir,
    projects,
    opts,
  );
  if (workflowRefreshes.get(appStore) !== request || appStore.state.workflowEventEpoch !== eventEpoch) return;
  appStore.actions.setWorkflowState(sessionId, epoch, snapshot);
}

async function mutateWorkflowAndRefresh<T>(
  appStore: AppStore,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
  client: LycaonClient,
  mutate: () => Promise<T>,
): Promise<T> {
  const backendRead = beginSessionSnapshotRead(appStore, sessionId);
  try {
    const value = await mutate();
    if (backendRead.isSameBackend()) await refreshWorkflowState(appStore, client, sessionId, projectDir, projects);
    return value;
  } catch (err) {
    if (backendRead.isSameBackend()) await refreshWorkflowState(appStore, client, sessionId, projectDir, projects).catch(() => undefined);
    throw err;
  } finally {
    backendRead.finish();
  }
}

export async function startWorkflowForSession(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
  req: Omit<StartWorkflowRunRequest, "operation_id"> & { operation_id?: string },
): Promise<WorkflowRun> {
  const backendRead = beginSessionSnapshotRead(appStore, sessionId);
  try {
    const operationId = req.operation_id || crypto.randomUUID();
    const reviewed = catalogWorkflowRun(
      appStore.state.activeWorkflowRun,
      appStore.state.workflowRuns,
    ) ?? appStore.state.activeWorkflowRun;
    const command = reviewed
      ? {
          ...req,
          operation_id: operationId,
          replace_run_id: reviewed.id,
          expected_revision: reviewed.revision,
        }
      : { ...req, operation_id: operationId };
    const run = await client.startWorkflowRun(sessionId, command);
    if (backendRead.isSameBackend()) await refreshWorkflowState(appStore, client, sessionId, projectDir, projects);
    return run;
  } catch (err) {
    if (isApiErrorCode(err, WORKFLOW_CHANGED_CODES)) {
      if (backendRead.isSameBackend()) await refreshWorkflowState(appStore, client, sessionId, projectDir, projects).catch(() => undefined);
      throw new Error("The workflow changed. Review it and try again.");
    }
    throw err;
  } finally {
    backendRead.finish();
  }
}

export async function exitActiveWorkflow(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
  reason?: string,
	target?: WorkflowRun,
): Promise<WorkflowRun | null> {
	const active = target ?? catalogWorkflowRun(
		appStore.state.activeWorkflowRun,
		appStore.state.workflowRuns,
	) ?? appStore.state.activeWorkflowRun;
  if (!active) return null;
  return mutateWorkflowAndRefresh(appStore, sessionId, projectDir, projects, client, () =>
    client.exitWorkflowRun(active.id, {
      expected_revision: active.revision,
      ...(reason ? { reason } : {}),
    }),
  );
}

export async function pauseActiveWorkflow(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
): Promise<void> {
  const run = appStore.state.activeWorkflowRun;
  if (!run) return;
  await mutateWorkflowAndRefresh(appStore, sessionId, projectDir, projects, client, () =>
    client.pauseWorkflowRun(run.id, { expected_revision: run.revision }),
  );
}

export async function resumeActiveWorkflow(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
): Promise<void> {
  const run = appStore.state.activeWorkflowRun;
  if (!run) return;
  await mutateWorkflowAndRefresh(appStore, sessionId, projectDir, projects, client, () =>
    client.resumeWorkflowRun(run.id, { expected_revision: run.revision }),
  );
}

export async function advanceActiveWorkflow(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  projects: readonly Project[],
): Promise<void> {
  const run = appStore.state.activeWorkflowRun;
  if (!run) return;
  await mutateWorkflowAndRefresh(appStore, sessionId, projectDir, projects, client, () =>
    client.advanceWorkflowRun(run.id, { expected_revision: run.revision }),
  );
}
