import type { Project } from "../api/types.ts";
import type { ProjectsStore } from "../store/projects-store.ts";

export const emptyProjects: readonly Project[] = [];

export function mockProjectsStore(
  projects: readonly Project[] = emptyProjects,
): ProjectsStore {
  return {
    state: { projects: [...projects], loaded: true },
    byId: (id: string) => projects.find((p) => p.id === id),
    summaries: () => [],
  } as unknown as ProjectsStore;
}
