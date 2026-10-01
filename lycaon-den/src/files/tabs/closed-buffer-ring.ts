/** Keep the 20 most recently closed buffers per project. */
import type {
  DenClosedBufferEntry,
  DenClosedBufferRing,
  DenEditorViewStateEntry,
} from "../../../shared/app-state-types.ts";
import { parseViewStateEntry } from "../../components/source/editor/editor-view-state.ts";

export const CLOSED_BUFFER_RING_CAP = 20;

const rings = new Map<string, DenClosedBufferEntry[]>();

export function pushClosedBuffer(
  projectId: string,
  entry: DenClosedBufferEntry,
): void {
  const id = projectId.trim();
  if (!id) return;
  const list = rings.get(id) ?? [];
  const filtered = list.filter((e) => e.key !== entry.key);
  filtered.unshift(entry);
  rings.set(id, filtered.slice(0, CLOSED_BUFFER_RING_CAP));
}

/** Pop the most recent entry, or null when empty. */
export function popClosedBuffer(projectId: string): DenClosedBufferEntry | null {
  const id = projectId.trim();
  const list = rings.get(id);
  if (!list?.length) return null;
  const [head, ...rest] = list;
  rings.set(id, rest);
  return head ?? null;
}

export function toClosedBufferRingStore(): DenClosedBufferRing {
  const byProject: Record<string, DenClosedBufferEntry[]> = {};
  for (const [pid, list] of rings) {
    if (list.length === 0) continue;
    byProject[pid] = list.map(cloneEntry);
  }
  return { byProject };
}

export function loadClosedBufferRingStore(
  store: DenClosedBufferRing | undefined,
): void {
  rings.clear();
  if (!store?.byProject) return;
  for (const [pid, list] of Object.entries(store.byProject)) {
    if (typeof pid !== "string" || !pid.trim()) continue;
    if (!Array.isArray(list)) continue;
    const entries: DenClosedBufferEntry[] = [];
    for (const raw of list) {
      const parsed = parseClosedBufferEntry(raw);
      if (parsed) entries.push(parsed);
      if (entries.length >= CLOSED_BUFFER_RING_CAP) break;
    }
    if (entries.length) rings.set(pid, entries);
  }
}

export function parseClosedBufferRing(
  value: unknown,
): DenClosedBufferRing | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const row = value as { byProject?: unknown };
  if (typeof row.byProject !== "object" || row.byProject === null) {
    return undefined;
  }
  const byProject: Record<string, DenClosedBufferEntry[]> = {};
  for (const [pid, list] of Object.entries(
    row.byProject as Record<string, unknown>,
  )) {
    if (!Array.isArray(list)) continue;
    const entries: DenClosedBufferEntry[] = [];
    for (const raw of list) {
      const parsed = parseClosedBufferEntry(raw);
      if (parsed) entries.push(parsed);
      if (entries.length >= CLOSED_BUFFER_RING_CAP) break;
    }
    if (entries.length) byProject[pid] = entries;
  }
  return Object.keys(byProject).length > 0 ? { byProject } : undefined;
}

function parseClosedBufferEntry(value: unknown): DenClosedBufferEntry | null {
  if (typeof value !== "object" || value === null) return null;
  const row = value as Record<string, unknown>;
  if (typeof row.key !== "string" || !row.key.trim()) return null;
  if (typeof row.rootId !== "string" || !row.rootId.trim()) return null;
  if (typeof row.path !== "string" || !row.path.trim()) return null;
  const entry: DenClosedBufferEntry = {
    key: row.key,
    rootId: row.rootId,
    path: row.path,
  };
  if (typeof row.rootLabel === "string") entry.rootLabel = row.rootLabel;
  if (row.pinned === true) entry.pinned = true;
  if (row.decodeAs === "utf-16le" || row.decodeAs === "utf-16be") entry.decodeAs = row.decodeAs;
  if (row.viewState != null) {
    const vs = parseViewStateEntry(row.viewState);
    if (vs) entry.viewState = vs;
  }
  return entry;
}

function cloneEntry(e: DenClosedBufferEntry): DenClosedBufferEntry {
  return {
    key: e.key,
    rootId: e.rootId,
    path: e.path,
    ...(e.rootLabel != null ? { rootLabel: e.rootLabel } : {}),
    ...(e.viewState != null ? { viewState: { ...e.viewState, folds: [...e.viewState.folds] } } : {}),
    ...(e.pinned ? { pinned: true } : {}),
    ...(e.decodeAs ? { decodeAs: e.decodeAs } : {}),
  };
}

/** Build a ring entry from a live buffer + optional captured view state. */
export function closedEntryFromBuffer(args: {
  key: string;
  rootId: string;
  path: string;
  rootLabel?: string;
  pinned?: boolean;
  decodeAs?: "utf-16le" | "utf-16be";
  jobId?: string;
  viewState?: DenEditorViewStateEntry | null;
}): DenClosedBufferEntry | null {
  if (args.jobId?.trim()) return null;
  return {
    key: args.key,
    rootId: args.rootId,
    path: args.path,
    ...(args.rootLabel != null ? { rootLabel: args.rootLabel } : {}),
    ...(args.viewState != null ? { viewState: args.viewState } : {}),
    ...(args.pinned ? { pinned: true } : {}),
    ...(args.decodeAs ? { decodeAs: args.decodeAs } : {}),
  };
}

export function resetClosedBufferRingForTests(): void {
  rings.clear();
}
