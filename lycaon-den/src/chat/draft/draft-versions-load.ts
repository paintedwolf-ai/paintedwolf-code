import type { DraftVersion } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import {
  getCachedDraftVersions,
  setCachedDraftVersions,
} from "./draft-version-cache.ts";

/**
 * Cached fetch of prior draft bodies for a slot. `minPriorCount` invalidates a
 * cache captured before later supersessions grew the wire version count.
 */
export async function loadDraftVersions(
  client: LycaonClient,
  sessionId: string,
  slotId: string,
  minPriorCount = 0,
): Promise<DraftVersion[]> {
  const cached = getCachedDraftVersions(sessionId, slotId);
  if (cached && cached.length >= minPriorCount) return cached;
  const res = await client.listDraftVersions(sessionId, slotId);
  setCachedDraftVersions(sessionId, slotId, res.versions);
  return res.versions;
}
