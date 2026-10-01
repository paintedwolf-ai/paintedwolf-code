import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { batch } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";

import { latestWalkTurnStart, walkChapterStart } from "./walk-chapters.ts";
import { invalidateWalkLoads, loadWalk, resetWalkLoadsForTests } from "./walk-loader.ts";
import { resetWalkGitLoadsForTests } from "./walk-git-loading.ts";
import { prefetchWalkNeighbors, resetWalkPrefetchForTests } from "./walk-prefetch.ts";
import {
  clampWalkIndex,
  EMPTY_WALK,
  walkStepAt,
  walkStepIndexForFile,
  type Walk,
  type WalkFileTarget,
  type WalkStep,
} from "./walk-model.ts";
import {
  loadSourceComparison,
  resetSourceComparisonCacheForTests,
} from "../source/source-comparison-cache.ts";
import { reportSurfaceFailure, type SurfaceFailureCopy } from "../../notices/surface-failure.ts";

const WALK_UNAVAILABLE: SurfaceFailureCopy = {
  code: "walk_unavailable",
  title: "Walk unavailable",
  suggestedAction: "Reopen the walk to try again.",
};
const WALK_REFRESH_FAILED: SurfaceFailureCopy = {
  code: "walk_refresh_failed",
  title: "Walk not refreshed",
  suggestedAction: "Your place is saved. Reopen the walk to load the latest steps.",
};
const WALK_STEP_UNAVAILABLE: SurfaceFailureCopy = {
  code: "walk_step_unavailable",
  title: "Walk step unavailable",
  suggestedAction: "Pick another step, or reopen the walk.",
};

type WalkStatus = "idle" | "loading" | "ready" | "error";

type WalkState = {
  notice: string | null;
  active: boolean;
  /** Session scope for comparison requests. */
  sessionId: string;
  /** -1 when the walk has no steps. */
  at: number;
  /** Requested playhead; differs from at only while its snapshot settles. */
  targetAt: number;
  /** Advances when explicit navigation has a presentation ready. */
  selectionRevision: number;
  /** An explicit selection awaiting application by the Files view. */
  focusPending: boolean;
  walk: Walk;
  /** The effect step's comparison; null on a group step. */
  comparison: ComparisonSnapshot | null;
  status: WalkStatus;
  unseenStepKeys: readonly string[];
};

/** Coalesces refreshes during a write burst. */
const REFRESH_THROTTLE_MS = 600;

/** A slow refresh stretches the next delay by this multiple of its duration. */
const REFRESH_DUTY_FACTOR = 3;

const REFRESH_DELAY_CAP_MS = 5000;

const states = new Map<string, WalkState>();
const epochs = new Map<string, number>();
const selectionRequests = new Map<string, number>();
const listeners = new Set<(projectId: string) => void>();
const clients = new Map<string, LycaonClient>();
const refreshTimers = new Map<string, ReturnType<typeof setTimeout>>();
const refreshing = new Set<string>();
const lastRefreshMs = new Map<string, number>();
const enteringEpochs = new Map<string, number>();
const pendingRefresh = new Set<string>();

function isEntering(projectId: string): boolean {
  return enteringEpochs.get(projectId) === epochs.get(projectId);
}

function finishEnter(projectId: string, epoch: number): void {
  if (enteringEpochs.get(projectId) === epoch) enteringEpochs.delete(projectId);
}

function idle(): WalkState {
  return {
    notice: null,
    active: false,
    sessionId: "",
    at: -1,
    targetAt: -1,
    selectionRevision: 0,
    focusPending: false,
    walk: EMPTY_WALK,
    comparison: null,
    status: "idle",
    unseenStepKeys: [],
  };
}

export function subscribeWalk(fn: (projectId: string) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function sameStep(a: WalkStep, b: WalkStep): boolean {
  if (a.key !== b.key || a.label !== b.label || a.kind !== b.kind) return false;
  if (a.kind === "effect" || b.kind === "effect") {
    return a.toolCallId === b.toolCallId;
  }
  if (a.kind === "command" && b.kind === "command" && a.command.state !== b.command.state) {
    return false;
  }
  if (a.effects.length !== b.effects.length) return false;
  for (let i = 0; i < a.effects.length; i += 1) {
    if (a.effects[i]!.id !== b.effects[i]!.id) return false;
  }
  return true;
}

function sameSteps(
  a: readonly WalkStep[],
  b: readonly WalkStep[],
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (!sameStep(a[i]!, b[i]!)) return false;
  }
  return true;
}

function flushPendingRefresh(projectId: string): void {
  const id = projectId.trim();
  if (!id || !pendingRefresh.has(id)) return;
  if (!walkState(id).active) {
    pendingRefresh.delete(id);
    return;
  }
  if (isEntering(id) || refreshing.has(id)) return;
  pendingRefresh.delete(id);
  requestWalkRefresh(id);
}

function sameWalkState(a: WalkState, b: WalkState): boolean {
  return (
    a.active === b.active &&
    a.sessionId === b.sessionId &&
    a.at === b.at &&
    a.targetAt === b.targetAt &&
    a.selectionRevision === b.selectionRevision &&
    a.focusPending === b.focusPending &&
    a.comparison === b.comparison &&
    a.status === b.status &&
    a.notice === b.notice &&
    a.unseenStepKeys.join("\0") === b.unseenStepKeys.join("\0") &&
    JSON.stringify(a.walk.chapters) === JSON.stringify(b.walk.chapters) &&
    a.walk.baseline === b.walk.baseline &&
    sameSteps(a.walk.steps, b.walk.steps)
  );
}

function commit(projectId: string, next: WalkState): WalkState {
  const held = states.get(projectId);
  if (held && sameWalkState(held, next)) return held;
  states.set(projectId, next);
  batch(() => {
    for (const fn of listeners) fn(projectId);
  });
  return next;
}

export function walkState(projectId: string): WalkState {
  return states.get(projectId.trim()) ?? idle();
}

export function isWalking(projectId: string): boolean {
  return walkState(projectId).active;
}

export function currentWalkStep(projectId: string): WalkStep | null {
  const s = walkState(projectId);
  if (!s.active) return null;
  return walkStepAt(s.walk, s.at);
}

/** Consumes focus only for the completed selection Files is applying. */
export function consumeWalkFocusRequest(projectId: string, revision: number): boolean {
  const id = projectId.trim();
  const current = walkState(id);
  if (!current.active || !current.focusPending || current.selectionRevision !== revision) return false;
  commit(id, { ...current, focusPending: false });
  return true;
}

export function walkNewStepCount(projectId: string): number {
  const s = walkState(projectId);
  if (!s.active) return 0;
  return s.unseenStepKeys.length;
}

export function walkNewTurnCount(projectId: string): number {
  const s = walkState(projectId);
  const unseen = new Set(s.unseenStepKeys);
  return s.walk.chapters.filter((chapter) => chapter.turn > 0 &&
    chapter.stepKeys.every((key) => unseen.has(key))).length;
}

function claim(id: string): number {
  const next = (epochs.get(id) ?? 0) + 1;
  epochs.set(id, next);
  return next;
}

/** An outside edit keeps its place when refresh groups it into a run. */
function stepIndexAfterRefresh(walk: Walk, previous: WalkStep): number {
  const byKey = walk.steps.findIndex((step) => step.key === previous.key);
  if (byKey >= 0 || previous.kind !== "effect") return byKey;
  const effectId = previous.effect.id;
  return walk.steps.findIndex((step) =>
    step.kind === "outside" && step.effects.some((effect) => effect.id === effectId)
  );
}

/** A group step presents its page and has no comparison to load. */
async function loadStepComparison(
  client: LycaonClient,
  projectId: string,
  sessionId: string,
  step: WalkStep | null,
): Promise<ComparisonSnapshot | null> {
  if (!step || step.kind !== "effect") return null;
  return loadSourceComparison(
    client,
    projectId,
    { effectId: step.effect.id },
    sessionId,
  );
}

export type EnterWalkOptions = {
  transition?: "retain" | "replace";
  startMessageId?: string;
};

export async function enterWalk(
  projectId: string,
  client: LycaonClient | null | undefined,
  sessionId: string,
  initialFile?: WalkFileTarget | null,
  options: EnterWalkOptions = {},
): Promise<WalkState> {
  const id = projectId.trim();
  const sid = sessionId.trim();
  if (!id || !sid) return walkState(projectId);

  const epoch = claim(id);
  enteringEpochs.set(id, epoch);
  selectionRequests.set(id, (selectionRequests.get(id) ?? 0) + 1);
  refreshing.delete(id);
  const held = walkState(id);
  const retainPresentation =
    options.transition !== "replace" && held.active && held.status !== "loading";
  if (!retainPresentation) {
    commit(id, {
      ...idle(),
      active: true,
      sessionId: sid,
      status: "loading",
    });
  }

  if (!client) {
    finishEnter(id, epoch);
    clients.delete(id);
    pendingRefresh.delete(id);
    reportSurfaceFailure(WALK_UNAVAILABLE, "Not connected.", id);
    return commit(id, retainPresentation
      ? { ...held, status: "error" }
      : {
          ...idle(),
          active: true,
          sessionId: sid,
          status: "error",
        });
  }
  clients.set(id, client);

  try {
    const walk = await loadWalk(client, id, sid);
    if (epochs.get(id) !== epoch) return walkState(id);
    const chapter = options.startMessageId
      ? walk.chapters.find((value) => value.messageId === options.startMessageId)
      : null;
    if (options.startMessageId && !chapter) {
      throw new Error("This turn has no available steps.");
    }
    const fileIndex = options.startMessageId ? -1 : walkStepIndexForFile(walk, initialFile);
    const at = fileIndex >= 0 ? fileIndex
      : chapter ? walkChapterStart(walk, chapter) : latestWalkTurnStart(walk);
    const step = walkStepAt(walk, at);
    const comparison = await loadStepComparison(client, id, sid, step);
    if (epochs.get(id) !== epoch) return walkState(id);
    const ready = commit(id, {
      notice: initialFile && fileIndex < 0 && !options.startMessageId
        ? "This file has no recorded changes in this chat. Showing the chat’s recorded history."
        : null,
      active: true,
      sessionId: sid,
      at,
      targetAt: at,
      selectionRevision: selectionRequests.get(id) ?? 0,
      focusPending: true,
      walk,
      comparison,
      status: "ready",
      unseenStepKeys: [],
    });
    prefetchWalkNeighbors(client, id, sid, walk, at);
    finishEnter(id, epoch);
    flushPendingRefresh(id);
    if (walk.steps.some((row) => row.kind === "command" && row.command.state === "running")) requestWalkRefresh(id);
    return ready;
  } catch (err) {
    if (epochs.get(id) !== epoch) return walkState(id);
    const current = walkState(id);
    reportSurfaceFailure(
      WALK_UNAVAILABLE,
      err instanceof Error ? err : retainPresentation ? "Could not open this walk." : "Could not prepare walk.",
      id,
    );
    const failed = commit(id, retainPresentation
      ? { ...current, status: "error" }
      : {
          ...idle(),
          active: true,
          sessionId: sid,
          status: "error",
        });
    finishEnter(id, epoch);
    flushPendingRefresh(id);
    return failed;
  }
}

/** Refreshes the timeline without changing the reader's selected snapshot. */
export async function refreshWalk(
  projectId: string,
  client?: LycaonClient | null,
): Promise<WalkState> {
  const id = projectId.trim();
  const before = walkState(id);
  if (!id || !before.active || !before.sessionId) return before;
  if (isEntering(id) || refreshing.has(id)) {
    pendingRefresh.add(id);
    return before;
  }
  const c = client ?? clients.get(id);
  if (!c) return before;
  clients.set(id, c);
  const epoch = epochs.get(id) ?? 0;
  refreshing.add(id);
  const startedAt = Date.now();
  try {
    invalidateWalkLoads(id);
    const walk = await loadWalk(c, id, before.sessionId);
    const current = walkState(id);
    if (epochs.get(id) !== epoch || !current.active) return current;
    // Navigation may finish while the refresh is in flight.
    const previous = walkStepAt(current.walk, current.at);
    const key = previous?.key;
    const at = previous ? stepIndexAfterRefresh(walk, previous) : walk.steps.length ? 0 : -1;
    if (key && at < 0) {
      reportSurfaceFailure(WALK_REFRESH_FAILED, "This step is no longer available.", id);
      return current;
    }
    const previousTarget = walkStepAt(current.walk, current.targetAt);
    const target = previousTarget ? stepIndexAfterRefresh(walk, previousTarget) : at;
    const previousKeys = new Set(current.walk.steps.map((step) => step.key));
    const arrivals = new Set(current.unseenStepKeys);
    for (const step of walk.steps) {
      if (!previousKeys.has(step.key)) arrivals.add(step.key);
    }
    const step = walkStepAt(walk, at);
    if (step) arrivals.delete(step.key);
    let comparison = current.comparison;
    if (!key && step) {
      const selectionRequest = selectionRequests.get(id) ?? 0;
      comparison = await loadStepComparison(c, id, current.sessionId, step);
      if (epochs.get(id) !== epoch || selectionRequests.get(id) !== selectionRequest || !walkState(id).active) {
        return walkState(id);
      }
    }
    return commit(id, {
      ...current,
      walk,
      at,
      targetAt: target < 0 ? at : target,
      comparison,
      status: "ready",
      unseenStepKeys: walk.steps.filter((row) => arrivals.has(row.key)).map((row) => row.key),
    });
  } catch (err) {
    if (epochs.get(id) !== epoch || !walkState(id).active) return walkState(id);
    reportSurfaceFailure(WALK_REFRESH_FAILED, err instanceof Error ? err : "Couldn’t refresh the walk.", id);
    return walkState(id);
  } finally {
    if (epochs.get(id) === epoch) {
      lastRefreshMs.set(id, Date.now() - startedAt);
      refreshing.delete(id);
      flushPendingRefresh(id);
      if (walkState(id).walk.steps.some((row) => row.kind === "command" && row.command.state === "running")) requestWalkRefresh(id);
    }
  }
}

function refreshDelay(projectId: string): number {
  return Math.max(
    REFRESH_THROTTLE_MS,
    Math.min(
      REFRESH_DUTY_FACTOR * (lastRefreshMs.get(projectId) ?? 0),
      REFRESH_DELAY_CAP_MS,
    ),
  );
}

export function requestWalkRefresh(projectId: string): void {
  invalidateWalkLoads(projectId);
  const id = projectId.trim();
  if (!id || !walkState(id).active) return;
  if (isEntering(id) || refreshing.has(id)) {
    pendingRefresh.add(id);
    return;
  }
  if (refreshTimers.has(id)) return;
  refreshTimers.set(
    id,
    setTimeout(() => {
      refreshTimers.delete(id);
      if (!walkState(id).active) return;
      void refreshWalk(id);
    }, refreshDelay(id)),
  );
}

export function leaveWalk(projectId: string): void {
  const id = projectId.trim();
  if (!id) return;
  if (!walkState(id).active) return;
  claim(id);
  enteringEpochs.delete(id);
  selectionRequests.set(id, (selectionRequests.get(id) ?? 0) + 1);
  clients.delete(id);
  refreshing.delete(id);
  pendingRefresh.delete(id);
  lastRefreshMs.delete(id);
  const timer = refreshTimers.get(id);
  if (timer) clearTimeout(timer);
  refreshTimers.delete(id);
  commit(id, idle());
}

export function setWalkAt(projectId: string, index: number): void {
  const id = projectId.trim();
  const s = walkState(id);
  if (!s.active) return;
  const next = clampWalkIndex(s.walk, index);
  const sourceClient = clients.get(id);
  const prefetch = () => { if (sourceClient) prefetchWalkNeighbors(sourceClient, id, s.sessionId, s.walk, next); };
  const step = walkStepAt(s.walk, next);
  if (!step) return;
  const unseenStepKeys = s.unseenStepKeys.filter((key) => key !== step.key);
  const selectionRequest = (selectionRequests.get(id) ?? 0) + 1;
  selectionRequests.set(id, selectionRequest);
  if (next === s.at) {
    commit(id, { ...s, targetAt: next, unseenStepKeys, selectionRevision: selectionRequest, focusPending: true });
    return;
  }
  if (step.kind !== "effect") {
    prefetch();
    // A group step settles synchronously: its page needs no comparison.
    commit(id, {
      ...s,
      at: next,
      targetAt: next,
      selectionRevision: selectionRequest,
      focusPending: true,
      comparison: null,
      status: "ready",
      unseenStepKeys,
    });
    return;
  }
  const client = clients.get(id);
  if (!client) return;
  commit(id, { ...s, targetAt: next, status: "ready" });
  void loadStepComparison(client, id, s.sessionId, step).then(
    (comparison) => {
      const held = walkState(id);
      if (selectionRequests.get(id) !== selectionRequest || !held.active) return;
      // Re-resolve by step key after concurrent rail refreshes.
      const resolved = held.walk.steps.findIndex((row) => row.key === step.key);
      if (resolved < 0) return;
      commit(id, {
        ...held,
        at: resolved,
        targetAt: resolved,
        selectionRevision: selectionRequest,
        focusPending: true,
        comparison,
        status: "ready",
        unseenStepKeys: held.unseenStepKeys.filter((key) => key !== step.key),
      });
    },
    (failure: unknown) => {
      const held = walkState(id);
      if (selectionRequests.get(id) !== selectionRequest || !held.active) return;
      reportSurfaceFailure(WALK_STEP_UNAVAILABLE, failure instanceof Error ? failure : "Could not load this walk step.", id);
      commit(id, {
        ...held,
        targetAt: held.at,
        status: "error",
      });
    },
  );
  prefetch();
}

export function stepWalk(projectId: string, delta: number): void {
  const s = walkState(projectId);
  if (!s.active || s.at < 0) return;
  setWalkAt(projectId, s.targetAt + delta);
}

export function walkToStart(projectId: string): void {
  setWalkAt(projectId, 0);
}

export function walkToLatest(projectId: string): void {
  const s = walkState(projectId);
  commit(projectId.trim(), { ...s, unseenStepKeys: [] });
  setWalkAt(projectId, s.walk.steps.length - 1);
}

export function resetWalkForTests(): void {
  resetWalkLoadsForTests();
  resetWalkPrefetchForTests();
  resetWalkGitLoadsForTests();
  states.clear();
  epochs.clear();
  selectionRequests.clear();
  clients.clear();
  refreshing.clear();
  enteringEpochs.clear();
  pendingRefresh.clear();
  lastRefreshMs.clear();
  for (const timer of refreshTimers.values()) clearTimeout(timer);
  refreshTimers.clear();
  resetSourceComparisonCacheForTests();
}
