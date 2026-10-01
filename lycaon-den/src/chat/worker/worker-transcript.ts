import type { LycaonClient } from "../../api/client.ts";
import type { Message, WorkerTask, SessionTranscriptPage } from "../../api/types.ts";
import { retainedValueBytes } from "../../utils/byte-cache.ts";
import type { TranscriptWindow } from "../transcript/layout/transcript-window.ts";
import {
  workerOverlayOpen,
  workerShowsAcceptedBranchEdits,
} from "./worker-branch-model.ts";
import { taskJobIdFromToolMessage } from "../task/task-result-model.ts";

/** One worker's resident transcript: a live tail plus older pages, rows materialized in Ord order. */
export type WorkerTranscript = {
  window: TranscriptWindow;
  rows: Message[];
  /** A durable tail page has been installed since this entry was created or restored. */
  hydrated: boolean;
};

export type WorkerTranscriptCache = Readonly<Record<string, WorkerTranscript>>;

export function workerTranscriptRows(
  cache: WorkerTranscriptCache | undefined,
  workerId: string,
): readonly Message[] | undefined {
  return cache?.[workerId]?.rows;
}

/** Evict least recently touched transcripts beyond this many bytes or entries. */
const WORKER_TRANSCRIPT_CACHE_BYTES = 8 * 1024 * 1024;
const WORKER_TRANSCRIPT_CACHE_ENTRIES = 32;

/** Recency and open readers decide which cached worker transcripts stay resident. */
export class WorkerTranscriptRetention {
  private readonly recent = new Map<string, true>();
  private readonly readers = new Map<string, number>();

  retain(workerId: string): () => void {
    this.readers.set(workerId, (this.readers.get(workerId) ?? 0) + 1);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const remaining = (this.readers.get(workerId) ?? 1) - 1;
      if (remaining > 0) this.readers.set(workerId, remaining);
      else this.readers.delete(workerId);
    };
  }

  /** Ids to evict after `touched` changed, least recently touched first. */
  evictions(cache: WorkerTranscriptCache, touched: string): string[] {
    this.recent.delete(touched);
    this.recent.set(touched, true);
    for (const id of Object.keys(cache)) if (!this.recent.has(id)) this.recent.set(id, true);
    const sizes = new Map(
      Object.entries(cache).map(([id, entry]) => [
        id,
        entry.rows.reduce((total, row) => total + workerRowBytes(row), 0),
      ]),
    );
    let bytes = [...sizes.values()].reduce((sum, size) => sum + size, 0);
    let count = sizes.size;
    const evicted: string[] = [];
    for (const id of [...this.recent.keys()]) {
      const size = sizes.get(id);
      if (size === undefined) {
        this.recent.delete(id);
        continue;
      }
      if (bytes <= WORKER_TRANSCRIPT_CACHE_BYTES && count <= WORKER_TRANSCRIPT_CACHE_ENTRIES) break;
      if (id === touched || this.readers.has(id)) continue;
      bytes -= size;
      count--;
      evicted.push(id);
      this.recent.delete(id);
    }
    return evicted;
  }
}

const rowSizes = new WeakMap<Message, number>();
function workerRowBytes(row: Message): number {
  let size = rowSizes.get(row);
  if (size === undefined) {
    size = retainedValueBytes(row);
    rowSizes.set(row, size);
  }
  return size;
}

export function latestWorkerForChildSession(
  workers: readonly WorkerTask[],
  childSessionId: string,
): WorkerTask | undefined {
  const sid = childSessionId.trim();
  if (!sid) return undefined;
  let latest: WorkerTask | undefined;
  for (const worker of workers) {
    if (worker.child_session_id?.trim() !== sid) continue;
    if (
      !latest ||
      worker.created_at > latest.created_at ||
      (worker.created_at === latest.created_at && worker.id > latest.id)
    ) {
      latest = worker;
    }
  }
  return latest;
}

/** Cold-cache schedule key independent of status-only updates. */
export function workersColdCacheRevision(
  parentMessages: readonly Message[],
  workers: readonly WorkerTask[],
  cache: WorkerTranscriptCache,
  selectedWorkerId?: string | null,
): string {
  const cold = workersNeedingTranscriptHydrate(
    parentMessages,
    workers,
    cache,
    { selectedWorkerId },
  )
    .map((w) => w.id)
    .sort()
    .join(",");
  const selected = selectedWorkerId?.trim();
  if (!selected) return cold;
  const row = workers.find((w) => w.id === selected);
  const progress =
    row && !cache[selected]?.rows.length
      ? `${row.status}\0${row.tool_loops_used ?? 0}\0${row.child_session_id ?? ""}`
      : "";
  return `${selected}\0${progress}\0${cold}`;
}

/** The worker's newest durable page, or undefined while the host cannot answer. */
export async function fetchWorkerTranscriptTail(
  client: LycaonClient,
  worker: WorkerTask,
): Promise<SessionTranscriptPage | undefined> {
  const childId = worker.child_session_id?.trim();
  if (!childId) return undefined;
  try {
    return await client.listSessionMessages(childId, { workerId: worker.id });
  } catch {
    return undefined;
  }
}

function workersReferencedInParentMessages(
  parentMessages: readonly Message[],
  workers: readonly WorkerTask[],
): WorkerTask[] {
  const ids = new Set<string>();
  for (const msg of parentMessages) {
    const summaryJob = msg.worker_summary?.worker_id?.trim();
    if (summaryJob) ids.add(summaryJob);
    if (msg.role === "tool") {
      const jobId = taskJobIdFromToolMessage(msg);
      if (jobId) ids.add(jobId);
    }
  }
  if (ids.size === 0) return [];
  return workers.filter((w) => ids.has(w.id));
}

export function inflightWorkerPrefetchTargets(
  workers: readonly WorkerTask[],
): WorkerTask[] {
  return workers.filter(
    (worker) =>
      worker.status === "running" ||
      worker.status === "pending" ||
      workerOverlayOpen(worker),
  );
}

export function parentReferencedWorkerPrefetchTargets(
  parentMessages: readonly Message[],
  workers: readonly WorkerTask[],
  cache: WorkerTranscriptCache,
): WorkerTask[] {
  const picks: WorkerTask[] = [];
  for (const worker of workersReferencedInParentMessages(parentMessages, workers)) {
    if (!workerShowsAcceptedBranchEdits(worker)) continue;
    const entry = cache[worker.id];
    if (entry?.hydrated || entry?.rows.length) continue;
    picks.push(worker);
  }
  return picks;
}

function workerNeedsChildTranscriptHydrate(
  worker: WorkerTask,
  cache: WorkerTranscriptCache,
): boolean {
  const childId = worker.child_session_id?.trim();
  return Boolean(childId && !cache[worker.id]?.hydrated);
}

export function workersNeedingTranscriptHydrate(
  parentMessages: readonly Message[],
  workers: readonly WorkerTask[],
  cache: WorkerTranscriptCache,
  opts?: { selectedWorkerId?: string | null },
): WorkerTask[] {
  const byId = new Map<string, WorkerTask>();
  const selected = opts?.selectedWorkerId?.trim();

  if (selected) {
    const row = workers.find((w) => w.id === selected);
    if (row && workerNeedsChildTranscriptHydrate(row, cache)) {
      byId.set(row.id, row);
    }
  }

  for (const worker of inflightWorkerPrefetchTargets(workers)) {
    if (workerNeedsChildTranscriptHydrate(worker, cache)) {
      byId.set(worker.id, worker);
    }
  }

  for (const worker of parentReferencedWorkerPrefetchTargets(
    parentMessages,
    workers,
    cache,
  )) {
    byId.set(worker.id, worker);
  }

  if (selected) {
    const first = byId.get(selected);
    if (first) {
      byId.delete(selected);
      return [first, ...byId.values()];
    }
  }
  return [...byId.values()];
}

export async function hydrateWorkerTranscripts(
  client: LycaonClient,
  parentMessages: readonly Message[],
  workers: readonly WorkerTask[],
  cache: WorkerTranscriptCache,
  onLoaded: (workerId: string, page: SessionTranscriptPage) => void,
  opts?: { selectedWorkerId?: string | null; limit?: number },
): Promise<void> {
  const picks = workersNeedingTranscriptHydrate(
    parentMessages,
    workers,
    cache,
    { selectedWorkerId: opts?.selectedWorkerId },
  ).slice(0, opts?.limit ?? 8);
  await Promise.all(
    picks.map(async (worker) => {
      const page = await fetchWorkerTranscriptTail(client, worker);
      if (page) onLoaded(worker.id, page);
    }),
  );
}
