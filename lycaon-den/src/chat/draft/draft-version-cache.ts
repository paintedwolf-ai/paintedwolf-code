import type { DraftVersion } from "../../api/types.ts";
import { ByteCache, retainedValueBytes } from "../../utils/byte-cache.ts";

const cache = new ByteCache<string, DraftVersion[]>(4 * 1024 * 1024, 64);

function draftVersionCacheKey(sessionId: string, slotId: string): string {
  return `${sessionId.trim()}\0${slotId.trim()}`;
}

export function getCachedDraftVersions(
  sessionId: string,
  slotId: string,
): DraftVersion[] | undefined {
  const key = draftVersionCacheKey(sessionId, slotId);
  return cache.get(key);
}

export function setCachedDraftVersions(
  sessionId: string,
  slotId: string,
  versions: DraftVersion[],
): void {
  const key = draftVersionCacheKey(sessionId, slotId);
  cache.set(key, [...versions], key.length * 2 + retainedValueBytes(versions));
}

export function clearDraftVersionCache(sessionId?: string): void {
  if (!sessionId?.trim()) {
    cache.clear();
    return;
  }
  const prefix = `${sessionId.trim()}\0`;
  for (const key of cache.keys()) {
    if (key.startsWith(prefix)) cache.delete(key);
  }
}
