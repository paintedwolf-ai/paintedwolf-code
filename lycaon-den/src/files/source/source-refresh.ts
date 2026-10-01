import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";

export const SOURCE_REFRESH_IDLE_MS = 400;
export const SOURCE_REFRESH_MAX_WAIT_MS = 2000;

export type SourceRefresh = { watchCoverage: boolean };
type Refresh = (request: SourceRefresh) => Promise<void>;

const refreshByProject = new Map<
  string,
  { scheduler: ReturnType<typeof createBoundedDebouncedAsyncScheduler>; watchCoverage: boolean }
>();

export function setSourceRefresh(
  projectId: string,
  refresh: Refresh | null,
): void {
  const id = projectId.trim();
  if (!id) return;
  refreshByProject.get(id)?.scheduler.cancel();
  if (!refresh) {
    refreshByProject.delete(id);
    return;
  }
  const entry = {
    watchCoverage: false,
    scheduler: createBoundedDebouncedAsyncScheduler(async () => {
      const watchCoverage = entry.watchCoverage;
      entry.watchCoverage = false;
      await refresh({ watchCoverage });
    }, SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS),
  };
  refreshByProject.set(id, entry);
}

/** Coverage invalidations survive coalescing with ordinary refreshes. */
export function requestSourceRefresh(projectId: string, options?: Partial<SourceRefresh>): void {
  const entry = refreshByProject.get(projectId.trim());
  if (!entry) return;
  entry.watchCoverage ||= options?.watchCoverage === true;
  entry.scheduler.schedule();
}

export function resetSourceRefreshForTests(): void {
  for (const entry of refreshByProject.values()) entry.scheduler.cancel();
  refreshByProject.clear();
}
