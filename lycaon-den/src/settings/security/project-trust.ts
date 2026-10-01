import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectTrust, UpdateProjectTrustRequest } from "../../api/types.ts";

type TrustView = { projectId: string; value: ProjectTrust | null; error: string | null };
const [view, setView] = createSignal<TrustView>({ projectId: "", value: null, error: null });
let generation = 0;
let scopeGeneration = 0;
let latestMutation = 0;

export function projectTrustView(projectId: string): TrustView {
  const current = view();
  return current.projectId === projectId ? current : { projectId, value: null, error: null };
}

function publishProjectTrust(trust: ProjectTrust): void {
  generation++;
  setView({ projectId: trust.project_id, value: trust, error: null });
}

function begin(projectId: string): number {
  const current = view();
  if (current.projectId !== projectId) {
    scopeGeneration++;
    setView({ projectId, value: null, error: null });
  }
  return ++generation;
}

function failed(projectId: string, mine: number, error: unknown): void {
  if (mine === generation) setView({ projectId, value: null, error: error instanceof Error ? error.message : "Could not load project trust." });
}

export async function loadProjectTrust(client: LycaonClient, projectId: string): Promise<ProjectTrust> {
  const mine = begin(projectId);
  try {
    const trust = await client.getProjectTrust(projectId);
    if (mine === generation) publishProjectTrust(trust);
    return trust;
  } catch (error) { failed(projectId, mine, error); throw error; }
}

export function openProjectTrust(client: LycaonClient, projectId: string): Promise<ProjectTrust> {
  return mutateProjectTrust(projectId, () => client.openProjectTrustReview(projectId), "Could not open trust changes.");
}

export function saveProjectTrust(client: LycaonClient, projectId: string, request: UpdateProjectTrustRequest): Promise<ProjectTrust> {
  return mutateProjectTrust(projectId, () => client.updateProjectTrust(projectId, request), "Could not save project trust.");
}

async function mutateProjectTrust(projectId: string, request: () => Promise<ProjectTrust>, errorMessage: string): Promise<ProjectTrust> {
  const mine = begin(projectId);
  const scope = scopeGeneration;
  latestMutation = mine;
  try {
    const trust = await request();
    // Mutation results supersede reads started while the write was pending.
    if (scope === scopeGeneration && latestMutation === mine) publishProjectTrust(trust);
    return trust;
  } catch (error) {
    if (scope === scopeGeneration && latestMutation === mine) {
      setView(current => ({ ...current, error: error instanceof Error ? error.message : errorMessage }));
    }
    throw error;
  }
}

export function resetProjectTrustForTests(): void {
  generation++;
  scopeGeneration++;
  setView({ projectId: "", value: null, error: null });
}
