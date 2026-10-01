/** The host's live view of what each chat is doing in a project's files. */

import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  AgentPresenceEvent,
  AgentPresenceSnapshot,
  AgentSessionPresence,
} from "../../api/types.ts";

type ProjectAgentPresence = {
  readonly projectId: string;
  /** Chats with presence, by root session id. */
  readonly sessions: ReadonlyMap<string, AgentSessionPresence>;
};

type ProjectState = {
  sessions: Map<string, AgentSessionPresence>;
  /** Newest event revision applied per chat since the stream opened. */
  revisions: Map<string, number>;
  /** Events at or below the last snapshot's revision are already reflected. */
  floor: number;
  generation: number;
};

const EMPTY_SESSIONS: ReadonlyMap<string, AgentSessionPresence> = new Map();
const states = new Map<string, ProjectState>();
const [published, setPublished] = createSignal<ReadonlyMap<string, ProjectAgentPresence>>(new Map());

function state(projectId: string): ProjectState {
  let found = states.get(projectId);
  if (!found) {
    found = { sessions: new Map(), revisions: new Map(), floor: 0, generation: 0 };
    states.set(projectId, found);
  }
  return found;
}

function publish(projectId: string): void {
  const next = new Map(published());
  const current = states.get(projectId);
  if (current && current.sessions.size > 0) {
    next.set(projectId, { projectId, sessions: new Map(current.sessions) });
  } else {
    next.delete(projectId);
  }
  setPublished(next);
}

function hasAgentPresence(presence: AgentSessionPresence): boolean {
  return presence.activities.length > 0 || presence.reads.length > 0 || presence.intents.length > 0
    || presence.worker_drafts.length > 0;
}

/** Reactive presence for one project; empty until the host reports any. */
export function agentPresenceFor(projectId: string): ReadonlyMap<string, AgentSessionPresence> {
  return published().get(projectId.trim())?.sessions ?? EMPTY_SESSIONS;
}

/** Applies one chat's complete presence from the event stream. */
export function applyAgentPresenceEvent(event: AgentPresenceEvent): void {
  const projectId = event.project_id.trim();
  const sessionId = event.session_id.trim();
  if (!projectId || !sessionId) return;
  const s = state(projectId);
  if (event.revision <= (s.revisions.get(sessionId) ?? s.floor)) return;
  s.revisions.set(sessionId, event.revision);
  const had = s.sessions.has(sessionId);
  if (hasAgentPresence(event.presence)) {
    s.sessions.set(sessionId, event.presence);
  } else if (had) {
    s.sessions.delete(sessionId);
  } else {
    return;
  }
  publish(projectId);
}

/** Merges a snapshot with events that arrived while it was loading. */
export function applyAgentPresenceSnapshot(snapshot: AgentPresenceSnapshot): void {
  const projectId = snapshot.project_id.trim();
  if (!projectId) return;
  const s = state(projectId);
  const sessions = new Map<string, AgentSessionPresence>();
  for (const presence of snapshot.sessions) {
    if (hasAgentPresence(presence)) sessions.set(presence.session_id, presence);
  }
  for (const [sessionId, revision] of s.revisions) {
    if (revision <= snapshot.revision) continue;
    const newer = s.sessions.get(sessionId);
    if (newer) sessions.set(sessionId, newer);
    else sessions.delete(sessionId);
  }
  s.sessions = sessions;
  s.floor = snapshot.revision;
  for (const [sessionId, revision] of s.revisions) {
    if (revision <= snapshot.revision) s.revisions.delete(sessionId);
  }
  publish(projectId);
}

/** Reconnecting clears stream watermarks because a restarted host resets revisions. */
export async function resyncAgentPresence(client: LycaonClient, projectId: string): Promise<void> {
  const id = projectId.trim();
  if (!id) return;
  const s = state(id);
  const generation = ++s.generation;
  s.revisions.clear();
  s.floor = 0;
  const snapshot = await client.getAgentPresence(id);
  if (states.get(id) !== s || s.generation !== generation) return;
  applyAgentPresenceSnapshot(snapshot);
}

/** Drops presence for every project other than the one this window follows. */
export function retainAgentPresenceProject(projectId: string | null): void {
  const keep = projectId?.trim() ?? "";
  let changed = false;
  for (const id of [...states.keys()]) {
    if (id === keep) continue;
    states.delete(id);
    changed = true;
  }
  if (changed) setPublished(new Map([...published()].filter(([id]) => id === keep)));
}

export function resetAgentPresenceForTests(): void {
  states.clear();
  setPublished(new Map());
}
