import { createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceComparisonDigest, SourceComparisonSelector } from "../../api/types.ts";

/** A row's comparison and the inventory fact it measures. */
export type DiffsDigestRequest = { key: string; selector: SourceComparisonSelector; fact: string };

const BATCH = 64;
const CONCURRENT_BATCHES = 2;
const RETAINED = 4096;

type Held = SourceComparisonDigest | Promise<void>;
/** A batch claimed in the cache before any request is sent. */
type Reserved = { batch: DiffsDigestRequest[]; keys: string[]; pending: Promise<void>; settle: () => void };
const held = new WeakMap<LycaonClient, Map<string, Held>>();

function cacheFor(client: LycaonClient): Map<string, Held> {
  let cache = held.get(client);
  if (!cache) { cache = new Map(); held.set(client, cache); }
  return cache;
}

function retain(cache: Map<string, Held>, key: string, value: Held): void {
  cache.delete(key);
  cache.set(key, value);
  while (cache.size > RETAINED) {
    const oldest = cache.keys().next().value;
    if (oldest === undefined) break;
    cache.delete(oldest);
  }
}

/** Batches and caches host measurements for comparison line counts and row heights. */
export function createDiffsDigests(args: {
  client: () => LycaonClient | null;
  projectId: () => string;
  sessionId: () => string | undefined;
  requests: () => readonly DiffsDigestRequest[];
}) {
  const [revision, setRevision] = createSignal(0);
  const controller = new AbortController();
  onCleanup(() => controller.abort());

  const cacheKey = (request: DiffsDigestRequest) =>
    JSON.stringify([args.projectId(), args.sessionId() ?? null, request.selector, request.fact]);

  async function measure(client: LycaonClient, cache: Map<string, Held>, held: Reserved): Promise<void> {
    try {
      const answer = await client.digestSourceComparisons(untrack(args.projectId), {
        ...(untrack(args.sessionId) ? { session_id: untrack(args.sessionId) } : {}),
        sources: held.batch.map((request) => request.selector),
      }, controller.signal);
      answer.digests.forEach((digest, index) => { const key = held.keys[index]; if (key) retain(cache, key, digest); });
    } catch {
      // Release batch on measurement failure.
      release(cache, held);
    }
    setRevision((value) => value + 1);
  }

  /** Releases batch reservation. */
  function release(cache: Map<string, Held>, held: Reserved): void {
    for (const key of held.keys) if (cache.get(key) === held.pending) cache.delete(key);
  }

  createEffect(() => {
    const client = args.client();
    const requests = args.requests();
    if (!client || !requests.length) return;
    untrack(() => {
      const cache = cacheFor(client);
      // Wait on in-flight requests.
      const waiting = requests.map((request) => cache.get(cacheKey(request))).filter((value) => value instanceof Promise);
      if (waiting.length) void Promise.all(waiting).then(() => setRevision((value) => value + 1));
      const missing = requests.filter((request) => !cache.has(cacheKey(request)));
      if (!missing.length) return;
      // Reserve entries to prevent duplicate requests.
      const batches: Reserved[] = [];
      for (let at = 0; at < missing.length; at += BATCH) {
        const batch = missing.slice(at, at + BATCH);
        const keys = batch.map(cacheKey);
        let settle!: () => void;
        const pending = new Promise<void>((resolve) => { settle = resolve; });
        for (const key of keys) retain(cache, key, pending);
        batches.push({ batch, keys, pending, settle });
      }
      let next = 0;
      const finish = (held: Reserved): void => held.settle();
      const lane = async (): Promise<void> => {
        while (next < batches.length && !controller.signal.aborted) {
          const held = batches[next++]!;
          await measure(client, cache, held);
          finish(held);
        }
      };
      // Release unstarted batches on abort.
      const abandon = () => {
        for (let at = next; at < batches.length; at++) {
          const held = batches[at]!;
          release(cache, held);
          finish(held);
        }
        next = batches.length;
      };
      controller.signal.addEventListener("abort", abandon, { once: true });
      const lanes = Math.min(CONCURRENT_BATCHES, batches.length);
      void Promise.all(Array.from({ length: lanes }, () => lane()))
        .finally(() => controller.signal.removeEventListener("abort", abandon));
    });
  });

  const byKey = createMemo(() => {
    void revision();
    const client = args.client();
    const answers = new Map<string, SourceComparisonDigest>();
    if (!client) return answers;
    const cache = cacheFor(client);
    for (const request of args.requests()) {
      const value = cache.get(cacheKey(request));
      if (value && !(value instanceof Promise)) answers.set(request.key, value);
    }
    return answers;
  });

  return {
    /** Comparison digest for a key, if measured. */
    digest: (key: string): SourceComparisonDigest | undefined => byKey().get(key),
  };
}

/** Net added and removed lines, or null if unmeasured. */
export function digestStat(digest: SourceComparisonDigest | undefined): { added: number; removed: number } | null {
  if (!digest || digest.failure) return null;
  if (!digest.in_range) return { added: 0, removed: 0 };
  return digest.summary ? { added: digest.summary.added, removed: digest.summary.removed } : null;
}

/** Comparison involves binary content on either endpoint. */
export function digestIsBinary(digest: SourceComparisonDigest | undefined): boolean {
  if (!digest || digest.failure || !digest.summary) return false;
  return digest.summary.before?.availability === "binary" || digest.summary.after?.availability === "binary";
}

/** Whether the comparison has zero net line changes. */
export function digestIsNoop(digest: SourceComparisonDigest | undefined): boolean {
  if (digestIsBinary(digest)) return false;
  const stat = digestStat(digest);
  return !!stat && stat.added === 0 && stat.removed === 0;
}

