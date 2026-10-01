import { sourceCacheBytes } from "../source/source-cache-size.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceGitReview } from "../../api/types.ts";

const MAX_GIT_REVIEW_BYTES = 8 * 1024 * 1024;
const MAX_GIT_REVIEWS = 64;

type Options = { sessionId?: string; movement?: boolean; parent?: number; cursor?: string };
type Entry = { client: LycaonClient; key: string; value?: SourceGitReview; pending?: Promise<SourceGitReview>; bytes: number };
const entries: Entry[] = [];

function trim(): void {
  let bytes = entries.reduce((total, entry) => total + entry.bytes, 0);
  while (entries.length > MAX_GIT_REVIEWS || bytes > MAX_GIT_REVIEW_BYTES) {
    const oldest = entries.shift();
    if (!oldest) break;
    bytes -= oldest.bytes;
  }
}

/** Movement identities pin commit pairs, allowing nodes to share a read. */
export function loadWalkGitReview(client: LycaonClient, projectId: string, changeId: string, options: Options = {}): Promise<SourceGitReview> {
  const key = JSON.stringify([projectId, options.sessionId ?? "", changeId, options.movement ?? null, options.parent ?? null, options.cursor ?? ""]);
  let entry = entries.find(entry => entry.client === client && entry.key === key);
  if (entry) entries.splice(entries.indexOf(entry), 1);
  else entry = { client, key, bytes: 0 };
  entries.push(entry); trim();
  if (entry.value) return Promise.resolve(entry.value);
  if (entry.pending) return entry.pending;
  const held = entry;
  held.pending = client.getProjectSourceGitReview(projectId, changeId, options).then(value => {
    held.bytes = sourceCacheBytes(value, MAX_GIT_REVIEW_BYTES);
    if (held.bytes > MAX_GIT_REVIEW_BYTES) {
      const index = entries.indexOf(held);
      if (index >= 0) entries.splice(index, 1);
    } else {
      held.value = value;
      trim();
    }
    return value;
  }).finally(() => { held.pending = undefined; });
  return held.pending;
}

export function resetWalkGitLoadsForTests(): void { entries.length = 0; }
