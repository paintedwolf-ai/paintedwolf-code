import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { loadComparisonSnapshot, comparisonSelector } from "../../api/source-reader.ts";
import { sourceCacheBytes } from "./source-cache-size.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceComparisonTarget } from "../../api/http-capabilities/source-history.ts";


const MAX_COMPARISONS = 160;
const MAX_COMPARISON_BYTES = 32 * 1024 * 1024;

type Entry = {
  bytes: number;
  value?: ComparisonSnapshot;
  pending?: Promise<ComparisonSnapshot>;
};

const entries = new Map<string, Entry>();
let clientIds = new WeakMap<LycaonClient, number>();
let nextClientId = 1;

function clientId(client: LycaonClient): number {
  const held = clientIds.get(client);
  if (held !== undefined) return held;
  const id = nextClientId++;
  clientIds.set(client, id);
  return id;
}

function cacheKey(
  client: LycaonClient,
  projectId: string,
  target: Exclude<SourceComparisonTarget, { baseline: string }>,
  sessionId: string,
): string {
  const selector = "effectId" in target
    ? `effect:${target.effectId}`
    : "versionId" in target
      ? `version:${target.versionId}`
      : "turn" in target
        ? `turn:${target.fileId}:${target.sessionId}:${target.turn}:${target.markUserEdits ?? ""}`
      : "blobOid" in target
        ? `blob:${target.blobOid}:${target.beforeBlobOid ?? ""}:${target.rootId}:${target.displayPath ?? ""}`
        : `reviewed:${target.fileId}:${target.reviewedThroughOrdinal}`;
  return `${clientId(client)}\0${projectId.trim()}\0${sessionId.trim()}\0${selector}`;
}

function touch(key: string, entry: Entry): void {
  entries.delete(key);
  entries.set(key, entry);
  let bytes = [...entries.values()].reduce((total, held) => total + held.bytes, 0);
  while (entries.size > MAX_COMPARISONS || bytes > MAX_COMPARISON_BYTES) {
    const settled = [...entries].find(([, candidate]) => candidate.pending === undefined);
    const oldest = settled?.[0] ?? entries.keys().next().value;
    if (oldest === undefined) break;
    bytes -= entries.get(oldest)?.bytes ?? 0;
    entries.delete(oldest);
  }
}

/** Shares bounded comparison snapshots across version views. */
export function loadSourceComparison(
  client: LycaonClient,
  projectId: string,
  target: SourceComparisonTarget,
  sessionId = "",
): Promise<ComparisonSnapshot> {
  // Working heads and unread boundaries can change without changing the selector.
  if ("baseline" in target) return loadComparisonSnapshot(client, projectId, comparisonSelector(target), sessionId);
  const key = cacheKey(client, projectId, target, sessionId);
  const held = entries.get(key);
  if (held?.value) {
    touch(key, held);
    return Promise.resolve(held.value);
  }
  if (held?.pending) return held.pending;

  const entry: Entry = { bytes: 0 };
  entry.pending = loadComparisonSnapshot(client, projectId, comparisonSelector(target), sessionId)
    .then((value) => {
      entry.bytes = sourceCacheBytes(value, MAX_COMPARISON_BYTES);
      entry.pending = undefined;
      if (entry.bytes > MAX_COMPARISON_BYTES) {
        if (entries.get(key) === entry) entries.delete(key);
      } else {
        entry.value = value;
        if (entries.get(key) === entry) touch(key, entry);
      }
      return value;
    })
    .catch((error) => {
      if (entries.get(key) === entry) entries.delete(key);
      throw error;
    });
  touch(key, entry);
  return entry.pending;
}

export function resetSourceComparisonCacheForTests(): void {
  entries.clear();
  clientIds = new WeakMap<LycaonClient, number>();
  nextClientId = 1;
}
