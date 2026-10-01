import { createEffect, createSignal, onCleanup, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceReaderEndpoint } from "../../api/types.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { loadSourceComparison } from "../source/source-comparison-cache.ts";
import { comparisonEndpoints } from "../source/source-comparison.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { fileVersionBeforeDeletion, fileVersionFromComparison, type FileVersionView } from "./file-version.ts";

const committedFileFailure = {
  code: "files_committed_file_unavailable",
  title: "Committed file unavailable",
  suggestedAction: "Reopen the comparison, or check the project's connection and source access.",
};

/** How the removed contents come back. */
export type DeletionRestore =
  | { kind: "version"; versionId: string }
  | { kind: "commit" };

/** The change that removed a file, presented as a version whose before side holds the removed contents. */
export type Deletion = { version: FileVersionView; restore: DeletionRestore | null };

/**
 * The last deletion of a path. `unrecorded` means neither the ledger nor the
 * last commit holds the removed contents.
 */
export type LastDeletion =
  | { kind: "pending" }
  | { kind: "unrecorded" }
  | ({ kind: "deleted" } & Deletion);

/** The removed side restores only when its bytes were kept. */
function restorableRemovedSide(availability: SourceReaderEndpoint["availability"]): boolean {
  return availability === "available" || availability === "binary";
}

/** The ledger's retained deletion at this path, from the buffer's own read. */
export function retainedDeletion(buffer: Pick<FileBuffer, "deleted" | "fileId" | "rootId" | "path">): Deletion | null {
  const deleted = buffer.deleted;
  if (!deleted) return null;
  const previousId = deleted.previous.version_id?.trim() ?? "";
  return {
    version: fileVersionBeforeDeletion(buffer.fileId ?? "", buffer.rootId, buffer.path, deleted),
    restore: previousId && buffer.fileId && restorableRemovedSide(deleted.previous.availability)
      ? { kind: "version", versionId: previousId }
      : null,
  };
}

/**
 * Resolves the last deletion of an absent file: the ledger's retained deletion
 * first, else the removal since the last commit.
 */
export function createLastDeletion(args: {
  absent: Accessor<boolean>;
  buffer: Accessor<FileBuffer>;
  client: Accessor<LycaonClient | null | undefined>;
  projectId: Accessor<string>;
  sessionId: Accessor<string | undefined>;
  /** HEAD of the commit that still holds the file, when it was removed since then. */
  committedHead: Accessor<string | null>;
}): Accessor<LastDeletion> {
  const [committed, setCommitted] = createSignal<LastDeletion>({ kind: "pending" });
  createEffect(() => {
    const head = args.committedHead();
    const buffer = args.buffer();
    if (!args.absent() || buffer.deleted || head == null) return;
    const client = args.client(), projectId = args.projectId();
    const target = { path: buffer.path, rootId: buffer.rootId, baseline: "commit" as const, expectedHead: head };
    setCommitted({ kind: "pending" });
    if (!client) { setCommitted({ kind: "unrecorded" }); return; }
    let cancelled = false;
    onCleanup(() => { cancelled = true; });
    void loadSourceComparison(client, projectId, target, args.sessionId()).then((diff) => {
      if (cancelled) return;
      const endpoints = comparisonEndpoints(diff);
      const version = endpoints?.after.state === "absent"
        ? fileVersionFromComparison({ versionId: "", fileId: buffer.fileId ?? "", rootId: target.rootId, path: target.path, op: "delete", ts: "", initialComparison: "before" }, diff)
        : null;
      if (!version) {
        setCommitted({ kind: "unrecorded" });
        reportSurfaceFailure(committedFileFailure, "The file changed since the comparison was loaded.", projectId);
        return;
      }
      // The commit never held the file either: nothing was removed to show.
      if (version.beforeAvailability === "absent") { setCommitted({ kind: "unrecorded" }); return; }
      setCommitted({ kind: "deleted", version, restore: restorableRemovedSide(version.beforeAvailability) ? { kind: "commit" } : null });
    }, (err) => {
      if (cancelled) return;
      setCommitted({ kind: "unrecorded" });
      reportSurfaceFailure(committedFileFailure, err, projectId);
    });
  });
  return () => {
    if (!args.absent()) return { kind: "unrecorded" };
    const retained = retainedDeletion(args.buffer());
    if (retained) return { kind: "deleted", ...retained };
    return args.committedHead() == null ? { kind: "unrecorded" } : committed();
  };
}
