import type { SourceGitReviewFile, SourceWalkFile } from "../../api/types.ts";
import { resolvedScope } from "../tree/scope-resolution.ts";
import type { TurnDiffs } from "./turn-diffs.ts";
import type { GitDiffs } from "./git-diffs.ts";
import type { GitDiffsAddress } from "./diffs-address.ts";

/** Maximum files displayed on a diffs page. */
export const DIFFS_FILE_CAP = 500;

/** Single changed file in a diffs page inventory. */
export type DiffsFile =
  | { kind: "walk"; file: SourceWalkFile }
  | { kind: "git"; address: GitDiffsAddress; file: SourceGitReviewFile };

export type DiffsInventory = {
  files: readonly DiffsFile[];
  /** Whether file count exceeds DIFFS_FILE_CAP. */
  truncated: boolean;
  /** Whether the inventory read has finished. */
  settled: boolean;
  error: string | null;
};

const EMPTY: DiffsInventory = { files: [], truncated: false, settled: false, error: null };

const walkRows = new WeakMap<SourceWalkFile, DiffsFile>();

function walkRow(file: SourceWalkFile): DiffsFile {
  let row = walkRows.get(file);
  if (!row) {
    row = { kind: "walk", file };
    walkRows.set(file, row);
  }
  return row;
}

/** Resolves inventory from the active review lens. */
export function lensInventory(projectId: string): DiffsInventory {
  const record = resolvedScope(projectId);
  if (record.status === "empty") return EMPTY;
  return {
    files: record.files.slice(0, DIFFS_FILE_CAP).map(walkRow),
    truncated: record.files.length > DIFFS_FILE_CAP,
    settled: record.status === "ready" || record.settled,
    error: record.status === "error" ? record.error : null,
  };
}

export function turnInventory(loaded: TurnDiffs | undefined, error: string | null): DiffsInventory {
  if (!loaded) return { ...EMPTY, error };
  return { files: loaded.files.map(walkRow), truncated: loaded.truncated, settled: true, error };
}

export function gitInventory(loaded: GitDiffs | undefined, error: string | null): DiffsInventory {
  if (!loaded) return { ...EMPTY, error };
  return { files: loaded.files, truncated: loaded.truncated, settled: true, error };
}

/** Stable cache key for an inventory file. */
export function diffsFileKey(row: DiffsFile): string {
  return row.kind === "walk"
    ? row.file.file_id || `${row.file.root_id}\u0000${row.file.path}`
    : `${row.address.rootId}\u0000${row.file.path}`;
}

/** Number of recorded effects for a walk file. */
export function diffsFileWriteCount(row: DiffsFile): number {
  return row.kind === "walk" ? row.file.effects.length : 0;
}
