import type { SecurityOverview } from "../../api/types.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";

type ProjectSecurityClient = {
  getProjectSecurity(projectId: string, rootId?: string): Promise<SecurityOverview>;
};

export type ProjectSecuritySource = {
  client: ProjectSecurityClient;
  projectId: string;
  /** Attached folder; absent reads the project's primary folder. */
  rootId?: string;
};

/** One folder's security overview; surfaces reading the same folder share a record. */
export function createProjectSecurityQuery(source: () => ProjectSecuritySource | null) {
  return createSurfaceQuery({
    name: "project-security",
    source: () => {
      const next = source();
      if (!next) return null;
      const rootId = next.rootId?.trim() || undefined;
      return { ...next, rootId, key: rootId ? `${next.projectId}:${rootId}` : next.projectId };
    },
    scope: ({ projectId }) => projectId,
    load: async ({ client, projectId, rootId }) =>
      rootId ? client.getProjectSecurity(projectId, rootId) : client.getProjectSecurity(projectId),
    required: false,
  });
}

/** Scans are in flight: a full pass is running or a scanner is mid-run. */
export function securityOverviewRunning(overview: SecurityOverview | null | undefined): boolean {
  return overview?.running != null || (overview?.scanners.some((scanner) => scanner.running) ?? false);
}
