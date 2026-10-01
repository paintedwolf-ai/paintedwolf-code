import type {
  SourceChange,
  SourceChangesEvent,
  SourceDirEntry,
  SourceDirListing,
} from "../../api/types.ts";
import { runCooldownMs } from "../../store/coalesced-async.ts";
import { SourceWorkspaceMismatchError } from "../source/source-workspace-identity.ts";

export type SourceTreeBrowse = (
  rootId: string,
  dir: string,
) => Promise<SourceDirListing>;

export type SourceTreeListingSnapshot = {
  listing: SourceDirListing;
  stale: boolean;
};

export type SourceTreeConnection = {
  workspaceId: string;
  load: (
    rootId: string,
    dir: string,
    force?: boolean,
  ) => Promise<SourceDirListing>;
  get: (rootId: string, dir: string) => SourceTreeListingSnapshot | undefined;
  revision: (rootId: string) => number;
  confirm: (change: SourceChange) => void;
  /** Reports a listing the surface found to be wrong, for paced revalidation. */
  invalidate: (rootId: string, dir: string) => void;
  /** Keeps the expanded tree's structure resident without scheduling offscreen reads. */
  retain: (keys: Iterable<string>) => void;
  observe: (keys: Iterable<string>, active: boolean) => void;
  subscribe: (listener: () => void) => () => void;
  disconnect: () => void;
};

type ListingRecord = {
  listing: SourceDirListing;
  /** When the projection last diverged from disk; null while it is trusted. */
  staleSince: number | null;
  loadedAt: number;
  lastUsed: number;
};

type FlightStatus = {
  /** A change landed while this listing was on the wire. */
  raced: boolean;
  /** Removed directories discard their pending responses. */
  abandoned: boolean;
};

type ListingFlight = {
  token: symbol;
  status: FlightStatus;
  promise: Promise<SourceDirListing>;
};

type ConnectionRecord = {
  browse: SourceTreeBrowse;
  observed: Set<string>;
  retained: Set<string>;
  listeners: Set<() => void>;
  active: boolean;
};

type WorkspaceState = {
  projectId: string;
  workspaceId: string;
  revision: number;
  resyncRevision: number;
  membershipRevisions: Map<string, number>;
  listings: Map<string, ListingRecord>;
  childCount: number;
  flights: Map<string, ListingFlight>;
  listeners: Set<() => void>;
  connections: Map<symbol, ConnectionRecord>;
  revalidateTimer?: ReturnType<typeof setTimeout>;
  revalidating: boolean;
  /** Earliest the next revalidation round may start, from its predecessor. */
  cooldownUntil: number;
  /** Earliest the next round may start after a failed one. */
  retryAt?: number;
  /** Last change this projection could not absorb. */
  lastDivergenceAt: number;
  lastUsed: number;
};

const WORKSPACE_CAP = 3;
const LISTING_CAP = 10_000;
const CHILD_CAP = 100_000;
/** Quiet window a diverged listing waits for before it is re-read. */
const REVALIDATE_IDLE_MS = 400;
/** Longest a diverged listing waits when churn never goes quiet. */
const REVALIDATE_MAX_WAIT_MS = 2_000;
/** Cadence for a directory the host reported it cannot watch. */
const INCOMPLETE_REVALIDATE_MS = 2_000;
const REVALIDATE_COOLDOWN_CAP_MS = 4 * REVALIDATE_MAX_WAIT_MS;
const workspaces = new Map<string, WorkspaceState>();

function sourceTreeListingKey(rootId: string, dir: string): string {
  return `${rootId}\0${normalizeDir(dir)}`;
}

function normalizeDir(dir: string): string {
  const normalized = normalizePath(dir);
  return normalized ?? ".";
}

function normalizePath(raw: string | undefined): string | null {
  const parts = (raw ?? "")
    .replaceAll("\\", "/")
    .split("/")
    .filter((part) => part !== "" && part !== ".");
  if (parts.length === 0 || parts.some((part) => part === "..")) return null;
  return parts.join("/");
}

function parentDir(path: string): string {
  const index = path.lastIndexOf("/");
  return index < 0 ? "." : path.slice(0, index) || ".";
}

function fileName(path: string): string {
  const index = path.lastIndexOf("/");
  return index < 0 ? path : path.slice(index + 1);
}

function createWorkspace(projectId: string, workspaceId: string): WorkspaceState {
  const state: WorkspaceState = {
    projectId,
    workspaceId,
    revision: 0,
    resyncRevision: 0,
    membershipRevisions: new Map(),
    listings: new Map(),
    childCount: 0,
    flights: new Map(),
    listeners: new Set(),
    connections: new Map(),
    revalidating: false,
    cooldownUntil: 0,
    lastDivergenceAt: 0,
    lastUsed: Date.now(),
  };
  workspaces.set(workspaceId, state);
  evictWorkspaces();
  return state;
}

function workspace(projectId: string, workspaceId: string): WorkspaceState {
  const current = workspaces.get(workspaceId);
  if (current) {
    current.projectId = projectId;
    current.lastUsed = Date.now();
    return current;
  }
  return createWorkspace(projectId, workspaceId);
}

function notify(state: WorkspaceState): void {
  state.lastUsed = Date.now();
  for (const listener of [...state.listeners]) listener();
}

/** Retains raced responses as stale so continuous writes do not starve rendering. */
function noteDivergence(state: WorkspaceState, key: string): void {
  state.lastDivergenceAt = Date.now();
  const flight = state.flights.get(key);
  if (flight) flight.status.raced = true;
}

function isStale(record: ListingRecord): boolean {
  return record.staleSince != null;
}

/** Marks a listing for revalidation. Returns whether this was new divergence. */
function markStale(state: WorkspaceState, key: string): boolean {
  const record = state.listings.get(key);
  noteDivergence(state, key);
  if (!record || isStale(record)) return false;
  state.listings.set(key, { ...record, staleSince: Date.now() });
  return true;
}

function sameEntries(
  current: readonly SourceDirEntry[],
  next: readonly SourceDirEntry[],
): boolean {
  return (
    current.length === next.length &&
    current.every(
      (entry, index) =>
        entry.name === next[index]?.name &&
        entry.is_dir === next[index]?.is_dir,
    )
  );
}

function compareEntries(left: SourceDirEntry, right: SourceDirEntry): number {
  if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1;
  return left.name.localeCompare(right.name, undefined, {
    sensitivity: "base",
  });
}

function sortedEntries(entries: readonly SourceDirEntry[]): SourceDirEntry[] {
  return [...entries].sort(compareEntries);
}

function insertEntry(
  entries: readonly SourceDirEntry[],
  entry: SourceDirEntry,
): SourceDirEntry[] {
  let low = 0;
  let high = entries.length;
  while (low < high) {
    const middle = (low + high) >>> 1;
    if (compareEntries(entries[middle]!, entry) <= 0) low = middle + 1;
    else high = middle;
  }
  return [...entries.slice(0, low), entry, ...entries.slice(low)];
}

function validateListing(
  state: WorkspaceState,
  rootId: string,
  dir: string,
  listing: SourceDirListing,
): SourceDirListing {
  const normalizedDir = normalizeDir(dir);
  if (listing.workspace_id !== state.workspaceId) {
    throw new SourceWorkspaceMismatchError(
      state.workspaceId,
      listing.workspace_id,
    );
  }
  if (
    listing.root_id !== rootId ||
    normalizeDir(listing.dir) !== normalizedDir
  ) {
    throw new Error("Source listing returned for another directory.");
  }
  return {
    ...listing,
    dir: normalizedDir,
    entries: sortedEntries(listing.entries),
  };
}

function storeListing(
  state: WorkspaceState,
  listing: SourceDirListing,
  options: { now?: number; publish?: boolean; stale?: boolean } = {},
): boolean {
  const { now = Date.now(), publish = true, stale = false } = options;
  const key = sourceTreeListingKey(listing.root_id, listing.dir);
  const previous = state.listings.get(key);
  const sameMembership = previous != null && sameEntries(previous.listing.entries, listing.entries);
  state.childCount -= previous?.listing.entries.length ?? 0;
  state.listings.set(key, {
    listing: sameMembership ? { ...listing, entries: previous.listing.entries } : listing,
    // A stale response starts a new revalidation window.
    staleSince: stale ? now : null,
    loadedAt: now,
    lastUsed: now,
  });
  state.childCount += listing.entries.length;
  const changed =
    !previous ||
    isStale(previous) !== stale ||
    previous.listing.watch_complete !== listing.watch_complete ||
    !sameMembership;
  if (changed && publish) notify(state);
  if (publish) evictListings(state);
  if (publish) scheduleRevalidation(state);
  return changed;
}

/** Deduplicates directory reads and schedules stale responses for paced revalidation. */
async function loadListing(
  state: WorkspaceState,
  connection: ConnectionRecord,
  rootId: string,
  dir: string,
  force: boolean,
): Promise<SourceDirListing> {
  dir = normalizeDir(dir);
  const key = sourceTreeListingKey(rootId, dir);
  const cached = state.listings.get(key);
  if (!force && cached && !isStale(cached)) {
    cached.lastUsed = Date.now();
    return cached.listing;
  }
  const currentFlight = state.flights.get(key);
  if (currentFlight) return currentFlight.promise;

  const flightToken = Symbol(key);
  const status: FlightStatus = { raced: false, abandoned: false };
  // Register after browse so its response covers synchronous source events.
  const request = connection.browse(rootId, dir);
  const promise = (async () => {
    const listing = validateListing(state, rootId, dir, await request);
    if (!status.abandoned) storeListing(state, listing, { stale: status.raced });
    return listing;
  })().finally(() => {
    if (state.flights.get(key)?.token === flightToken) state.flights.delete(key);
  });
  state.flights.set(key, { token: flightToken, status, promise });
  return promise;
}

type DirectPatch = {
  change: SourceChange;
  path: string;
  effect: "upsert" | "remove";
};

/** Null entries preserve membership; unresolved entry kinds require a new read. */
type PatchOutcome = {
  entries: SourceDirEntry[] | null;
  unresolved: boolean;
};

function applySingleDirectPatch(
  record: ListingRecord,
  patch: DirectPatch,
): PatchOutcome | null {
  const entries = record.listing.entries;
  const name = fileName(patch.path);
  const index = entries.findIndex((entry) => entry.name === name);
  const current = entries[index];
  if (patch.effect === "remove") {
    if (!current) return null;
    return {
      entries: entries.filter((_, entryIndex) => entryIndex !== index),
      unresolved: false,
    };
  }
  if (patch.change.is_dir == null) {
    return current ? null : { entries: null, unresolved: true };
  }
  if (current?.is_dir === patch.change.is_dir) return null;
  const next = current
    ? { ...current, is_dir: patch.change.is_dir }
    : { name, is_dir: patch.change.is_dir };
  const remaining = current
    ? entries.filter((_, entryIndex) => entryIndex !== index)
    : entries;
  return { entries: insertEntry(remaining, next), unresolved: false };
}

function applyBatchedDirectPatches(
  record: ListingRecord,
  patches: readonly DirectPatch[],
): PatchOutcome | null {
  const byName = new Map(
    record.listing.entries.map((entry) => [entry.name, entry]),
  );
  let entriesChanged = false;
  let unresolved = false;
  for (const patch of patches) {
    const name = fileName(patch.path);
    const current = byName.get(name);
    if (patch.effect === "remove") {
      if (current) {
        byName.delete(name);
        entriesChanged = true;
      }
    } else if (patch.change.is_dir == null) {
      if (!current) unresolved = true;
    } else if (!current || current.is_dir !== patch.change.is_dir) {
      byName.set(
        name,
        current
          ? { ...current, is_dir: patch.change.is_dir }
          : { name, is_dir: patch.change.is_dir },
      );
      entriesChanged = true;
    }
  }
  if (!entriesChanged && !unresolved) return null;
  return {
    entries: entriesChanged ? sortedEntries([...byName.values()]) : null,
    unresolved,
  };
}

function applyDirectPatches(
  state: WorkspaceState,
  patches: DirectPatch[],
): boolean {
  const grouped = new Map<string, DirectPatch[]>();
  for (const patch of patches) {
    const key = sourceTreeListingKey(
      patch.change.root_id,
      parentDir(patch.path),
    );
    const current = grouped.get(key);
    if (current) current.push(patch);
    else grouped.set(key, [patch]);
  }
  let changed = false;
  for (const [key, direct] of grouped) {
    const record = state.listings.get(key);
    if (!record) {
      // An initial listing may omit an in-flight change.
      if (state.flights.has(key)) noteDivergence(state, key);
      continue;
    }
    const outcome = direct.length === 1
      ? applySingleDirectPatch(record, direct[0]!)
      : applyBatchedDirectPatches(record, direct);
    // Matching events leave the current request valid.
    if (!outcome) continue;
    noteDivergence(state, key);
    const { entries } = outcome;
    const staleSince = outcome.unresolved
      ? (record.staleSince ?? Date.now())
      : record.staleSince;
    // Re-marking an already-stale listing publishes nothing new.
    if (!entries && staleSince === record.staleSince) continue;
    state.listings.set(key, {
      ...record,
      listing: entries ? { ...record.listing, entries } : record.listing,
      staleSince,
    });
    if (entries) {
      state.childCount += entries.length - record.listing.entries.length;
    }
    changed = true;
  }
  return changed;
}

function markNearestLoadedAncestor(
  state: WorkspaceState,
  change: SourceChange,
  path: string,
): boolean {
  const parts = path.split("/");
  for (let depth = parts.length - 1; depth >= 0; depth -= 1) {
    const dir = depth === 0 ? "." : parts.slice(0, depth).join("/");
    const key = sourceTreeListingKey(change.root_id, dir);
    const record = state.listings.get(key);
    if (!record) continue;
    const child = record.listing.entries.find((entry) => entry.name === parts[depth]);
    if (depth === parts.length - 1) return false;
    if (!child || !child.is_dir) return markStale(state, key);
    return false;
  }
  return false;
}

function evictSubtree(state: WorkspaceState, rootId: string, path: string): boolean {
  const prefix = `${rootId}\0${path}/`;
  const exact = `${rootId}\0${path}`;
  let changed = false;
  const keys = new Set([...state.listings.keys(), ...state.flights.keys()]);
  for (const key of keys) {
    if (key !== exact && !key.startsWith(prefix)) continue;
    const record = state.listings.get(key);
    if (record) {
      state.childCount -= record.listing.entries.length;
      state.listings.delete(key);
      changed = true;
    }
    // Discard the response for the deleted directory.
    const flight = state.flights.get(key);
    if (flight) {
      flight.status.abandoned = true;
      state.lastDivergenceAt = Date.now();
    }
  }
  return changed;
}

function invalidateListings(state: WorkspaceState): boolean {
  let changed = false;
  for (const key of [...state.listings.keys(), ...state.flights.keys()]) {
    changed = markStale(state, key) || changed;
  }
  return changed;
}

function applyChanges(state: WorkspaceState, changes: SourceChange[]): boolean {
  let changed = false;
  const patches: DirectPatch[] = [];
  const ancestorChecks: Array<{ change: SourceChange; path: string }> = [];
  for (const change of changes) {
    const path = normalizePath(change.path);
    if (!path || !change.root_id.trim()) continue;
    if (change.op === "rename") {
      const fromPath = normalizePath(change.from_path);
      if (fromPath) {
        patches.push({ change, path: fromPath, effect: "remove" });
        changed = evictSubtree(state, change.root_id, fromPath) || changed;
      }
      patches.push({ change, path, effect: "upsert" });
      ancestorChecks.push({ change, path });
      continue;
    }
    if (change.op === "delete") {
      patches.push({ change, path, effect: "remove" });
      changed = evictSubtree(state, change.root_id, path) || changed;
      continue;
    }
    patches.push({ change, path, effect: "upsert" });
    ancestorChecks.push({ change, path });
  }
  changed = applyDirectPatches(state, patches) || changed;
  for (const check of ancestorChecks) {
    changed = markNearestLoadedAncestor(state, check.change, check.path) || changed;
  }
  return changed;
}

/** Existing file content does not change folder membership. Unknown paths still invalidate. */
function recordMembershipChanges(state: WorkspaceState, changes: readonly SourceChange[]): void {
  for (const change of changes) {
    const path = normalizePath(change.path);
    if (!path) continue;
    if (change.op === "write") {
      const record = state.listings.get(sourceTreeListingKey(change.root_id, parentDir(path)));
      const entry = record?.listing.entries.find((entry) => entry.name === fileName(path));
      if (entry && (change.is_dir == null || entry.is_dir === change.is_dir)) continue;
    }
    state.membershipRevisions.set(change.root_id, state.revision);
  }
}

function membershipRevision(state: WorkspaceState, rootId: string): number {
  return Math.max(state.resyncRevision, state.membershipRevisions.get(rootId) ?? 0);
}

/** Apply one workspace-scoped event to the shared source projection. */
export function applySourceTreeChanges(event: SourceChangesEvent): void {
  if (event.workspace_kind !== "project") return;
  const workspaceId = event.workspace_id.trim();
  const state = workspaces.get(workspaceId);
  if (!workspaceId || !state || state.projectId !== event.project_id) return;
  state.revision += 1;
  if (event.resync) state.resyncRevision = state.revision;
  else recordMembershipChanges(state, event.changes);
  const changed = event.resync
    ? invalidateListings(state)
    : applyChanges(state, event.changes);
  if (changed) notify(state);
  scheduleRevalidation(state);
}

/** Mark connected projections stale after an SSE continuity gap. */
export function requestSourceTreeResync(projectId: string): void {
  const id = projectId.trim();
  if (!id) return;
  for (const state of workspaces.values()) {
    if (state.projectId !== id) continue;
    state.revision += 1;
    state.resyncRevision = state.revision;
    if (invalidateListings(state)) notify(state);
    scheduleRevalidation(state);
  }
}

/** Keys any surface currently has on screen, whether or not it is live. */
function observedKeys(state: WorkspaceState): Set<string> {
  const keys = new Set<string>();
  for (const connection of state.connections.values()) {
    for (const key of connection.observed) keys.add(key);
  }
  return keys;
}

type RevalidationTarget = {
  key: string;
  connection: ConnectionRecord;
  dueAt: number;
};

/** Schedules observed listings with debounce, maximum wait, and a cooldown between reads. */
function revalidationTargets(state: WorkspaceState): RevalidationTarget[] {
  const floor = Math.max(state.cooldownUntil, state.retryAt ?? 0);
  const targets = new Map<string, RevalidationTarget>();
  for (const connection of state.connections.values()) {
    if (!connection.active) continue;
    for (const key of connection.observed) {
      if (targets.has(key)) continue;
      const record = state.listings.get(key);
      if (!record) continue;
      const dueAt = record.staleSince != null
        ? Math.min(
            state.lastDivergenceAt + REVALIDATE_IDLE_MS,
            record.staleSince + REVALIDATE_MAX_WAIT_MS,
          )
        : record.listing.watch_complete === false
          ? record.loadedAt + INCOMPLETE_REVALIDATE_MS
          : null;
      if (dueAt == null) continue;
      targets.set(key, { key, connection, dueAt: Math.max(dueAt, floor) });
    }
  }
  return [...targets.values()];
}

function scheduleRevalidation(state: WorkspaceState): void {
  if (state.revalidating) return;
  if (state.revalidateTimer != null) clearTimeout(state.revalidateTimer);
  state.revalidateTimer = undefined;
  const targets = revalidationTargets(state);
  if (targets.length === 0) {
    state.retryAt = undefined;
    return;
  }
  const nextDue = Math.min(...targets.map((target) => target.dueAt));
  state.revalidateTimer = setTimeout(() => {
    state.revalidateTimer = undefined;
    void runRevalidation(state);
  }, Math.max(0, nextDue - Date.now()));
}

async function runRevalidation(state: WorkspaceState): Promise<void> {
  const startedAt = Date.now();
  const due = revalidationTargets(state).filter(
    (target) => target.dueAt <= startedAt,
  );
  if (due.length === 0) {
    scheduleRevalidation(state);
    return;
  }
  state.revalidating = true;
  const results = await Promise.allSettled(
    due.map((target) => {
      const separator = target.key.indexOf("\0");
      return loadListing(
        state,
        target.connection,
        target.key.slice(0, separator),
        target.key.slice(separator + 1),
        true,
      );
    }),
  );
  state.revalidating = false;
  const settledAt = Date.now();
  state.cooldownUntil =
    settledAt +
    runCooldownMs(settledAt - startedAt, REVALIDATE_COOLDOWN_CAP_MS);
  state.retryAt = results.some((result) => result.status === "rejected")
    ? settledAt + INCOMPLETE_REVALIDATE_MS
    : undefined;
  scheduleRevalidation(state);
}

function evictListings(state: WorkspaceState): void {
  if (state.listings.size <= LISTING_CAP && state.childCount <= CHILD_CAP) return;
  const ordered = [...state.listings.entries()].sort(
    (left, right) => left[1].lastUsed - right[1].lastUsed,
  );
  // Visible listings stay cached to avoid immediate refetches.
  const pinned = observedKeys(state);
  for (const connection of state.connections.values()) {
    for (const key of connection.retained) pinned.add(key);
  }
  let changed = false;
  for (const [key, record] of ordered) {
    if (state.listings.size <= LISTING_CAP && state.childCount <= CHILD_CAP) break;
    if (state.flights.has(key) || pinned.has(key)) continue;
    state.listings.delete(key);
    state.childCount -= record.listing.entries.length;
    changed = true;
  }
  if (changed) notify(state);
}

function evictWorkspaces(): void {
  if (workspaces.size <= WORKSPACE_CAP) return;
  const candidates = [...workspaces.values()]
    .filter((state) => state.connections.size === 0 && state.flights.size === 0)
    .sort((left, right) => left.lastUsed - right.lastUsed);
  for (const state of candidates) {
    if (workspaces.size <= WORKSPACE_CAP) break;
    if (state.revalidateTimer != null) clearTimeout(state.revalidateTimer);
    workspaces.delete(state.workspaceId);
  }
}

/** Connect one Files surface to the shared projection for its physical roots. */
export function connectSourceTreeWorkspace(input: {
  projectId: string;
  workspaceId: string;
  browse: SourceTreeBrowse;
}): SourceTreeConnection {
  const projectId = input.projectId.trim();
  const workspaceId = input.workspaceId.trim();
  if (!projectId || !workspaceId) {
    throw new Error("A project and workspace are required for source browsing.");
  }
  const state = workspace(projectId, workspaceId);
  const token = Symbol(workspaceId);
  const connection: ConnectionRecord = {
    browse: input.browse,
    observed: new Set(),
    retained: new Set(),
    listeners: new Set(),
    active: false,
  };
  state.connections.set(token, connection);
  let connected = true;
  return {
    workspaceId,
    load: (rootId, dir, force = false) =>
      loadListing(state, connection, rootId, dir, force),
    get: (rootId, dir) => {
      const record = state.listings.get(sourceTreeListingKey(rootId, dir));
      if (!record) return undefined;
      record.lastUsed = Date.now();
      return { listing: record.listing, stale: isStale(record) };
    },
    revision: (rootId) => membershipRevision(state, rootId),
    confirm: (change) => {
      state.revision += 1;
      recordMembershipChanges(state, [change]);
      if (applyChanges(state, [change])) notify(state);
      scheduleRevalidation(state);
    },
    invalidate: (rootId, dir) => {
      const key = sourceTreeListingKey(rootId, dir);
      state.membershipRevisions.set(rootId, ++state.revision);
      if (markStale(state, key)) notify(state);
      scheduleRevalidation(state);
    },
    retain: (keys) => {
      const retained = new Set(keys);
      if (retained.size === connection.retained.size && [...retained].every((key) => connection.retained.has(key))) return;
      connection.retained = retained;
      evictListings(state);
    },
    observe: (keys, active) => {
      connection.observed = new Set(keys);
      connection.active = active;
      scheduleRevalidation(state);
    },
    subscribe: (listener) => {
      const subscriptionListener = () => listener();
      state.listeners.add(subscriptionListener);
      connection.listeners.add(subscriptionListener);
      return () => {
        connection.listeners.delete(subscriptionListener);
        state.listeners.delete(subscriptionListener);
      };
    },
    disconnect: () => {
      if (!connected) return;
      connected = false;
      for (const listener of connection.listeners) state.listeners.delete(listener);
      connection.listeners.clear();
      state.connections.delete(token);
      evictListings(state);
      scheduleRevalidation(state);
      evictWorkspaces();
    },
  };
}

/** Warms root listings before a workspace reaches the screen. */
export async function prepareSourceTreeWorkspace(input: {
  projectId: string;
  workspaceId: string;
  rootIds: readonly string[];
  browse: SourceTreeBrowse;
}): Promise<SourceDirListing[]> {
  const connection = connectSourceTreeWorkspace(input);
  try {
    const results = await Promise.allSettled(
      input.rootIds.map((rootId) => connection.load(rootId, ".")),
    );
    const rejected = results.find(
      (result): result is PromiseRejectedResult =>
        result.status === "rejected",
    );
    if (rejected) throw rejected.reason;
    return results.flatMap((result) =>
      result.status === "fulfilled" ? [result.value] : [],
    );
  } finally {
    connection.disconnect();
  }
}

export function resetSourceTreeStoreForTests(): void {
  for (const state of workspaces.values()) {
    if (state.revalidateTimer != null) clearTimeout(state.revalidateTimer);
  }
  workspaces.clear();
}
