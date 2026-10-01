import type { Project } from "../api/types.ts";
import { projectIdForPath } from "../store/app-state.ts";

export type ResolvedSettingsScope =
  | { scope: "global"; projectId?: never }
  | { scope: "project"; projectId: string };

export function resolveGlobalSettingsScope(): ResolvedSettingsScope {
  return { scope: "global" };
}

export function resolveProjectSettingsScope(
  projectDir: string | undefined,
  projects: readonly Project[],
): ResolvedSettingsScope {
  if (!projectDir) {
    throw new Error("Project settings require a project directory.");
  }
  const projectId = projectIdForPath(projects, projectDir);
  if (!projectId) {
    throw new Error(`Project settings cannot resolve ${projectDir}.`);
  }
  return { scope: "project", projectId };
}

export function resolveEditorSettingsScope(
  alwaysProjectScope: boolean | undefined,
  projectDir: string | undefined,
  projects: readonly Project[],
): ResolvedSettingsScope {
  if (alwaysProjectScope) {
    return resolveProjectSettingsScope(projectDir, projects);
  }
  return resolveGlobalSettingsScope();
}
