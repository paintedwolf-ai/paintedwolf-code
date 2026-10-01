import { filesBufferBase } from "../documents/project-files-buffers.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceFileVersion, SourceTip } from "../../api/types.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import type { FileHistoryCommit } from "./file-history-model.ts";
import {
  versionAfterSide,
  type FileVersionHistory,
  type FileVersionView,
} from "./file-version.ts";
import {
  type FileMutationOutcome,
  type FileUndo,
  restoreCommitState,
  restoreFileVersion,
} from "../commands/file-mutations.ts";
import { fileBasename } from "../review/review-model.ts";
import { sourceContentNotice } from "../source/source-content-availability.ts";

function fileVersionIsRestorable(version: FileVersionView): boolean {
  switch (version.availability) {
    case "available":
    case "absent":
    case "binary":
      return true;
    case "directory":
    case "unavailable":
    case "not_captured":
    case "unresolved":
      return false;
  }
}

function currentTipForVersionRestore(
  buffer: Pick<FileBuffer, "baseSha256" | "deleted">,
  history: FileVersionHistory,
): SourceTip | null {
  const sha256 = buffer.baseSha256?.trim() ?? "";
  if (sha256) return { state: "content", sha256 };
  if (buffer.deleted || history.current?.state === "absent") return { state: "absent" };
  return null;
}

/** A file tab's own past version offers Restore; a comparison preview tab never does. */
export function versionRestoreOffered(
  buffer: Pick<FileBuffer, "kind" | "diffPreview">,
  version: FileVersionView | null | undefined,
): boolean {
  return version != null && !(buffer.kind === "diff" && buffer.diffPreview != null);
}

export function versionRestoreDisabledReason(args: {
  buffer: FileBuffer;
  history: FileVersionHistory;
  version: FileVersionView;
  restoring: boolean;
}): string | null {
  if (args.restoring) return "Restoring this version…";
  if (args.buffer.dirty) {
    return "Save or discard the current draft before restoring a version.";
  }
  if (!fileVersionIsRestorable(args.version)) {
    return sourceContentNotice(versionAfterSide(args.version))?.message ??
      "Exact content for this version is unavailable.";
  }
  const current = currentTipForVersionRestore(args.buffer, args.history);
  if (!current || !args.buffer.fileId) {
    return "Reload the working file before restoring a version.";
  }
  if (current.state === "absent" && args.version.availability === "absent") {
    return "This version already matches the working file.";
  }
  if (
    current.state === "content" &&
    args.version.availability !== "absent" &&
    args.version.sha256 != null &&
    current.sha256 === args.version.sha256
  ) {
    return "This version already matches the working file.";
  }
  return null;
}

/** Returns why a listed state cannot be restored. */
export function listedRestoreDisabledReason(args: {
  buffer: FileBuffer;
  history: FileVersionHistory;
  restoring: boolean;
  row:
    | { kind: "version"; version: SourceFileVersion }
    | { kind: "commit"; entry: FileHistoryCommit };
}): string | null {
  if (args.restoring) return "Restoring…";
  if (args.buffer.dirty) {
    return "Save or discard the current draft before restoring.";
  }
  const current = currentTipForVersionRestore(args.buffer, args.history);
  if (!current || !args.buffer.fileId) {
    return "Reload the working file before restoring.";
  }
  if (args.row.kind === "commit") {
    if (!args.row.entry.commit.blob_oid) {
      return "This commit removed the file; restore the state before it instead.";
    }
    return null;
  }
  const version = args.row.version;
  if (version.state !== "content" && version.state !== "absent") {
    return "Exact content for this version is unavailable.";
  }
  if (
    version.state === "content" && version.capture_state !== "stored" &&
    !version.git_change
  ) {
    return "Exact content for this version is unavailable.";
  }
  if (current.state === "absent" && version.state === "absent") {
    return "This version already matches the working file.";
  }
  if (
    current.state === "content" && version.state === "content" &&
    version.content_sha256 !== "" && current.sha256 === version.content_sha256
  ) {
    return "This version already matches the working file.";
  }
  return null;
}

/** Restores a retained version by id: a listed row, the selected version, or the state a deletion removed. */
export async function restoreRetainedVersion(args: {
  client: LycaonClient;
  projectId: string;
  sessionId?: string;
  buffer: FileBuffer;
  history: FileVersionHistory;
  versionId: string;
}): Promise<FileMutationOutcome> {
  const base = currentTipForVersionRestore(args.buffer, args.history);
  if (!base || !args.buffer.fileId) {
    return {
      ok: false,
      conflict: false,
      error: "Reload the working file before restoring.",
    };
  }
  const result = await restoreFileVersion(
    args.client,
    args.projectId,
    {
      versionId: args.versionId,
      fileId: args.buffer.fileId,
      rootId: args.buffer.rootId,
      path: args.buffer.path,
      base,
    },
    args.sessionId,
  );
  if (!result.ok) return result;
  const response = result.response;
  const restored: SourceTip = response.state === "content"
    ? { state: "content", sha256: response.sha256 }
    : { state: "absent" };
  const undo: FileUndo = response.previous_version_id
    ? {
        kind: "version",
        versionId: response.previous_version_id,
        fileId: response.file_id,
        rootId: response.root_id,
        path: response.path,
        base: restored,
      }
    : restoreUndoWithoutVersion(args.buffer, base, restored);
  return {
    ok: true,
    undo,
    label: `Restored ${fileBasename(response.path)} from version history`,
  };
}

/** Restores a listed commit from verified object bytes. */
export async function restoreListedCommit(args: {
  client: LycaonClient;
  projectId: string;
  sessionId?: string;
  buffer: FileBuffer;
  history: FileVersionHistory;
  entry: FileHistoryCommit;
}): Promise<FileMutationOutcome> {
  const base = currentTipForVersionRestore(args.buffer, args.history);
  const blobOid = args.entry.commit.blob_oid ?? "";
  if (!base || !args.buffer.fileId || !blobOid) {
    return {
      ok: false,
      conflict: false,
      error: "Reload the working file before restoring.",
    };
  }
  const result = await restoreCommitState(
    args.client,
    args.projectId,
    {
      commit: args.entry.commit.commit,
      fileId: args.buffer.fileId,
      rootId: args.buffer.rootId,
      path: args.buffer.path,
      sourcePath: args.entry.commit.source_path,
      blobOid,
      base,
    },
    args.sessionId,
  );
  if (!result.ok) return result;
  const response = result.response;
  const restored: SourceTip = response.state === "content"
    ? { state: "content", sha256: response.sha256 }
    : { state: "absent" };
  const undo: FileUndo = response.previous_version_id
    ? {
        kind: "version",
        versionId: response.previous_version_id,
        fileId: response.file_id,
        rootId: response.root_id,
        path: response.path,
        base: restored,
      }
    : restoreUndoWithoutVersion(args.buffer, base, restored);
  return {
    ok: true,
    undo,
    label: `Restored ${fileBasename(response.path)} from commit ${response.commit.slice(0, 7)}`,
  };
}

function restoreUndoWithoutVersion(
  buffer: FileBuffer,
  base: SourceTip,
  restored: SourceTip,
): FileUndo {
  if (base.state === "absent") {
    return { kind: "delete", rootId: buffer.rootId, path: buffer.path };
  }
  if (restored.state === "absent") {
    return {
      kind: "create",
      rootId: buffer.rootId,
      path: buffer.path,
      content: filesBufferBase(buffer),
      encoding: buffer.encoding ?? "utf-8",
    };
  }
  return {
    kind: "put",
    rootId: buffer.rootId,
    path: buffer.path,
    content: filesBufferBase(buffer),
    encoding: buffer.encoding ?? "utf-8",
    baseSha256: restored.sha256 ?? "",
  };
}
