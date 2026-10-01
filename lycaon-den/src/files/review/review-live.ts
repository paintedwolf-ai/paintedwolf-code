/**
 * Review's live rows: files an agent is changing now. A row that landed is held
 * until the comparison has read past its landing, so the file moves from the
 * live row into the landed list without leaving it.
 */

import type {
  AgentSessionPresence,
  AgentWorkerDraftState,
  SourceChange,
} from "../../api/types.ts";
import { MinVisibleHold } from "../../ui/min-visible-hold.ts";
import {
  reviewRowKey,
  type ReviewChangeStats,
  type ReviewLiveRow,
  type ReviewLiveState,
} from "./review-model.ts";
import {
  scopeClaimedEpoch,
  scopeSettledEpoch,
  subscribeScopeSettled,
} from "../tree/scope-resolution.ts";

const STATE_RANK: Record<ReviewLiveState, number> = {
  waiting: 0,
  editing: 1,
  landing: 2,
  ready: 3,
  sandbox: 4,
  reserved: 5,
};

const DRAFT_STATE: Record<AgentWorkerDraftState, ReviewLiveState> = {
  reserved: "reserved",
  drafting: "sandbox",
  ready: "ready",
  landing: "landing",
};

/** Stats are kept for this many recent landings. */
export const REVIEW_STATS_MAX = 200;
/** Subscribers re-render at most this often while events stream. */
export const REVIEW_LIVE_COALESCE_MS = 180;

/** Every file an agent is about to change, at its strongest state. */
export function projectLiveRows(
  sessions: ReadonlyMap<string, AgentSessionPresence>,
): Map<string, ReviewLiveRow> {
  const out = new Map<string, ReviewLiveRow>();
  const add = (
    presence: AgentSessionPresence,
    rootId: string,
    path: string,
    state: ReviewLiveState,
    ids: { toolCallId?: string; jobId?: string },
  ) => {
    if (!rootId.trim() || !path.trim()) return;
    const key = reviewRowKey(rootId, path);
    const held = out.get(key);
    const toolCallIds = [...(held?.toolCallIds ?? []), ...(ids.toolCallId ? [ids.toolCallId] : [])];
    const jobIds = [...(held?.jobIds ?? []), ...(ids.jobId ? [ids.jobId] : [])];
    const stronger = !held || STATE_RANK[state] < STATE_RANK[held.state];
    out.set(key, {
      rootId,
      path,
      state: stronger ? state : held.state,
      chatId: stronger ? presence.session_id : held.chatId,
      chatTitle: stronger ? presence.title?.trim() ?? "" : held.chatTitle,
      turn: stronger ? presence.turn : held.turn,
      toolCallIds: [...new Set(toolCallIds)],
      jobIds: [...new Set(jobIds)],
    });
  };
  for (const presence of sessions.values()) {
    for (const intent of presence.intents) {
      add(presence, intent.root_id, intent.path, intent.state === "awaiting_approval" ? "waiting" : "editing",
        { toolCallId: intent.tool_call_id, jobId: intent.worker_id });
    }
    for (const activity of presence.activities) {
      if (activity.kind !== "editing") continue;
      add(presence, activity.root_id, activity.path, "editing",
        { toolCallId: activity.tool_call_id, jobId: activity.worker_id });
    }
    for (const draft of presence.worker_drafts) {
      add(presence, draft.root_id, draft.path, DRAFT_STATE[draft.state], { jobId: draft.worker_id });
    }
  }
  return out;
}

/** A landing is the work of a row when the call, the worker job, or the chat that made it matches. */
function landingOf(row: ReviewLiveRow, change: SourceChange): boolean {
  const toolCallId = change.tool_call_id?.trim();
  const jobId = change.worker_id?.trim();
  if (toolCallId && row.toolCallIds.includes(toolCallId)) return true;
  if (jobId && row.jobIds.includes(jobId)) return true;
  return change.origin === "agent" && change.session_id?.trim() === row.chatId;
}

type Landing = {
  /** The newest resolve claimed when the landing arrived; a later answer has read it. */
  epoch: number;
  row: ReviewLiveRow;
};

type ProjectLive = {
  live: Map<string, ReviewLiveRow>;
  /** Presence rows plus landings the comparison has not read yet. */
  active: Map<string, ReviewLiveRow>;
  /** The newest value of every row still shown, for a landing that arrives after its presence ended. */
  lastSeen: Map<string, ReviewLiveRow>;
  landed: Map<string, Landing>;
  /** `null` while a landing's stats are not yet known. */
  stats: Map<string, ReviewChangeStats | null>;
  /** Keys the hold shows, in the order they appeared. */
  shown: readonly string[];
  rows: readonly ReviewLiveRow[];
  hold: MinVisibleHold<true>;
  /** Set while publish drives the hold, which renders once afterward. */
  publishing: boolean;
};

export type ReviewLiveSnapshot = {
  rows: readonly ReviewLiveRow[];
  stats: ReadonlyMap<string, ReviewChangeStats>;
};

const byProject = new Map<string, ProjectLive>();
const listeners = new Set<(projectId: string) => void>();

function notify(projectId: string): void {
  for (const fn of listeners) fn(projectId);
}

/** Rows read their newest value; a row past its presence reads the last one it had. */
function render(projectId: string, state: ProjectLive): void {
  state.rows = state.shown.flatMap((key) => {
    const row = state.active.get(key) ?? state.lastSeen.get(key);
    return row ? [row] : [];
  });
  notify(projectId);
}

function ensure(projectId: string): ProjectLive {
  let state = byProject.get(projectId);
  if (!state) {
    const created: ProjectLive = {
      live: new Map(),
      active: new Map(),
      lastSeen: new Map(),
      landed: new Map(),
      stats: new Map(),
      shown: [],
      rows: [],
      publishing: false,
      // The hold decides which files show; it publishes when that set changes.
      hold: new MinVisibleHold<true>((visible) => {
        created.shown = [...visible.keys()];
        for (const key of created.lastSeen.keys()) {
          if (!visible.has(key)) created.lastSeen.delete(key);
        }
        if (!created.publishing) render(projectId, created);
      }),
    };
    state = created;
    byProject.set(projectId, state);
  }
  return state;
}

function publish(projectId: string, state: ProjectLive): void {
  const active = new Map(state.live);
  for (const [key, landing] of state.landed) {
    if (!active.has(key)) active.set(key, { ...landing.row, state: "landing" });
  }
  state.active = active;
  for (const [key, row] of active) state.lastSeen.set(key, row);
  state.publishing = true;
  try {
    state.hold.update(new Map([...active.keys()].map((key) => [key, true as const])));
  } finally {
    state.publishing = false;
  }
  render(projectId, state);
}

/** Replaces the live rows with what the host's presence says now. */
export function syncReviewPresence(
  projectId: string,
  sessions: ReadonlyMap<string, AgentSessionPresence>,
): void {
  const id = projectId.trim();
  if (!id) return;
  const state = ensure(id);
  state.live = projectLiveRows(sessions);
  publish(id, state);
}

/**
 * Notes a change that landed in the project's own files. A live row it
 * finishes is held until the comparison answers from after it.
 */
export function noteReviewLanding(projectId: string, change: SourceChange): void {
  const id = projectId.trim();
  const path = change.path?.trim();
  if (!id || !path) return;
  const state = ensure(id);
  const key = reviewRowKey(change.root_id?.trim() ?? "", path);
  state.stats.delete(key);
  state.stats.set(key, null);
  while (state.stats.size > REVIEW_STATS_MAX) {
    const oldest = state.stats.keys().next().value;
    if (oldest === undefined) break;
    state.stats.delete(oldest);
  }
  const row = state.live.get(key) ?? state.lastSeen.get(key);
  if (row && landingOf(row, change)) {
    state.landed.set(key, { epoch: scopeClaimedEpoch(id), row });
    publish(id, state);
    return;
  }
  notify(id);
}

/** Attaches line counts to the newest landing of a file, once. */
export function noteReviewChangeStats(
  projectId: string,
  rootId: string,
  path: string,
  stats: ReviewChangeStats,
): void {
  const state = byProject.get(projectId.trim());
  const key = reviewRowKey(rootId, path);
  if (!state || state.stats.get(key) !== null) return;
  state.stats.set(key, stats);
  notify(projectId.trim());
}

function releaseCaughtUp(projectId: string): void {
  const state = byProject.get(projectId);
  if (!state || state.landed.size === 0) return;
  const settled = scopeSettledEpoch(projectId);
  let released = false;
  for (const [key, landing] of state.landed) {
    if (settled <= landing.epoch) continue;
    state.landed.delete(key);
    released = true;
  }
  if (released) publish(projectId, state);
}

subscribeScopeSettled(releaseCaughtUp);

export function reviewLiveSnapshot(projectId: string): ReviewLiveSnapshot {
  const state = byProject.get(projectId.trim());
  if (!state) return { rows: [], stats: new Map() };
  const stats = new Map<string, ReviewChangeStats>();
  for (const [key, value] of state.stats) if (value) stats.set(key, value);
  return { rows: state.rows, stats };
}

/** Subscribes with an immediate leading and a throttled trailing notification. */
export function subscribeReviewLive(projectId: string, fn: () => void): () => void {
  const id = projectId.trim();
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastRun = 0;
  const listener = (changed: string) => {
    if (changed !== id) return;
    const since = Date.now() - lastRun;
    if (since >= REVIEW_LIVE_COALESCE_MS) {
      lastRun = Date.now();
      fn();
      return;
    }
    if (timer != null) return;
    timer = setTimeout(() => {
      timer = null;
      lastRun = Date.now();
      fn();
    }, REVIEW_LIVE_COALESCE_MS - since);
  };
  listeners.add(listener);
  return () => {
    if (timer != null) clearTimeout(timer);
    listeners.delete(listener);
  };
}

export function resetReviewLiveForTests(): void {
  for (const state of byProject.values()) state.hold.dispose();
  byProject.clear();
}
