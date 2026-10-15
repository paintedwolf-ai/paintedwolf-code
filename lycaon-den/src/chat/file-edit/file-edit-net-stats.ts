/**
 * Net line counts for folds whose net change does not carry them. A group reads
 * them from here, so its summary is the same whether or not its rows are mounted.
 */

import { createEffect, createSignal, onCleanup, type Accessor } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { fileEditFoldFingerprint, fileEditLineStat, type FileEditFold, type FileEditLineStat } from "./file-edit-fold.ts";
import { fileEditReader } from "./file-edit-reader.ts";

/** A fold of several writes measures its net change; a single write's row measures it once opened. */
export function foldNeedsMeasuredNet(fold: FileEditFold): boolean {
  return fileEditLineStat(fold.net) == null && fold.steps.length > 1;
}

export function createFoldNetStats(
  folds: Accessor<readonly FileEditFold[]>,
  projectId: Accessor<string | undefined>,
  sessionId: Accessor<string | null | undefined>,
): { stats: Accessor<ReadonlyMap<string, FileEditLineStat>>; error: Accessor<unknown> } {
  const [stats, setStats] = createSignal<ReadonlyMap<string, FileEditLineStat>>(new Map());
  const [error, setError] = createSignal<unknown>();
  const requested = new Set<string>();
  let disposed = false;
  onCleanup(() => { disposed = true; });
  createEffect(() => {
    const client = getLycaonClient();
    const project = projectId();
    const session = sessionId();
    if (!client || !project || !session) return;
    for (const fold of folds()) {
      const key = fileEditFoldFingerprint(fold);
      if (!foldNeedsMeasuredNet(fold) || requested.has(key)) continue;
      requested.add(key);
      void fileEditReader(client, project, session, fold).summary().then(
        (stat) => { if (!disposed) setStats((previous) => new Map(previous).set(key, stat)); },
        (cause) => { if (!disposed) setError(cause); },
      );
    }
  });
  return { stats, error };
}
