import { sourceCacheBytes } from "../source/source-cache-size.ts";
import type { LycaonClient } from "../../api/client.ts";
import type {
  SourceCommandWindow,
  SourceGitChange,
  SourceWalkFile,
  SourceWalkTurn,
} from "../../api/types.ts";
import {
  mergeSourceCommandWindows,
  mergeSourceGitChanges,
  mergeSourceWalkFiles,
} from "../source/source-walk-pages.ts";
import { buildWalk, type Walk } from "./walk-model.ts";

const PAGE_LIMIT = 500;

async function fetchWalk(
  client: LycaonClient,
  projectId: string,
  sessionId: string,
): Promise<Walk> {
  const baseline = `session:${sessionId}`;
  const files: SourceWalkFile[] = [];
  const gitChanges: SourceGitChange[] = [];
  const commands: SourceCommandWindow[] = [];
  const turns = new Map<string, SourceWalkTurn>();
  let cursor: string | undefined;
  for (;;) {
    const page = await client.listProjectSourceWalk(projectId, {
      baseline,
      limit: PAGE_LIMIT,
      cursor,
      sessionId,
      includeOutsideChanges: true,
    });
    files.push(...page.files);
    gitChanges.push(...page.git_changes);
    commands.push(...page.commands);
    for (const turn of page.turns) {
      if (!turns.has(turn.message_id)) turns.set(turn.message_id, turn);
    }
    const next = page.next_cursor;
    if (!next) break;
    if (cursor !== undefined && next === cursor) {
      throw new Error("The walk could not load its complete history. Retry the walk.");
    }
    cursor = next;
  }
  return buildWalk(
    baseline,
    mergeSourceWalkFiles(files),
    mergeSourceGitChanges(gitChanges),
    mergeSourceCommandWindows(commands),
    [...turns.values()],
  );
}

const MAX_WALKS = 16;
const MAX_WALK_BYTES = 16 * 1024 * 1024;
type WalkEntry = { client: LycaonClient; projectId: string; sessionId: string; generation: number; value?: Walk; bytes: number; pending?: Promise<Walk> };
const walks: WalkEntry[] = [];
let liveProject: string | null = null;

/** Settled reuse requires a continuously observed source event stream. */
export function setWalkLoadSubscription(projectId: string | null): void {
  invalidateWalkLoads();
  liveProject = projectId?.trim() || null;
}

/** Source changes invalidate cached and in-flight snapshots. */
export function invalidateWalkLoads(projectId?: string): void {
  for (const entry of walks) if (projectId === undefined || entry.projectId === projectId.trim()) {
    entry.generation++;
    entry.value = undefined;
    entry.bytes = 0;
    entry.pending = undefined;
  }
}

function touchWalk(entry: WalkEntry): void {
  const index = walks.indexOf(entry);
  if (index >= 0) walks.splice(index, 1);
  walks.push(entry);
  let bytes = walks.reduce((total, value) => total + value.bytes, 0);
  while (walks.length > MAX_WALKS || bytes > MAX_WALK_BYTES) {
    const oldest = walks.shift();
    if (oldest) bytes -= oldest.bytes;
  }
}

export function loadWalk(client: LycaonClient, projectId: string, sessionId: string): Promise<Walk> {
  projectId = projectId.trim(); sessionId = sessionId.trim();
  let entry = walks.find(value => value.client === client && value.projectId === projectId && value.sessionId === sessionId);
  if (!entry) entry = { client, projectId, sessionId, generation: 0, bytes: 0 };
  touchWalk(entry);
  if (entry.value) return Promise.resolve(entry.value);
  if (entry.pending) return entry.pending;
  const held = entry;
  const generation = held.generation;
  const pending = fetchWalk(client, projectId, sessionId).then(value => {
    if (liveProject === projectId && held.generation === generation && walks.includes(held)) {
      // Running command windows change without requiring a file effect.
      if (!value.steps.some(step => step.kind === "command" && step.command.state === "running")) {
        held.bytes = sourceCacheBytes(value, MAX_WALK_BYTES);
        if (held.bytes > MAX_WALK_BYTES) walks.splice(walks.indexOf(held), 1);
        else {
          held.value = value;
          touchWalk(held);
        }
      }
    }
    return value;
  }).finally(() => { if (held.pending === pending) held.pending = undefined; });
  held.pending = pending;
  return pending;
}

export function resetWalkLoadsForTests(): void { walks.length = 0; liveProject = null; }
