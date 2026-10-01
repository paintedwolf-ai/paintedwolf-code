import type { Project } from "../api/types.ts";
import {
  CACHED_PROJECTS_CAP,
  type CachedProject,
} from "../../shared/app-state-types.ts";

export function projectToCached(project: Project): CachedProject {
  return {
    id: project.id,
    name: project.name,
    roots: project.roots.map((r) => ({
      id: r.id,
      path: r.path,
      label: r.label,
      is_primary: r.is_primary,
      added_at: r.added_at,
      kind: r.kind,
    })),
    roots_generation: project.roots_generation,
    session_count: project.session_count,
    starred: project.starred,
    is_draft: project.is_draft,
    promotion: project.promotion,
    last_activity_at: project.last_activity_at,
    last_opened_at: project.last_opened_at,
    created_at: project.created_at,
  };
}

export function cachedToProject(cached: CachedProject): Project {
  return {
    id: cached.id,
    name: cached.name,
    roots: cached.roots.map((r) => ({
      id: r.id,
      path: r.path,
      label: r.label,
      is_primary: r.is_primary,
      added_at: r.added_at,
      kind: r.kind,
    })),
    roots_generation: cached.roots_generation,
    session_count: cached.session_count,
    starred: cached.starred,
    is_draft: cached.is_draft,
    promotion: cached.promotion,
    last_activity_at: cached.last_activity_at,
    last_opened_at: cached.last_opened_at,
    created_at: cached.created_at,
  };
}

/** Recent-first wire projects trimmed for disk persistence. */
export function cachedProjectsFromRegistry(
  projects: readonly Project[],
): CachedProject[] {
  return [...projects]
    .sort(
      (a, b) =>
        Date.parse(b.last_opened_at) - Date.parse(a.last_opened_at),
    )
    .slice(0, CACHED_PROJECTS_CAP)
    .map(projectToCached);
}
