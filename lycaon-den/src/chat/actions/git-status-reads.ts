import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { GitRepoEntry, GitReposView } from "../../api/types.ts";
import { loadGitWorkspaceStatus, type GitWorkspaceStatus } from "./git-workspace-status.ts";
import { resolveScope } from "../../components/git-repo-scope.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf } from "../../store/load-state.ts";

type GitStatusFlight = {
  client: LycaonClient;
  promise: Promise<GitWorkspaceStatus | undefined>;
  isCurrent: () => boolean;
};

export type GitProjection = {
  /** Begin order across every projection, so a repository set never regresses to an older read's. */
  order: number;
  sessionViewEpoch: number;
  /** No newer projection, scope pin, or session view (or, for a read, board event) has replaced this one. */
  current: () => boolean;
};

const gitStatusFlights = new WeakMap<AppStore, Map<string, GitStatusFlight>>();
const gitProjections = new WeakMap<AppStore, GitProjection>();
const gitRepoSetOrders = new WeakMap<AppStore, number>();
const gitWrites = new WeakMap<AppStore, Set<Promise<unknown>>>();
let gitProjectionOrder = 0;
const [flightsRevision, setFlightsRevision] = createSignal(0);

/** True while a status read for this store is in flight; every flight ends. */
export function gitStatusRefreshPending(appStore: AppStore): boolean {
  flightsRevision();
  return (gitStatusFlights.get(appStore)?.size ?? 0) > 0;
}

/**
 * A board event during a write does not expire it: board-driven refreshes wait for the
 * write to settle and then read again, so they cannot publish pre-write status over it.
 */
function beginGitProjection(appStore: AppStore, kind: "read" | "write" = "read"): GitProjection {
  const epoch = appStore.state.sessionViewEpoch;
  const pin = appStore.state.gitScopePin;
  const boardEpoch = appStore.state.boardEventEpoch;
  const projection: GitProjection = {
    order: ++gitProjectionOrder,
    sessionViewEpoch: epoch,
    current: () => appStore.state.sessionViewEpoch === epoch &&
      (kind === "write" || appStore.state.boardEventEpoch === boardEpoch) &&
      appStore.state.gitScopePin === pin && gitProjections.get(appStore) === projection,
  };
  gitProjections.set(appStore, projection);
  return projection;
}

/** Runs a write under its own projection; reads that begin meanwhile wait until it settles. */
export function runGitWrite<T>(appStore: AppStore, write: (projection: GitProjection) => Promise<T>): Promise<T> {
  const run = write(beginGitProjection(appStore, "write"));
  let writes = gitWrites.get(appStore);
  if (!writes) {
    writes = new Set();
    gitWrites.set(appStore, writes);
  }
  writes.add(run);
  const settled = () => { writes.delete(run); };
  run.then(settled, settled);
  return run;
}

export function gitWriteInFlight(appStore: AppStore): boolean {
  return (gitWrites.get(appStore)?.size ?? 0) > 0;
}

/** Resolves once no write is in flight, including writes that begin while waiting. */
export async function settleGitWrites(appStore: AppStore): Promise<void> {
  for (let writes = gitWrites.get(appStore); writes && writes.size > 0;) {
    await Promise.allSettled([...writes]);
  }
}

/**
 * Board events, scope pins, and newer status reads do not change which repositories exist,
 * so a superseded read still publishes its set unless a later-begun projection already has.
 */
function applyGitRepoSet(appStore: AppStore, projection: GitProjection, view: GitReposView): void {
  if (appStore.state.sessionViewEpoch !== projection.sessionViewEpoch) return;
  if ((gitRepoSetOrders.get(appStore) ?? 0) > projection.order) return;
  gitRepoSetOrders.set(appStore, projection.order);
  appStore.actions.setGitRepos(view.repos, view.active_repo_id ?? "");
}

export function unavailableStatus(rootIds: string[] = []): GitWorkspaceStatus {
  return {
    available: false,
    repo_id: "",
    root_ids: rootIds,
    ahead: 0,
    behind: 0,
    dirty: false,
    staged_count: 0,
    unstaged_count: 0,
    changed_count: 0,
    files: [],
  };
}

export async function refreshGitRepos(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId?: string,
  projection: GitProjection = beginGitProjection(appStore),
): Promise<{ repos: GitRepoEntry[]; active_repo_id: string }> {
  if (!projectId.trim()) {
    return { repos: [], active_repo_id: "" };
  }
  const view = await client.listGitRepos(projectId, sessionId);
  applyGitRepoSet(appStore, projection, view);
  return { repos: view.repos, active_repo_id: view.active_repo_id ?? "" };
}

type GitStatusReadOptions = {
  repoId?: string;
  refreshSet?: boolean;
  /** A write's follow-up read, which completes even when it may no longer publish status. */
  projection?: GitProjection;
};

export async function refreshGitStatus(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId?: string,
  options?: GitStatusReadOptions,
): Promise<GitWorkspaceStatus | undefined> {
  if (!options?.projection && gitWriteInFlight(appStore)) {
    const epoch = appStore.state.sessionViewEpoch;
    const pin = appStore.state.gitScopePin;
    await settleGitWrites(appStore);
    // The caller chose its repository for the view and pin it saw.
    if (appStore.state.sessionViewEpoch !== epoch || appStore.state.gitScopePin !== pin) return undefined;
  }
  const key = [
    projectId.trim(),
    sessionId?.trim() ?? "",
    options?.repoId?.trim() ?? "",
    options?.refreshSet === false ? "status" : "repos+status",
  ].join("\u0000");
  let flights = gitStatusFlights.get(appStore);
  if (!flights) {
    flights = new Map();
    gitStatusFlights.set(appStore, flights);
  }
  const existing = flights.get(key);
  if (existing?.client === client && existing.isCurrent()) return existing.promise;
  const projection = options?.projection ?? beginGitProjection(appStore);
  const flight: Promise<GitWorkspaceStatus | undefined> = Promise.resolve().then(() =>
    loadGitStatus(
      appStore,
      client,
      projectId,
      sessionId,
      options,
      projection,
      isCurrent,
    ),
  );
  const isCurrent = () => projection.current() && flights?.get(key)?.promise === flight;
  flights.set(key, { client, promise: flight, isCurrent });
  setFlightsRevision((n) => n + 1);
  try {
    return await flight;
  } finally {
    if (flights.get(key)?.promise === flight) {
      flights.delete(key);
      setFlightsRevision((n) => n + 1);
    }
  }
}

async function loadGitStatus(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  options: GitStatusReadOptions | undefined,
  projection: GitProjection,
  isCurrent: () => boolean,
): Promise<GitWorkspaceStatus | undefined> {
  if (!projectId.trim()) return undefined;
  try {
    return await loadGitStatusRemote(appStore, client, projectId, sessionId, options, projection, isCurrent);
  } catch (err) {
    // Preserve read failures for the checkout error state.
    if (isCurrent()) appStore.actions.setGitStatusLoadFailed(err);
    return undefined;
  }
}

async function loadGitStatusRemote(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  options: GitStatusReadOptions | undefined,
  projection: GitProjection,
  isCurrent: () => boolean,
): Promise<GitWorkspaceStatus | undefined> {
  let repos = appStore.state.gitRepos ?? [];
  let active = appStore.state.gitActiveRepoId ?? "";
  if (options?.refreshSet !== false) {
    const view = await client.listGitRepos(projectId, sessionId);
    applyGitRepoSet(appStore, projection, view);
    // Mutation follow-up reads complete after navigation, without repainting it.
    if (!isCurrent() && !options?.projection) return undefined;
    repos = view.repos;
    active = view.active_repo_id ?? "";
  }
  const pin = appStore.state.gitScopePin ?? null;
  const scope = resolveScope(repos, active, pin);
  let repoId = (options?.repoId ?? "").trim();
  if (!repoId && scope.kind === "repo") {
    repoId = scope.repoId;
  }
  if (!repoId) {
    if (scope.kind === "root") {
      const status = unavailableStatus([scope.rootId]);
      if (!isCurrent()) return undefined;
      appStore.actions.setGitStatus(status);
      return status;
    }
    return valueOf(appStore.state.gitStatus);
  }
  const status = await loadGitWorkspaceStatus(client, projectId, repoId, sessionId);
  if (!isCurrent()) return undefined;
  appStore.actions.setGitStatus(status);
  return status;
}
