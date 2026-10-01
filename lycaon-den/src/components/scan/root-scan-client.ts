import type { LycaonClient } from "../../api/client.ts";

// Surface queries key retained reads by client identity, so one folder keeps one client.
const bound = new WeakMap<LycaonClient, Map<string, LycaonClient>>();

/** Every ledger action uses the same attached folder as the rows on screen. */
export function rootScanClient(client: LycaonClient, rootId: string): LycaonClient {
  let byRoot = bound.get(client);
  if (!byRoot) {
    byRoot = new Map();
    bound.set(client, byRoot);
  }
  const existing = byRoot.get(rootId);
  if (existing) return existing;
  const scoped: LycaonClient = {
    ...client,
    listCodeScans: (projectId, query) => client.listCodeScans(projectId, { ...query, root_id: rootId }),
    getProjectSecurity: (projectId) => client.getProjectSecurity(projectId, rootId),
    queryProjectFindings: (projectId, request) => client.queryProjectFindings(projectId, request, rootId),
    listProjectFindingIgnores: (projectId) => client.listProjectFindingIgnores(projectId, rootId),
    createProjectFindingIgnore: (projectId, entry) => client.createProjectFindingIgnore(projectId, entry, rootId),
    deleteProjectFindingIgnore: (projectId, entryId) => client.deleteProjectFindingIgnore(projectId, entryId, rootId),
    exportProjectFindings: (projectId, request) => client.exportProjectFindings(projectId, request, rootId),
    startFullScan: (projectId, request) => client.startFullScan(projectId, request, rootId),
  };
  byRoot.set(rootId, scoped);
  return scoped;
}
