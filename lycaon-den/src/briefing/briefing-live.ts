import { createEffect, createSignal, onCleanup, untrack, type Accessor } from "solid-js";
import type { BriefingEvent, BriefingRequest, BriefingSnapshot } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";

export type BriefingProfileId = BriefingRequest["profile_id"];

/** A target as a consumer names it: its profile and the host's wire target. */
export type BriefingTarget = { profileId: BriefingProfileId; ref: Record<string, unknown> };

/** What a consumer renders: the host snapshot, and whether local waiting ended. */
export type BriefingView = {
  snapshot: BriefingSnapshot | null;
  /** True once a reconciliation found the attempt still running; stop waiting. */
  stalled: boolean;
};

export type BriefingClock = {
  now: () => number;
  setTimeout: (run: () => void, ms: number) => unknown;
  clearTimeout: (handle: unknown) => void;
};

const systemClock: BriefingClock = {
  now: () => Date.now(),
  setTimeout: (run, ms) => setTimeout(run, ms),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/** Automatic requests wait this long so a quick scroll past does not ask. */
export const BRIEFING_DEBOUNCE_MS = 250;
/** Inactive targets kept for instant reopen. */
export const BRIEFING_INACTIVE_LIMIT = 512;
/** Snapshots held for a request whose reply has not named its target yet. */
const UNCLAIMED_LIMIT = 64;

type Entry = {
  projectId: string;
  target: BriefingTarget;
  view: Accessor<BriefingView>;
  setView: (view: BriefingView) => void;
  targetKey: string | null;
  users: number;
  inFlight: boolean;
  debounce: unknown;
  reconcile: unknown;
  reconciled: boolean;
};

type Store = {
  client: () => BriefingsClient | null;
  clock: BriefingClock;
  entries: Map<string, Entry>;
  /** Entries by the host target they resolved to; requests of different shapes can name one target. */
  byTargetKey: Map<string, Set<string>>;
  /** Events no entry claimed yet: a quick attempt can settle before its reply arrives. */
  unclaimed: Map<string, BriefingSnapshot>;
  /** Lowest epoch still valid for each project and profile. */
  resets: Map<string, bigint>;
};

const noClient = () => null;

const store: Store = { client: noClient, clock: systemClock, entries: new Map(), byTargetKey: new Map(), unclaimed: new Map(), resets: new Map() };

/** Names where requests go; the connection binds the current host client. */
export function setBriefingClient(client: () => BriefingsClient | null): void {
  store.client = client;
}

export function setBriefingClockForTests(clock: BriefingClock | null): void {
  store.clock = clock ?? systemClock;
}

export function briefingEntryCountForTests(): number {
  return store.entries.size;
}

export function resetBriefingLiveForTests(): void {
  for (const entry of store.entries.values()) clearTimers(entry);
  store.entries.clear();
  store.byTargetKey.clear();
  store.unclaimed.clear();
  store.resets.clear();
  store.client = noClient;
  store.clock = systemClock;
}

function stableJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((k) => `${JSON.stringify(k)}:${stableJson(record[k])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

function entryKey(projectId: string, target: BriefingTarget): string {
  return `${projectId}\0${target.profileId}\0${stableJson(target.ref)}`;
}

function targetKeyIndex(projectId: string, profileId: string, targetKey: string): string {
  return `${projectId}\0${profileId}\0${targetKey}`;
}

function epochFloor(projectId: string, profileId: string): bigint {
  return store.resets.get(`${projectId}\0${profileId}`) ?? 0n;
}

/** Newer epoch wins, then newer revision; counters compare as integers. */
function isNewer(next: BriefingSnapshot, current: BriefingSnapshot | null): boolean {
  if (!current) return true;
  const [nextEpoch, epoch] = [BigInt(next.cache_epoch), BigInt(current.cache_epoch)];
  if (nextEpoch !== epoch) return nextEpoch > epoch;
  return BigInt(next.revision) >= BigInt(current.revision);
}

function clearTimers(entry: Entry): void {
  if (entry.debounce !== undefined) store.clock.clearTimeout(entry.debounce);
  if (entry.reconcile !== undefined) store.clock.clearTimeout(entry.reconcile);
  entry.debounce = undefined;
  entry.reconcile = undefined;
}

function ensureEntry(projectId: string, target: BriefingTarget): Entry {
  const key = entryKey(projectId, target);
  const existing = store.entries.get(key);
  if (existing) {
    store.entries.delete(key);
    store.entries.set(key, existing);
    return existing;
  }
  const [view, setView] = createSignal<BriefingView>({ snapshot: null, stalled: false });
  const entry: Entry = { projectId, target, view, setView, targetKey: null, users: 0, inFlight: false,
    debounce: undefined, reconcile: undefined, reconciled: false };
  store.entries.set(key, entry);
  evictInactive();
  return entry;
}

function evictInactive(): void {
  let inactive = 0;
  for (const entry of store.entries.values()) if (entry.users === 0) inactive++;
  for (const [key, entry] of store.entries) {
    if (inactive <= BRIEFING_INACTIVE_LIMIT) return;
    if (entry.users > 0) continue;
    clearTimers(entry);
    store.entries.delete(key);
    unindex(entry);
    inactive--;
  }
}

function unindex(entry: Entry): void {
  if (!entry.targetKey) return;
  const index = targetKeyIndex(entry.projectId, entry.target.profileId, entry.targetKey);
  const keys = store.byTargetKey.get(index);
  keys?.delete(entryKey(entry.projectId, entry.target));
  if (keys?.size === 0) store.byTargetKey.delete(index);
}

function apply(entry: Entry, snapshot: BriefingSnapshot, fromReconcile: boolean): void {
  if (BigInt(snapshot.cache_epoch) < epochFloor(entry.projectId, entry.target.profileId)) return;
  const current = entry.view();
  if (!isNewer(snapshot, current.snapshot)) return;
  if (entry.targetKey !== snapshot.target_key) {
    unindex(entry);
    entry.targetKey = snapshot.target_key;
    const index = targetKeyIndex(entry.projectId, snapshot.profile_id, snapshot.target_key);
    const keys = store.byTargetKey.get(index) ?? new Set<string>();
    keys.add(entryKey(entry.projectId, entry.target));
    store.byTargetKey.set(index, keys);
    const early = store.unclaimed.get(index);
    store.unclaimed.delete(index);
    if (early && isNewer(early, snapshot)) snapshot = early;
  }
  if (entry.reconcile !== undefined) {
    store.clock.clearTimeout(entry.reconcile);
    entry.reconcile = undefined;
  }
  const pending = snapshot.status === "pending";
  entry.setView({ snapshot, stalled: pending && fromReconcile });
  if (pending && !fromReconcile && !entry.reconciled && snapshot.reconcile_after) {
    const wait = Math.max(0, Date.parse(snapshot.reconcile_after) - store.clock.now());
    entry.reconcile = store.clock.setTimeout(() => {
      entry.reconcile = undefined;
      entry.reconciled = true;
      void send(entry, "automatic", true);
    }, wait);
  }
}

async function send(entry: Entry, trigger: "automatic" | "manual", reconciling = false): Promise<void> {
  const client = store.client();
  if (!client) return;
  entry.inFlight = true;
  try {
    const snapshot = await client.requestBriefing(entry.projectId, {
      profile_id: entry.target.profileId, target_ref: entry.target.ref, trigger,
    });
    apply(entry, snapshot, reconciling);
  } catch {
    // A refused or failed request leaves the surface as it is without briefings.
    if (reconciling) entry.setView({ ...entry.view(), stalled: true });
  } finally {
    entry.inFlight = false;
  }
}

/**
 * Asks for a target. Automatic requests are debounced and coalesce with one
 * already in flight unless `fresh`, which reopen and reconnect use; manual
 * requests go at once.
 */
export function requestBriefing(projectId: string, target: BriefingTarget, opts: { manual?: boolean; fresh?: boolean } = {}): void {
  const entry = ensureEntry(projectId, target);
  if (opts.manual) {
    if (entry.debounce !== undefined) store.clock.clearTimeout(entry.debounce);
    entry.debounce = undefined;
    entry.reconciled = false;
    void send(entry, "manual");
    return;
  }
  if (entry.debounce !== undefined || (entry.inFlight && !opts.fresh)) return;
  entry.debounce = store.clock.setTimeout(() => {
    entry.debounce = undefined;
    entry.reconciled = false;
    void send(entry, "automatic");
  }, BRIEFING_DEBOUNCE_MS);
}

/**
 * Watches a target while the caller's owner lives; the request outlives it.
 * A changed target, such as a card settling while open, is asked for anew.
 */
export function useBriefing(projectId: Accessor<string | undefined>, target: Accessor<BriefingTarget | null>): Accessor<BriefingView> {
  const [current, setCurrent] = createSignal<Entry | null>(null);
  const attach = (next: Entry | null) => {
    const previous = current();
    if (previous === next) return false;
    if (previous) {
      previous.users--;
      if (previous.users === 0 && previous.debounce !== undefined) {
        store.clock.clearTimeout(previous.debounce);
        previous.debounce = undefined;
      }
    }
    if (next) next.users++;
    setCurrent(next);
    evictInactive();
    return true;
  };
  createEffect(() => {
    const id = projectId();
    const wanted = target();
    untrack(() => {
      if (!id || !wanted) return attach(null);
      const entry = ensureEntry(id, wanted);
      // A held answer stays current through events and reconnect, so only a
      // target never answered here is asked for.
      if (attach(entry) && !entry.view().snapshot) requestBriefing(id, wanted, { fresh: true });
    });
  });
  onCleanup(() => attach(null));
  return () => current()?.view() ?? { snapshot: null, stalled: false };
}

/** Applies one `briefing` topic event. */
export function applyBriefingEvent(projectId: string, event: BriefingEvent): void {
  if (event.kind === "reset" && event.cache_epoch) {
    const floor = BigInt(event.cache_epoch);
    for (const profileId of event.profile_ids ?? []) store.resets.set(`${projectId}\0${profileId}`, floor);
    for (const entry of store.entries.values()) {
      const snapshot = entry.view().snapshot;
      if (entry.projectId === projectId && snapshot && BigInt(snapshot.cache_epoch) < epochFloor(projectId, entry.target.profileId)) {
        clearTimers(entry);
        entry.setView({ snapshot: null, stalled: false });
      }
    }
    for (const [index, held] of store.unclaimed) {
      if (index.startsWith(`${projectId}\0`) && BigInt(held.cache_epoch) < epochFloor(projectId, held.profile_id)) store.unclaimed.delete(index);
    }
    return;
  }
  const snapshot = event.snapshot;
  if (event.kind !== "snapshot" || !snapshot) return;
  const index = targetKeyIndex(projectId, snapshot.profile_id, snapshot.target_key);
  const keys = store.byTargetKey.get(index);
  if (!keys) {
    const held = store.unclaimed.get(index);
    store.unclaimed.delete(index);
    store.unclaimed.set(index, held && !isNewer(snapshot, held) ? held : snapshot);
    if (store.unclaimed.size > UNCLAIMED_LIMIT) store.unclaimed.delete(store.unclaimed.keys().next().value!);
    return;
  }
  for (const key of [...keys]) {
    const entry = store.entries.get(key);
    if (entry) apply(entry, snapshot, false);
  }
}

/** Re-asks every watched target after the event stream reopens. */
export function requestBriefingResync(): void {
  // Asking moves each entry to the most recent end, so walk a copy.
  for (const entry of [...store.entries.values()]) {
    if (entry.users > 0) requestBriefing(entry.projectId, entry.target, { fresh: true });
  }
}

/** Drops every target, as when summaries are turned off. */
export function clearBriefings(): void {
  for (const entry of store.entries.values()) {
    clearTimers(entry);
    entry.setView({ snapshot: null, stalled: false });
  }
}
