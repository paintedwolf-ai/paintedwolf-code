import { createSignal } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type {
  AgentWatch,
  AgentWatchListResponse,
  AgentWatchReceipt,
  ProjectWatch,
  ProjectWatchListResponse,
  StandingWatch,
  WatchEvent,
  WatchRun,
  WatchRunEvent,
} from "../api/types.ts";
import { persistAppStateInBackground } from "../store/app-state-background-write.ts";
import { getAppStateSnapshot } from "../store/app-state-snapshot.ts";

type ProjectWatchesView = {
  readonly projectId: string;
  readonly hydrated: boolean;
  readonly loading: boolean;
  readonly error: string | null;
  readonly standingWatches: readonly StandingWatch[];
  readonly standingById: ReadonlyMap<string, StandingWatch>;
  readonly liveAgentWatches: readonly AgentWatch[];
  readonly queuedRuns: readonly WatchRun[];
  readonly activeRunBySession: ReadonlyMap<
    string,
    { watch: StandingWatch; run: WatchRun }
  >;
  readonly recentlyEndedRuns: readonly WatchRun[];
  readonly hasUnpricedStandingWatch: boolean;
  readonly needsAttention: boolean;
};

type SessionWatchesView = {
  readonly sessionId: string;
  readonly hydrated: boolean;
  readonly loading: boolean;
  readonly error: string | null;
  readonly watches: readonly AgentWatch[];
  readonly byHandle: ReadonlyMap<string, AgentWatch>;
  readonly proposals: readonly AgentWatch[];
  readonly receiptsByHandle: ReadonlyMap<string, AgentWatchReceipt>;
};

type ProjectState = {
  standing: Map<string, StandingWatch>;
  agent: Map<string, AgentWatch>;
  revisions: Map<string, number>;
  hasUnpriced: boolean;
  hydrated: boolean;
  loading: boolean;
  error: string | null;
  generation: number;
};

type SessionState = {
  watches: Map<string, AgentWatch>;
  receipts: Map<string, AgentWatchReceipt>;
  revisions: Map<string, number>;
  hydrated: boolean;
  loading: boolean;
  error: string | null;
  generation: number;
};

const ENDED_WINDOW_MS = 24 * 3_600_000;
const projectStates = new Map<string, ProjectState>();
const sessionStates = new Map<string, SessionState>();

const [publishedProjects, setPublishedProjects] = createSignal<
  ReadonlyMap<string, ProjectWatchesView>
>(new Map());
const [publishedSessions, setPublishedSessions] = createSignal<
  ReadonlyMap<string, SessionWatchesView>
>(new Map());
const [hiddenOverflows, setHiddenOverflows] = createSignal<
  Readonly<Record<string, string>>
>(getAppStateSnapshot().hiddenProposalOverflows ?? {});

function getProjectState(projectId: string): ProjectState {
  let s = projectStates.get(projectId);
  if (!s) {
    s = {
      standing: new Map(),
      agent: new Map(),
      revisions: new Map(),
      hasUnpriced: false,
      hydrated: false,
      loading: false,
      error: null,
      generation: 0,
    };
    projectStates.set(projectId, s);
  }
  return s;
}

function getSessionState(sessionId: string): SessionState {
  let s = sessionStates.get(sessionId);
  if (!s) {
    s = {
      watches: new Map(),
      receipts: new Map(),
      revisions: new Map(),
      hydrated: false,
      loading: false,
      error: null,
      generation: 0,
    };
    sessionStates.set(sessionId, s);
  }
  return s;
}

function isEndedRunStatus(status: WatchRun["status"]): boolean {
  return (
    status !== "pending" &&
    status !== "queued" &&
    status !== "running" &&
    status !== "waiting_for_owner"
  );
}

function buildProjectView(
  projectId: string,
  s: ProjectState,
): ProjectWatchesView {
  const allStanding = [...s.standing.values()].sort((a, b) => {
    const byCreated = a.created_at.localeCompare(b.created_at);
    return byCreated !== 0 ? byCreated : a.watch_id.localeCompare(b.watch_id);
  });
  const standingWatches = allStanding.filter((w) => w.state !== "completed");
  const standingById = new Map(allStanding.map((w) => [w.watch_id, w]));
  const liveAgentWatches = [...s.agent.values()]
    .filter((w) => w.state === "live")
    .sort((a, b) => {
      const c = a.created_at.localeCompare(b.created_at);
      return c !== 0 ? c : a.handle.localeCompare(b.handle);
    });

  const queuedRuns: WatchRun[] = [];
  const activeRunBySession = new Map<
    string,
    { watch: StandingWatch; run: WatchRun }
  >();
  const endedRuns: WatchRun[] = [];
  const cutoffMs = Date.now() - ENDED_WINDOW_MS;

  for (const sw of allStanding) {
    const seenRunIds = new Set<string>();
    const runs = [
      ...(sw.active_run ? [sw.active_run] : []),
      ...(sw.recent_runs ?? []),
    ];
    for (const run of runs) {
      if (seenRunIds.has(run.run_id)) continue;
      seenRunIds.add(run.run_id);
      if (run.status === "queued") {
        queuedRuns.push(run);
      } else if (
        (run.status === "running" || run.status === "waiting_for_owner") &&
        run.session_id
      ) {
        activeRunBySession.set(run.session_id, { watch: sw, run });
      } else if (isEndedRunStatus(run.status) && run.ended_at) {
        const endedMs = Date.parse(run.ended_at);
        if (Number.isNaN(endedMs) || endedMs >= cutoffMs) {
          endedRuns.push(run);
        }
      }
    }
  }

  queuedRuns.sort((a, b) => a.scheduled_at.localeCompare(b.scheduled_at));
  endedRuns.sort((a, b) =>
    (b.ended_at ?? b.created_at).localeCompare(a.ended_at ?? a.created_at),
  );

  const needsAttention = standingWatches.some(
    (w) =>
      w.state === "disabled" ||
      (w.state === "paused" &&
        (w.state_reason === "consecutive_failures" ||
          w.state_reason === "target_unavailable")) ||
      w.active_run?.status === "waiting_for_owner",
  );

  return {
    projectId,
    hydrated: s.hydrated,
    loading: s.loading,
    error: s.error,
    standingWatches,
    standingById,
    liveAgentWatches,
    queuedRuns,
    activeRunBySession,
    recentlyEndedRuns: endedRuns,
    hasUnpricedStandingWatch: s.hasUnpriced,
    needsAttention,
  };
}

function buildSessionView(
  sessionId: string,
  s: SessionState,
): SessionWatchesView {
  const watches = [...s.watches.values()].sort((a, b) => {
    const c = a.created_at.localeCompare(b.created_at);
    return c !== 0 ? c : a.handle.localeCompare(b.handle);
  });
  const byHandle = new Map(watches.map((w) => [w.handle, w]));
  const proposals = watches.filter(
    (w) => w.state === "live" && w.standing_draft != null,
  );
  return {
    sessionId,
    hydrated: s.hydrated,
    loading: s.loading,
    error: s.error,
    watches,
    byHandle,
    proposals,
    receiptsByHandle: new Map(s.receipts),
  };
}

function publishProjectWatches(projectId: string): void {
  const cur = projectStates.get(projectId);
  const view = cur ? buildProjectView(projectId, cur) : undefined;
  setPublishedProjects((prev) => {
    const next = new Map(prev);
    if (view) next.set(projectId, view);
    else next.delete(projectId);
    return next;
  });
}

function publishSessionWatches(sessionId: string): void {
  const cur = sessionStates.get(sessionId);
  const view = cur ? buildSessionView(sessionId, cur) : undefined;
  setPublishedSessions((prev) => {
    const next = new Map(prev);
    if (view) next.set(sessionId, view);
    else next.delete(sessionId);
    return next;
  });
}

const EMPTY_PROJECT_VIEW = (projectId: string): ProjectWatchesView => ({
  projectId,
  hydrated: false,
  loading: false,
  error: null,
  standingWatches: [],
  standingById: new Map(),
  liveAgentWatches: [],
  queuedRuns: [],
  activeRunBySession: new Map(),
  recentlyEndedRuns: [],
  hasUnpricedStandingWatch: false,
  needsAttention: false,
});

const EMPTY_SESSION_VIEW = (sessionId: string): SessionWatchesView => ({
  sessionId,
  hydrated: false,
  loading: false,
  error: null,
  watches: [],
  byHandle: new Map(),
  proposals: [],
  receiptsByHandle: new Map(),
});

export function projectWatchesFor(projectId: string): ProjectWatchesView {
  const id = projectId.trim();
  if (!id) return EMPTY_PROJECT_VIEW("");
  return publishedProjects().get(id) ?? EMPTY_PROJECT_VIEW(id);
}

export function allPublishedProjectWatches(): ReadonlyMap<
  string,
  ProjectWatchesView
> {
  return publishedProjects();
}

export function standingWatchNeedsAttentionFor(projectId: string): boolean {
  return projectWatchesFor(projectId).needsAttention;
}

export function sessionWatchesFor(sessionId: string): SessionWatchesView {
  const id = sessionId.trim();
  if (!id) return EMPTY_SESSION_VIEW("");
  return publishedSessions().get(id) ?? EMPTY_SESSION_VIEW(id);
}

function applyProjectWatchesSnapshot(
  projectId: string,
  snap: ProjectWatchListResponse,
): void {
  const id = projectId.trim();
  if (!id) return;
  const s = getProjectState(id);
  s.standing.clear();
  s.agent.clear();
  for (const item of snap.watches) {
    s.revisions.set(item.watch_id, item.revision);
    if (item.standing_watch) {
      s.standing.set(item.watch_id, item.standing_watch);
    } else if (item.agent_watch) {
      s.agent.set(item.watch_id, item.agent_watch);
      upsertSessionWatchInternal(item.agent_watch);
    }
  }
  s.hasUnpriced = snap.has_unpriced_standing_watch;
  s.hydrated = true;
  s.loading = false;
  s.error = null;
  publishProjectWatches(id);
}

function upsertSessionWatchInternal(
  watch: AgentWatch,
  receipt?: AgentWatchReceipt,
): void {
  const sid = watch.session_id.trim();
  if (!sid) return;
  const s = getSessionState(sid);
  const prevRev = s.revisions.get(watch.watch_id) ?? 0;
  if (watch.revision < prevRev) return;
  s.revisions.set(watch.watch_id, watch.revision);
  s.watches.set(watch.handle, watch);
  const finalRec = receipt ?? watch.final_receipt;
  if (finalRec) s.receipts.set(watch.handle, finalRec);
  publishSessionWatches(sid);
}

function applySessionWatchesSnapshot(
  sessionId: string,
  snap: AgentWatchListResponse,
): void {
  const sid = sessionId.trim();
  if (!sid) return;
  const s = getSessionState(sid);
  s.watches.clear();
  for (const w of snap.watches) {
    s.revisions.set(w.watch_id, w.revision);
    s.watches.set(w.handle, w);
    if (w.final_receipt) s.receipts.set(w.handle, w.final_receipt);
  }
  s.hydrated = true;
  s.loading = false;
  s.error = null;
  publishSessionWatches(sid);
}

export function upsertStandingWatchInStore(sw: StandingWatch): void {
  const pid = sw.project_id.trim();
  if (!pid) return;
  const s = getProjectState(pid);
  const prevRev = s.revisions.get(sw.watch_id) ?? 0;
  if (sw.revision < prevRev) return;
  s.revisions.set(sw.watch_id, sw.revision);
  s.standing.set(sw.watch_id, sw);
  s.hasUnpriced = [...s.standing.values()].some(
    (w) => w.state === "enabled" && w.allow_unpriced,
  );
  publishProjectWatches(pid);
}

export function removeStandingWatchFromStore(
  projectId: string,
  watchId: string,
): void {
  const pid = projectId.trim();
  if (!pid) return;
  const s = projectStates.get(pid);
  if (!s) return;
  s.standing.delete(watchId);
  s.hasUnpriced = [...s.standing.values()].some(
    (w) => w.state === "enabled" && w.allow_unpriced,
  );
  publishProjectWatches(pid);
}

export function upsertAgentWatchInStore(
  watch: AgentWatch,
  receipt?: AgentWatchReceipt,
): void {
  upsertSessionWatchInternal(watch, receipt);
  const pid = watch.project_id.trim();
  if (pid && projectStates.has(pid)) {
    const ps = getProjectState(pid);
    ps.revisions.set(watch.watch_id, watch.revision);
    if (watch.state === "live") ps.agent.set(watch.watch_id, watch);
    else ps.agent.delete(watch.watch_id);
    publishProjectWatches(pid);
  }
}

export async function refreshProjectWatches(
  client: LycaonClient | null,
  projectId: string,
): Promise<void> {
  const pid = projectId.trim();
  if (!client || !pid) return;
  const s = getProjectState(pid);
  const gen = ++s.generation;
  s.loading = true;
  publishProjectWatches(pid);
  try {
    const res = await client.listProjectWatches(pid, { owner: "all" });
    if (s.generation !== gen) return;
    applyProjectWatchesSnapshot(pid, res);
  } catch (err) {
    if (s.generation !== gen) return;
    s.loading = false;
    s.error = err instanceof Error ? err.message : String(err);
    publishProjectWatches(pid);
  }
}

export function ensureProjectWatchesHydrated(
  client: LycaonClient | null,
  projectId: string,
): void {
  const pid = projectId.trim();
  if (!client || !pid) return;
  const s = getProjectState(pid);
  if (s.hydrated || s.loading || s.error !== null) return;
  void refreshProjectWatches(client, pid);
}

export async function refreshSessionWatches(
  client: LycaonClient | null,
  sessionId: string,
): Promise<void> {
  const sid = sessionId.trim();
  if (!client || !sid) return;
  const s = getSessionState(sid);
  const gen = ++s.generation;
  s.loading = true;
  publishSessionWatches(sid);
  try {
    const res = await client.listWatches(sid);
    if (s.generation !== gen) return;
    applySessionWatchesSnapshot(sid, res);
  } catch (err) {
    if (s.generation !== gen) return;
    s.loading = false;
    s.error = err instanceof Error ? err.message : String(err);
    publishSessionWatches(sid);
  }
}

export function ensureSessionWatchesHydrated(
  client: LycaonClient | null,
  sessionId: string,
): void {
  const sid = sessionId.trim();
  if (!client || !sid) return;
  const s = getSessionState(sid);
  if (s.hydrated || s.loading || s.error !== null) return;
  void refreshSessionWatches(client, sid);
}

function applyProjectedWatch(pw: ProjectWatch): void {
  if (pw.standing_watch) {
    upsertStandingWatchInStore(pw.standing_watch);
  } else if (pw.agent_watch) {
    upsertAgentWatchInStore(pw.agent_watch);
  }
}

export function applyWatchUpdatedEvent(
  ev: WatchEvent,
  client?: LycaonClient | null,
): void {
  const pid = ev.project_id.trim();
  if (ev.action === "deleted") {
    removeStandingWatchFromStore(pid, ev.watch_id);
    if (client && ev.session_id) {
      void refreshSessionWatches(client, ev.session_id);
    }
    return;
  }
  if (ev.watch) {
    applyProjectedWatch(ev.watch);
    return;
  }
  if (client && pid && projectStates.has(pid)) {
    void refreshProjectWatches(client, pid);
  }
  if (client && ev.session_id && sessionStates.has(ev.session_id)) {
    void refreshSessionWatches(client, ev.session_id);
  }
}

export function applyWatchRunUpdatedEvent(
  ev: WatchRunEvent,
  client?: LycaonClient | null,
): void {
  const pid = ev.project_id.trim();
  if (!pid) return;
  const s = projectStates.get(pid);
  if (s) {
    const sw = s.standing.get(ev.watch_id);
    if (sw) {
      const existingRuns = (sw.recent_runs ?? []).filter(
        (r) => r.run_id !== ev.run.run_id,
      );
      const isActive =
        ev.run.status === "running" || ev.run.status === "waiting_for_owner";
      const nextActive = isActive
        ? ev.run
        : sw.active_run?.run_id === ev.run.run_id
          ? undefined
          : sw.active_run;
      s.standing.set(ev.watch_id, {
        ...sw,
        active_run: nextActive,
        recent_runs: [ev.run, ...existingRuns],
        revision: Math.max(sw.revision, ev.revision),
      });
      publishProjectWatches(pid);
    }
  }
  if (client && projectStates.has(pid)) {
    void refreshProjectWatches(client, pid);
  }
}

export function invalidateWatchesForReconnect(
  client?: LycaonClient | null,
): void {
  for (const [pid, ps] of projectStates.entries()) {
    ps.hydrated = false;
    ps.loading = false;
    ps.error = null;
    if (client) void refreshProjectWatches(client, pid);
  }
  for (const [sid, ss] of sessionStates.entries()) {
    ss.hydrated = false;
    ss.loading = false;
    ss.error = null;
    if (client) void refreshSessionWatches(client, sid);
  }
}

function overflowWatermark(proposals: readonly AgentWatch[]): string {
  if (proposals.length < 4) return "";
  const firstHidden = proposals[2]?.handle ?? "";
  const newestHidden = proposals[proposals.length - 1]?.handle ?? "";
  return `${firstHidden}:${newestHidden}`;
}

export function isProposalOverflowHidden(
  sessionId: string,
  proposals: readonly AgentWatch[],
): boolean {
  if (proposals.length < 4) return false;
  const stored = hiddenOverflows()[sessionId.trim()];
  if (!stored) return false;
  return stored === overflowWatermark(proposals);
}

export function hideProposalOverflow(
  sessionId: string,
  proposals: readonly AgentWatch[],
): void {
  const sid = sessionId.trim();
  const mark = overflowWatermark(proposals);
  if (!sid || !mark) return;
  const next = { ...hiddenOverflows(), [sid]: mark };
  setHiddenOverflows(next);
  void persistAppStateInBackground({ hiddenProposalOverflows: next });
}

export function retainProjectWatchesProject(projectId: string | null): void {
  const keep = projectId?.trim() ?? "";
  if (!keep) {
    projectStates.clear();
    sessionStates.clear();
    setPublishedProjects(new Map());
    setPublishedSessions(new Map());
    setHiddenOverflows(getAppStateSnapshot().hiddenProposalOverflows ?? {});
    return;
  }
  let changed = false;
  for (const id of [...projectStates.keys()]) {
    if (id === keep) continue;
    projectStates.delete(id);
    changed = true;
  }
  if (changed) {
    setPublishedProjects(
      (prev) => new Map([...prev].filter(([id]) => id === keep)),
    );
  }
}
