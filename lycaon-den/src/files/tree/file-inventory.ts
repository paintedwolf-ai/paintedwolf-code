import type { LycaonClient } from "../../api/client.ts";
import type { SourceSearchHighlight, SourceSearchMatch, SourceSearchResponse } from "../../api/types.ts";

export type InventoryAddress = {
  rootId: string;
  path: string;
};

export type InventoryFile = InventoryAddress & {
  basename: string;
  dir: string;
  /** UTF-16 indexes into path that the query matched. */
  matchIndexes: number[];
};

const mruByProject = new Map<string, InventoryAddress[]>();

const MRU_CAP = 50;
const QUICK_OPEN_CAP = 50;

export function resetFileInventoryForTests(): void {
  mruByProject.clear();
}

export function recordInventoryOpen(projectId: string, address: InventoryAddress): void {
  if (!address.rootId || !address.path) return;
  const key = inventoryAddressKey(address);
  const previous = mruByProject.get(projectId) ?? [];
  mruByProject.set(projectId, [
    { rootId: address.rootId, path: address.path },
    ...previous.filter((entry) => inventoryAddressKey(entry) !== key),
  ].slice(0, MRU_CAP));
}

export function inventoryMru(projectId: string): readonly InventoryAddress[] {
  return mruByProject.get(projectId) ?? [];
}

function inventoryAddressKey(address: InventoryAddress): string {
  return JSON.stringify([address.rootId, address.path]);
}

/** Publishes usable pages while discovery continues; cancellation belongs to the view. */
export async function watchIndexedFiles(
  client: LycaonClient,
  projectId: string,
  query: string,
  publish: (files: readonly InventoryFile[], page: SourceSearchResponse) => void,
  opts?: { sessionId?: string; rootId?: string; signal?: AbortSignal },
): Promise<void> {
  let attempt = 0;
  for (;;) {
    opts?.signal?.throwIfAborted();
    const result = await client.searchProjectSource(projectId, query, {
      rootId: opts?.rootId,
      sessionId: opts?.sessionId,
      limit: QUICK_OPEN_CAP,
      signal: opts?.signal,
    });
    opts?.signal?.throwIfAborted();
    publish(result.matches.map(toInventoryFile), result);
    if (!result.refreshing && result.state !== "warming") return;
    const delay = Math.min(Math.max(result.retry_after_ms ?? 0, 750 * 2 ** attempt), 5000);
    attempt = Math.min(attempt + 1, 3);
    await abortableDelay(delay, opts?.signal);
  }
}

function abortableDelay(ms: number, signal?: AbortSignal): Promise<void> {
  if (!signal) return new Promise((resolve) => setTimeout(resolve, ms));
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal.reason instanceof Error ? signal.reason : new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function toInventoryFile(e: SourceSearchMatch): InventoryFile {
  const path = e.path;
  const slash = path.lastIndexOf("/");
  return {
    rootId: e.root_id,
    path,
    basename: slash < 0 ? path : path.slice(slash + 1),
    dir: slash < 0 ? "" : path.slice(0, slash),
    matchIndexes: highlightIndexes(path, e.highlights),
  };
}

/** Expands code point ranges into the UTF-16 indexes they cover. */
export function highlightIndexes(path: string, highlights: readonly SourceSearchHighlight[]): number[] {
  if (!highlights.length) return [];
  const out: number[] = [];
  let codePoint = 0;
  let unit = 0;
  let range = 0;
  for (const char of path) {
    while (range < highlights.length && highlights[range]!.end <= codePoint) range++;
    const current = highlights[range];
    if (!current) break;
    if (codePoint >= current.start) {
      for (let i = 0; i < char.length; i++) out.push(unit + i);
    }
    codePoint++;
    unit += char.length;
  }
  return out;
}

export function isSubsequence(text: string, q: string): boolean {
  let at = 0;
  for (let i = 0; i < text.length && at < q.length; i++) {
    if (text[i] === q[at]) at++;
  }
  return at === q.length;
}

/** Orders the idle picker: recent opens, then open buffers, then indexed files. */
export function orderIdleInventory(
  entries: readonly InventoryFile[],
  opts?: { recent?: readonly InventoryAddress[]; open?: readonly InventoryAddress[] },
): InventoryFile[] {
  const byAddress = new Map(entries.map((entry) => [inventoryAddressKey(entry), entry]));
  const seen = new Set<string>();
  const out: InventoryFile[] = [];
  for (const address of [...(opts?.recent ?? []), ...(opts?.open ?? []), ...entries]) {
    const key = inventoryAddressKey(address);
    if (seen.has(key)) continue;
    const entry = byAddress.get(key);
    if (!entry) continue;
    seen.add(key);
    out.push(entry);
    if (out.length >= QUICK_OPEN_CAP) break;
  }
  return out;
}

export function filterSymbolsByQuery<T extends { name: string }>(
  symbols: readonly T[],
  query: string,
): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return symbols.slice();
  return symbols.filter((s) => isSubsequence(s.name.toLowerCase(), q));
}
