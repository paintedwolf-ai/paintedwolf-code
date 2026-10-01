import type { LycaonClient } from "../../api/client.ts";
import { sourceCacheBytes } from "../source/source-cache-size.ts";
import { DIFFS_FILE_CAP, type DiffsFile } from "./diffs-inventory.ts";
import { diffsAddressKey, type GitDiffsAddress } from "./diffs-address.ts";

/** The review endpoint's largest page. */
const PAGE_LIMIT = 500;

export type GitDiffs = {
  files: readonly DiffsFile[];
  /** The commits change more files than the page lists. */
  truncated: boolean;
};

async function fetchGitDiffs(client: LycaonClient, projectId: string, address: GitDiffsAddress): Promise<GitDiffs> {
  const target = { rootId: address.rootId, beforeCommit: address.beforeCommit, afterCommit: address.afterCommit };
  const files: DiffsFile[] = [];
  let cursor: string | undefined = undefined;
  for (;;) {
    const page = await client.getProjectSourceRevisionReview(projectId, target, {
      cursor, limit: Math.min(PAGE_LIMIT, DIFFS_FILE_CAP - files.length),
    });
    if (page.before_commit !== address.beforeCommit || page.after_commit !== address.afterCommit) {
      throw new Error("The Git review answered for different commits. Try again.");
    }
    for (const file of page.files) files.push({ kind: "git", address, file });
    const next = page.next_cursor;
    if (!next) return { files, truncated: false };
    if (files.length >= DIFFS_FILE_CAP) return { files, truncated: true };
    cursor = next;
  }
}

const MAX_PAGES = 8;
const MAX_BYTES = 8 * 1024 * 1024;
type Entry = {
  client: LycaonClient;
  projectId: string;
  key: string;
  bytes: number;
  value?: GitDiffs;
  pending?: Promise<GitDiffs>;
};
const entries: Entry[] = [];

function touch(entry: Entry): void {
  const at = entries.indexOf(entry);
  if (at >= 0) entries.splice(at, 1);
  entries.push(entry);
  let bytes = entries.reduce((total, held) => total + held.bytes, 0);
  while (entries.length > MAX_PAGES || bytes > MAX_BYTES) {
    const oldest = entries.shift();
    if (!oldest) break;
    bytes -= oldest.bytes;
  }
}

/** Two commits never change, so one bounded inventory serves the tab and its reopens. */
export function loadGitDiffs(client: LycaonClient, projectId: string, address: GitDiffsAddress): Promise<GitDiffs> {
  const id = projectId.trim();
  const key = diffsAddressKey(address);
  let entry = entries.find((held) => held.client === client && held.projectId === id && held.key === key);
  if (!entry) entry = { client, projectId: id, key, bytes: 0 };
  touch(entry);
  if (entry.value) return Promise.resolve(entry.value);
  if (entry.pending) return entry.pending;
  const held = entry;
  const pending = fetchGitDiffs(client, id, address).then((value) => {
    if (entries.includes(held)) {
      held.bytes = sourceCacheBytes(value, MAX_BYTES);
      if (held.bytes > MAX_BYTES) entries.splice(entries.indexOf(held), 1);
      else {
        held.value = value;
        touch(held);
      }
    }
    return value;
  }).finally(() => { if (held.pending === pending) held.pending = undefined; });
  held.pending = pending;
  return pending;
}

export function resetGitDiffsForTests(): void {
  entries.length = 0;
}
