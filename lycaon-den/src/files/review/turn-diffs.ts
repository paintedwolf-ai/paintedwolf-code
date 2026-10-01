import type { LycaonClient } from "../../api/client.ts";
import type { SourceWalkFile } from "../../api/types.ts";
import { turnBaseline } from "./review-model.ts";
import { mergeSourceWalkFiles } from "../source/source-walk-pages.ts";
import { sourceCacheBytes } from "../source/source-cache-size.ts";
import { DIFFS_FILE_CAP } from "./diffs-inventory.ts";
import { diffsAddressKey, type TurnDiffsAddress } from "./diffs-address.ts";

/** The baseline the review lens resolves its "this turn" scope to, for one turn. */
export function turnDiffsBaseline(target: Pick<TurnDiffsAddress, "sessionId" | "turn">): string {
  return turnBaseline(target.sessionId, target.turn);
}

const PAGE_LIMIT = 500;

export type TurnDiffs = {
  files: readonly SourceWalkFile[];
  /** The turn changed more files than the page lists. */
  truncated: boolean;
};

async function fetchTurnDiffs(
  client: LycaonClient,
  projectId: string,
  target: TurnDiffsAddress,
  markUserEdits: boolean,
): Promise<TurnDiffs> {
  const baseline = turnDiffsBaseline(target);
  const files: SourceWalkFile[] = [];
  let cursor: string | undefined;
  let truncated = false;
  for (;;) {
    const page = await client.listProjectSourceWalk(projectId, {
      baseline,
      limit: PAGE_LIMIT,
      cursor,
      sessionId: target.sessionId,
      markUserEdits,
    });
    files.push(...page.files);
    const next = page.next_cursor;
    if (!next) break;
    if (cursor !== undefined && next === cursor) {
      throw new Error("The turn's changes could not be read completely. Try again.");
    }
    if (mergeSourceWalkFiles(files).length >= DIFFS_FILE_CAP) {
      truncated = true;
      break;
    }
    cursor = next;
  }
  return { files: mergeSourceWalkFiles(files).slice(0, DIFFS_FILE_CAP), truncated };
}

const MAX_PAGES = 8;
const MAX_BYTES = 8 * 1024 * 1024;
type Entry = {
  client: LycaonClient;
  projectId: string;
  key: string;
  markUserEdits: boolean;
  generation: number;
  bytes: number;
  value?: TurnDiffs;
  pending?: Promise<TurnDiffs>;
};
const entries: Entry[] = [];

/** Source changes drop cached turn inventories. */
export function invalidateTurnDiffs(projectId?: string): void {
  const id = projectId?.trim();
  for (const entry of entries) {
    if (id !== undefined && entry.projectId !== id) continue;
    entry.generation++;
    entry.value = undefined;
    entry.bytes = 0;
    entry.pending = undefined;
  }
}

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

/** Shares one bounded inventory per turn across the tab and its reopens. */
export function loadTurnDiffs(
  client: LycaonClient,
  projectId: string,
  target: TurnDiffsAddress,
  markUserEdits: boolean,
): Promise<TurnDiffs> {
  const id = projectId.trim();
  const key = diffsAddressKey(target);
  let entry = entries.find((held) =>
    held.client === client && held.projectId === id && held.key === key && held.markUserEdits === markUserEdits,
  );
  if (!entry) entry = { client, projectId: id, key, markUserEdits, generation: 0, bytes: 0 };
  touch(entry);
  if (entry.value) return Promise.resolve(entry.value);
  if (entry.pending) return entry.pending;
  const held = entry;
  const generation = held.generation;
  const pending = fetchTurnDiffs(client, id, target, markUserEdits).then((value) => {
    if (held.generation === generation && entries.includes(held)) {
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

export function resetTurnDiffsForTests(): void {
  entries.length = 0;
}
