import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode } from "../../api/http.ts";
import type {
  SourceCommitRestoreResponse,
  SourceEncoding,
  SourceTip,
  SourceVersionRestoreResponse,
} from "../../api/types.ts";
import { fileBasename } from "../review/review-model.ts";

/** Refusals that mean the file changed or left since the caller read it. */
const FILE_CHANGED_CODES = [
  "source_mutation_diverged",
  "source_write_conflict",
  "source_version_changed",
  "source_history_changed",
  "source_workspace_mismatch",
  "source_path_busy",
  "source_already_exists",
  "source_not_empty",
  "editor_revision_conflict",
  "source_not_found",
  "source_file_not_found",
  "source_version_not_found",
  "source_history_not_found",
  "path_not_found",
] as const;

export type FileVersionTarget = {
  versionId: string;
  fileId: string;
  rootId: string;
  path: string;
  base: SourceTip;
};

export type FileUndo =
  | ({ kind: "version" } & FileVersionTarget)
  | {
      kind: "put";
      rootId: string;
      path: string;
      content: string;
      encoding: SourceEncoding;
      baseSha256: string;
    }
  | {
      kind: "create";
      rootId: string;
      path: string;
      content: string;
      encoding: SourceEncoding;
    }
  | {
      kind: "delete";
      rootId: string;
      path: string;
    };

type FileMutationFailure =
  | { ok: false; conflict: true }
  | { ok: false; conflict: false; error: string };

export type FileMutationOutcome =
  | { ok: true; undo: FileUndo; label: string }
  | FileMutationFailure;

type FilePutResult =
  | { ok: true; sha256: string }
  | FileMutationFailure;

export async function writeFileContent(
  client: LycaonClient,
  projectId: string,
  args: {
    rootId: string;
    path: string;
    content: string;
    encoding: SourceEncoding;
    baseSha256: string;
  },
  sessionId?: string,
): Promise<FilePutResult> {
  try {
    const res = await client.replaceProjectSource(
      projectId,
      {
        operation_id: crypto.randomUUID(),
        path: args.path,
        root_id: args.rootId,
        content: args.content,
        encoding: args.encoding,
        base_sha256: args.baseSha256,
      },
      sessionId,
    );
    return { ok: true, sha256: res.sha256 };
  } catch (err) {
    return fileMutationError(err, "File update failed");
  }
}

export type FileBatchReport = {
  ok: number;
  skipped: Array<{ path: string; reason: string }>;
  undos: FileUndo[];
};

export type FilePut = {
  rootId: string;
  path: string;
  content: string;
  encoding: SourceEncoding;
  baseSha256: string;
  undoContent: string;
};

export async function applyFilePutBatch(
  client: LycaonClient,
  projectId: string,
  members: FilePut[],
  onProgress?: (done: number, total: number) => void,
  sessionId?: string,
): Promise<FileBatchReport> {
  const report: FileBatchReport = { ok: 0, skipped: [], undos: [] };
  const total = members.length;
  let done = 0;
  for (const m of members) {
    const res = await writeFileContent(client, projectId, m, sessionId);
    done++;
    onProgress?.(done, total);
    if (res.ok) {
      report.ok++;
      report.undos.push({
        kind: "put",
        rootId: m.rootId,
        path: m.path,
        content: m.undoContent,
        encoding: m.encoding,
        baseSha256: res.sha256,
      });
    } else if (res.conflict) {
      report.skipped.push({ path: m.path, reason: "changed since" });
    } else {
      report.skipped.push({ path: m.path, reason: res.error });
    }
  }
  return report;
}

export function fileMutationError(
  err: unknown,
  fallback: string,
): FileMutationFailure {
  if (isApiErrorCode(err, FILE_CHANGED_CODES)) return { ok: false, conflict: true };
  return {
    ok: false,
    conflict: false,
    error: err instanceof Error ? err.message : fallback,
  };
}

type FileVersionRestoreResult =
  | { ok: true; response: SourceVersionRestoreResponse }
  | FileMutationFailure;

export async function restoreFileVersion(
  client: LycaonClient,
  projectId: string,
  target: FileVersionTarget,
  sessionId?: string,
): Promise<FileVersionRestoreResult> {
  try {
    const response = await client.restoreProjectSourceVersion(
      projectId,
      target.versionId,
      {
        operation_id: crypto.randomUUID(),
        file_id: target.fileId,
        root_id: target.rootId,
        path: target.path,
        base: target.base,
      },
      sessionId,
    );
    return { ok: true, response };
  } catch (err) {
    return fileMutationError(err, "Could not restore this version.");
  }
}

export type CommitRestoreTarget = {
  commit: string;
  fileId: string;
  rootId: string;
  path: string;
  sourcePath: string;
  blobOid: string;
  base: SourceTip;
};

export type CommitRestoreResult =
  | { ok: true; response: SourceCommitRestoreResponse }
  | FileMutationFailure;

/** Restores the file's bytes as one commit holds them, from the repository
 * object store, verified by the host before anything is written. */
export async function restoreCommitState(
  client: LycaonClient,
  projectId: string,
  target: CommitRestoreTarget,
  sessionId?: string,
): Promise<CommitRestoreResult> {
  try {
    const response = await client.restoreProjectSourceCommitState(
      projectId,
      target.commit,
      {
        operation_id: crypto.randomUUID(),
        file_id: target.fileId,
        root_id: target.rootId,
        path: target.path,
        source_path: target.sourcePath,
        blob_oid: target.blobOid,
        base: target.base,
      },
      sessionId,
    );
    return { ok: true, response };
  } catch (err) {
    return fileMutationError(err, "Could not restore this commit's bytes.");
  }
}

async function applyCreate(
  client: LycaonClient,
  projectId: string,
  member: Extract<FileUndo, { kind: "create" }>,
  sessionId?: string,
  label?: string,
): Promise<FileMutationOutcome> {
  const name = fileBasename(member.path);
  try {
    await client.createProjectSourceEntry(
      projectId,
      {
        operation_id: crypto.randomUUID(),
        path: member.path,
        kind: "file",
        root_id: member.rootId || undefined,
      },
      sessionId,
    );
  } catch (err) {
    return fileMutationError(err, "Could not recreate the file.");
  }
  let live;
  try {
    live = await client.getProjectSource(projectId, member.path, {
      rootId: member.rootId || undefined,
      sessionId,
    });
  } catch (err) {
    await deleteQuietly(client, projectId, member, sessionId);
    return fileMutationError(err, "Could not read the recreated file.");
  }
  const baseSha = live.sha256?.trim() || "";
  const encoding = live.encoding ?? member.encoding;
  if (!baseSha) {
    await deleteQuietly(client, projectId, member, sessionId);
    return { ok: false, conflict: false, error: "Could not write the recreated file." };
  }
  const put = await writeFileContent(
    client,
    projectId,
    {
      rootId: member.rootId,
      path: member.path,
      content: member.content,
      encoding,
      baseSha256: baseSha,
    },
    sessionId,
  );
  if (!put.ok) {
    await deleteQuietly(client, projectId, member, sessionId);
    return put;
  }
  return {
    ok: true,
    undo: { kind: "delete", rootId: member.rootId, path: member.path },
    label: label ?? `Created ${name}`,
  };
}

async function deleteQuietly(
  client: LycaonClient,
  projectId: string,
  member: { rootId: string; path: string },
  sessionId?: string,
): Promise<void> {
  try {
    await client.deleteProjectSource(projectId, member.path, {
      operationId: crypto.randomUUID(),
      rootId: member.rootId || undefined,
      sessionId,
    });
  } catch {
    // Rollback may fail after the create succeeds.
  }
}

async function applyDelete(
  client: LycaonClient,
  projectId: string,
  member: Extract<FileUndo, { kind: "delete" }>,
  undoCreate: Extract<FileUndo, { kind: "create" }> | null,
  sessionId?: string,
  label?: string,
): Promise<FileMutationOutcome> {
  const name = fileBasename(member.path);
  let nextUndo = undoCreate;
  if (!nextUndo) {
    try {
      const live = await client.getProjectSource(projectId, member.path, {
        rootId: member.rootId || undefined,
        sessionId,
      });
      nextUndo = {
        kind: "create",
        rootId: member.rootId,
        path: member.path,
        content: live.content ?? "",
        encoding: live.encoding ?? "utf-8",
      };
    } catch (err) {
      return fileMutationError(err, "Could not read the file.");
    }
  }
  try {
    await client.deleteProjectSource(projectId, member.path, {
      operationId: crypto.randomUUID(),
      rootId: member.rootId || undefined,
      sessionId,
    });
  } catch (err) {
    return fileMutationError(err, "Could not delete the file.");
  }
  return {
    ok: true,
    undo: nextUndo,
    label: label ?? `Deleted ${name}`,
  };
}

async function applyPut(
  client: LycaonClient,
  projectId: string,
  member: Extract<FileUndo, { kind: "put" }>,
  undoContent: string,
  label: string,
  sessionId?: string,
): Promise<FileMutationOutcome> {
  const put = await writeFileContent(client, projectId, member, sessionId);
  if (!put.ok) return put;
  return {
    ok: true,
    undo: {
      kind: "put",
      rootId: member.rootId,
      path: member.path,
      content: undoContent,
      encoding: member.encoding,
      baseSha256: put.sha256,
    },
    label,
  };
}

export async function applyFileUndos(
  client: LycaonClient,
  projectId: string,
  members: readonly FileUndo[],
  sessionId?: string,
): Promise<FileMutationOutcome> {
  if (members.length === 0) {
    return { ok: false, conflict: false, error: "Nothing to undo." };
  }
  let last: FileMutationOutcome | null = null;
  for (const member of members) {
    last = await applyFileUndo(client, projectId, member, sessionId);
    if (!last.ok) return last;
  }
  return last!;
}

export async function applyFileUndo(
  client: LycaonClient,
  projectId: string,
  member: FileUndo,
  sessionId?: string,
  opts?: {
    undoCreate?: Extract<FileUndo, { kind: "create" }>;
    undoContent?: string;
    label?: string;
  },
): Promise<FileMutationOutcome> {
  const name = fileBasename(member.path);
  if (member.kind === "version") {
    const restored = await restoreFileVersion(
      client,
      projectId,
      member,
      sessionId,
    );
    if (!restored.ok) return restored;
    const response = restored.response;
    return {
      ok: true,
      undo: response.previous_version_id
        ? {
            kind: "version",
            versionId: response.previous_version_id,
            fileId: response.file_id,
            rootId: response.root_id,
            path: response.path,
            base: response.state === "content"
              ? { state: "content", sha256: response.sha256 }
              : { state: "absent" },
          }
        : member,
      label: `Restored ${name}`,
    };
  }
  if (member.kind === "create") {
    return applyCreate(client, projectId, member, sessionId, opts?.label);
  }
  if (member.kind === "delete") {
    return applyDelete(
      client,
      projectId,
      member,
      opts?.undoCreate ?? null,
      sessionId,
      opts?.label,
    );
  }
  return applyPut(
    client,
    projectId,
    member,
    opts?.undoContent ?? member.content,
    opts?.label ?? `Updated ${name}`,
    sessionId,
  );
}
