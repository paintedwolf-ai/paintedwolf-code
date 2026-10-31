/** Project-scoped live state for background commands in the Running context pane. */

import { createSignal } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type {
  ProjectBackgroundEvent,
  ProjectBackgroundProcess,
  ProjectBackgroundProcessListResponse,
  SessionEvent,
} from "../api/types.ts";

export type ProjectBackgroundChatGroup = {
  readonly sessionId: string;
  readonly sessionTitle?: string;
  readonly sessionArchived: boolean;
  readonly processes: readonly ProjectBackgroundProcess[];
};

export type ProjectBackgroundView = {
  readonly projectId: string;
  readonly hydrated: boolean;
  readonly loading: boolean;
  readonly error: string | null;
  readonly runningCount: number;
  readonly runningGroups: readonly ProjectBackgroundChatGroup[];
  readonly recentlyEnded: readonly ProjectBackgroundProcess[];
  readonly stoppingIds: ReadonlySet<string>;
  readonly stopErrors: ReadonlyMap<string, string>;
};

type SessionPatch = {
  session_title?: string;
  session_archived?: boolean;
};

type ProjectState = {
  processes: Map<string, ProjectBackgroundProcess>;
  /** Newest event revision applied per process id since the stream opened. */
  revisions: Map<string, number>;
  /** Events at or below the last snapshot's revision are already reflected. */
  floor: number;
  generation: number;
  hydrated: boolean;
  loading: boolean;
  error: string | null;
  stoppingIds: Set<string>;
  stopErrors: Map<string, string>;
  sessionPatches: Map<string, SessionPatch>;
};

const MAX_RECENTLY_ENDED = 32;
const EMPTY_SET: ReadonlySet<string> = new Set();
const EMPTY_MAP: ReadonlyMap<string, string> = new Map();

function emptyView(projectId: string): ProjectBackgroundView {
  return {
    projectId,
    hydrated: false,
    loading: false,
    error: null,
    runningCount: 0,
    runningGroups: [],
    recentlyEnded: [],
    stoppingIds: EMPTY_SET,
    stopErrors: EMPTY_MAP,
  };
}

const states = new Map<string, ProjectState>();
const [published, setPublished] = createSignal<
  ReadonlyMap<string, ProjectBackgroundView>
>(new Map());
const projectBackgroundEventListeners = new Set<
  (ev: ProjectBackgroundEvent) => void
>();

/** Subscribes to live `project.background` events as they are applied. */
export function onProjectBackgroundEvent(
  cb: (ev: ProjectBackgroundEvent) => void,
): () => void {
  projectBackgroundEventListeners.add(cb);
  return () => {
    projectBackgroundEventListeners.delete(cb);
  };
}

function state(projectId: string): ProjectState {
  let found = states.get(projectId);
  if (!found) {
    found = {
      processes: new Map(),
      revisions: new Map(),
      floor: 0,
      generation: 0,
      hydrated: false,
      loading: false,
      error: null,
      stoppingIds: new Set(),
      stopErrors: new Map(),
      sessionPatches: new Map(),
    };
    states.set(projectId, found);
  }
  return found;
}

function applySessionPatchToProcess(
  proc: ProjectBackgroundProcess,
  patch: SessionPatch | undefined,
): ProjectBackgroundProcess {
  if (!patch) return proc;
  let next = proc;
  if (
    patch.session_title !== undefined &&
    patch.session_title !== next.session_title
  ) {
    next = { ...next, session_title: patch.session_title };
  }
  if (
    patch.session_archived !== undefined &&
    patch.session_archived !== next.session_archived
  ) {
    next = { ...next, session_archived: patch.session_archived };
  }
  return next;
}

function compareRunningProcesses(
  a: ProjectBackgroundProcess,
  b: ProjectBackgroundProcess,
): number {
  const byStart = a.started_at.localeCompare(b.started_at);
  if (byStart !== 0) return byStart;
  return a.process_id.localeCompare(b.process_id);
}

function compareEndedProcesses(
  a: ProjectBackgroundProcess,
  b: ProjectBackgroundProcess,
): number {
  const endA = a.ended_at ?? a.started_at;
  const endB = b.ended_at ?? b.started_at;
  const byEnd = endB.localeCompare(endA);
  if (byEnd !== 0) return byEnd;
  const byStart = b.started_at.localeCompare(a.started_at);
  if (byStart !== 0) return byStart;
  return a.process_id.localeCompare(b.process_id);
}

function buildView(projectId: string, s: ProjectState): ProjectBackgroundView {
  const running: ProjectBackgroundProcess[] = [];
  const ended: ProjectBackgroundProcess[] = [];
  for (const proc of s.processes.values()) {
    if (proc.running) {
      running.push(proc);
    } else {
      ended.push(proc);
    }
  }
  running.sort(compareRunningProcesses);
  ended.sort(compareEndedProcesses);

  const groupsBySession = new Map<
    string,
    {
      sessionId: string;
      sessionTitle?: string;
      sessionArchived: boolean;
      processes: ProjectBackgroundProcess[];
    }
  >();
  for (const proc of running) {
    let group = groupsBySession.get(proc.session_id);
    if (!group) {
      group = {
        sessionId: proc.session_id,
        sessionTitle: proc.session_title,
        sessionArchived: proc.session_archived,
        processes: [],
      };
      groupsBySession.set(proc.session_id, group);
    } else {
      if (proc.session_title !== undefined) {
        group.sessionTitle = proc.session_title;
      }
      group.sessionArchived = proc.session_archived;
    }
    group.processes.push(proc);
  }

  return {
    projectId,
    hydrated: s.hydrated,
    loading: s.loading,
    error: s.error,
    runningCount: running.length,
    runningGroups: [...groupsBySession.values()],
    recentlyEnded: ended.slice(0, MAX_RECENTLY_ENDED),
    stoppingIds: new Set(s.stoppingIds),
    stopErrors: new Map(s.stopErrors),
  };
}

function publish(projectId: string): void {
  const next = new Map(published());
  const current = states.get(projectId);
  if (current) {
    next.set(projectId, buildView(projectId, current));
  } else {
    next.delete(projectId);
  }
  setPublished(next);
}

/** Reactive background command state for one project. */
export function projectBackgroundFor(projectId: string): ProjectBackgroundView {
  const id = projectId.trim();
  if (!id) return emptyView("");
  return published().get(id) ?? emptyView(id);
}

/** Reactive count of running background commands in a project for nav badges. */
export function runningBackgroundCountFor(projectId: string): number {
  const id = projectId.trim();
  if (!id) return 0;
  return published().get(id)?.runningCount ?? 0;
}

/** Applies a single `project_background` SSE event with revision reconciliation. */
export function applyProjectBackgroundEvent(
  event: ProjectBackgroundEvent,
): void {
  const projectId = event.project_id.trim();
  const procId = (event.process?.process_id ?? event.process_id).trim();
  if (!projectId || !procId) return;
  const s = state(projectId);
  if (event.revision <= (s.revisions.get(procId) ?? s.floor)) return;
  s.revisions.set(procId, event.revision);

  const existing = s.processes.get(procId);
  let nextProc: ProjectBackgroundProcess | undefined = event.process;
  if (!nextProc && existing) {
    nextProc = {
      ...existing,
      running: event.running,
      ports: event.ports ?? existing.ports,
      ...(event.exit_code !== undefined ? { exit_code: event.exit_code } : {}),
      ...(event.termination_reason !== undefined
        ? { termination_reason: event.termination_reason }
        : {}),
    };
  }
  if (!nextProc) return;

  const patched = applySessionPatchToProcess(
    nextProc,
    s.sessionPatches.get(nextProc.session_id),
  );
  s.processes.set(procId, patched);
  if (!patched.running || event.action === "ended") {
    s.stoppingIds.delete(procId);
    s.stopErrors.delete(procId);
  }
  publish(projectId);
  for (const listener of projectBackgroundEventListeners) {
    listener(event);
  }
}

/** Merges a snapshot from `GET /v1/projects/{id}/background` with newer live events. */
export function applyProjectBackgroundSnapshot(
  projectId: string,
  snapshot: ProjectBackgroundProcessListResponse,
): void {
  const id = projectId.trim();
  if (!id) return;
  const s = state(id);
  const nextProcesses = new Map<string, ProjectBackgroundProcess>();

  for (const raw of snapshot.processes) {
    s.sessionPatches.delete(raw.session_id);
    nextProcesses.set(raw.process_id, raw);
  }

  for (const [procId, revision] of s.revisions) {
    if (revision <= snapshot.revision) {
      s.revisions.delete(procId);
      continue;
    }
    const newer = s.processes.get(procId);
    if (newer) {
      const snapProc = nextProcesses.get(procId);
      const merged: ProjectBackgroundProcess = snapProc
        ? {
            ...newer,
            session_title: newer.session_title ?? snapProc.session_title,
            session_archived: snapProc.session_archived,
          }
        : newer;
      nextProcesses.set(
        procId,
        applySessionPatchToProcess(
          merged,
          s.sessionPatches.get(merged.session_id),
        ),
      );
    }
  }

  for (const [procId, proc] of nextProcesses) {
    nextProcesses.set(
      procId,
      applySessionPatchToProcess(proc, s.sessionPatches.get(proc.session_id)),
    );
  }

  for (const procId of [...s.stoppingIds]) {
    const proc = nextProcesses.get(procId);
    if (!proc || !proc.running) {
      s.stoppingIds.delete(procId);
      s.stopErrors.delete(procId);
    }
  }
  for (const procId of [...s.stopErrors.keys()]) {
    const proc = nextProcesses.get(procId);
    if (!proc || !proc.running) {
      s.stopErrors.delete(procId);
    }
  }

  s.processes = nextProcesses;
  s.floor = snapshot.revision;
  s.hydrated = true;
  s.loading = false;
  s.error = null;
  publish(id);
}

/**
 * Updates owning chat metadata (`session_title` / `session_archived`) across all
 * background commands for a session in the given project.
 */
export function patchProjectBackgroundSession(
  projectId: string,
  sessionId: string,
  patch: SessionPatch,
): void {
  const id = projectId.trim();
  const sid = sessionId.trim();
  if (!id || !sid) return;
  const s = state(id);
  const prevPatch = s.sessionPatches.get(sid) ?? {};
  const nextPatch: SessionPatch = {
    ...prevPatch,
    ...(patch.session_title !== undefined
      ? { session_title: patch.session_title }
      : {}),
    ...(patch.session_archived !== undefined
      ? { session_archived: patch.session_archived }
      : {}),
  };
  s.sessionPatches.set(sid, nextPatch);

  let changed = false;
  for (const [procId, proc] of s.processes) {
    if (proc.session_id !== sid) continue;
    const updated = applySessionPatchToProcess(proc, nextPatch);
    if (updated !== proc) {
      s.processes.set(procId, updated);
      changed = true;
    }
  }
  if (changed) publish(id);
}

/**
 * Applies a live `session` SSE event to any background command groups owned by
 * that chat, and refreshes from the host when session metadata may have changed.
 */
export function applyProjectBackgroundSessionEvent(
  event: SessionEvent & { archived?: boolean },
  client?: LycaonClient | null,
): void {
  const projectId = event.project_id.trim();
  const sessionId = event.id.trim();
  if (!projectId || !sessionId) return;
  const s = states.get(projectId);
  if (!s) return;

  let hasSessionProcesses = false;
  for (const proc of s.processes.values()) {
    if (proc.session_id === sessionId) {
      hasSessionProcesses = true;
      break;
    }
  }
  if (!hasSessionProcesses) return;

  if (event.title !== undefined || event.archived !== undefined) {
    patchProjectBackgroundSession(projectId, sessionId, {
      ...(event.title !== undefined ? { session_title: event.title } : {}),
      ...(event.archived !== undefined
        ? { session_archived: event.archived }
        : {}),
    });
  }

  if (client && event.action === "updated") {
    void refreshProjectBackgroundMetadata(client, projectId);
  }
}

async function refreshProjectBackgroundMetadata(
  client: LycaonClient,
  projectId: string,
): Promise<void> {
  const id = projectId.trim();
  if (!id) return;
  const s = states.get(id);
  if (!s) return;
  const generation = s.generation;
  try {
    const snapshot = await client.listProjectBackgroundProcesses(id);
    if (states.get(id) !== s || s.generation !== generation) return;
    applyProjectBackgroundSnapshot(id, snapshot);
  } catch {
    // Keep existing rows when a background metadata refresh fails.
  }
}

/**
 * Re-syncs a project's background processes from the host. Keeps existing rows
 * visible while loading and discards late responses if the project or generation
 * changes before the response arrives.
 */
export async function resyncProjectBackground(
  client: LycaonClient,
  projectId: string,
): Promise<void> {
  const id = projectId.trim();
  if (!id) return;
  const s = state(id);
  const generation = ++s.generation;
  s.revisions.clear();
  s.floor = 0;
  s.loading = true;
  s.error = null;
  publish(id);
  try {
    const snapshot = await client.listProjectBackgroundProcesses(id);
    if (states.get(id) !== s || s.generation !== generation) return;
    applyProjectBackgroundSnapshot(id, snapshot);
  } catch (err) {
    if (states.get(id) !== s || s.generation !== generation) return;
    s.loading = false;
    s.error = err instanceof Error ? err.message : String(err);
    publish(id);
  }
}

/** Hydrates the project's background commands if not yet hydrated or loading. */
export function ensureProjectBackgroundHydrated(
  client: LycaonClient | null | undefined,
  projectId: string,
): void {
  const id = projectId.trim();
  if (!client || !id) return;
  const s = state(id);
  if (s.hydrated || s.loading) return;
  void resyncProjectBackground(client, id);
}

/**
 * Requests the host to stop a running background command.
 * Keeps the row in pending-stop state until the host reports the process has
 * ended (`running: false`), and never moves it to Recently ended merely because
 * the stop request was accepted.
 */
export async function stopProjectBackgroundCommand(
  client: LycaonClient,
  projectId: string,
  processId: string,
): Promise<boolean> {
  const id = projectId.trim();
  const procId = processId.trim();
  if (!id || !procId) return false;
  const s = state(id);
  if (s.stoppingIds.has(procId)) return false;

  s.stoppingIds.add(procId);
  s.stopErrors.delete(procId);
  publish(id);

  try {
    await client.stopProjectBackgroundProcess(id, procId);
    if (states.get(id) !== s) return true;
    const current = s.processes.get(procId);
    if (!current || !current.running) {
      s.stoppingIds.delete(procId);
      publish(id);
    }
    return true;
  } catch (err) {
    if (states.get(id) !== s) return false;
    s.stoppingIds.delete(procId);
    const message =
      err instanceof Error && err.message.trim()
        ? err.message.trim()
        : "Could not stop background command.";
    s.stopErrors.set(procId, message);
    publish(id);
    return false;
  }
}

/** Drops background command state for every project other than the active one. */
export function retainProjectBackgroundProject(projectId: string | null): void {
  const keep = projectId?.trim() ?? "";
  let changed = false;
  for (const id of [...states.keys()]) {
    if (id === keep) continue;
    states.delete(id);
    changed = true;
  }
  if (changed) {
    setPublished(
      new Map([...published()].filter(([id]) => id === keep)),
    );
  }
}

export function resetProjectBackgroundForTests(): void {
  states.clear();
  setPublished(new Map());
}

/** Tooltip and aria-label for a chat row's live background command count. */
export function backgroundRunningLabel(count: number): string {
  return `${count} background command${count === 1 ? "" : "s"} running`;
}

/**
 * Resolves the live background command count for a chat row in `projectId`,
 * combining `project-background-store` updates with the row's `background_running`
 * summary count when the project store has not yet hydrated.
 */
export function sessionRunningBackgroundCount(
  projectId: string | null | undefined,
  sessionId: string | null | undefined,
  fallbackCount?: number,
): number {
  const pid = projectId?.trim() ?? "";
  const sid = sessionId?.trim() ?? "";
  const fallback = Math.max(0, fallbackCount ?? 0);
  if (!pid || !sid) return fallback;
  const view = projectBackgroundFor(pid);
  const liveGroup = view.runningGroups.find((g) => g.sessionId === sid);
  if (view.hydrated) {
    return liveGroup?.processes.length ?? 0;
  }
  if (liveGroup) {
    return Math.max(fallback, liveGroup.processes.length);
  }
  const endedForSession = view.recentlyEnded.filter((p) => p.session_id === sid).length;
  if (endedForSession > 0) {
    return Math.max(0, fallback - endedForSession);
  }
  return fallback;
}
