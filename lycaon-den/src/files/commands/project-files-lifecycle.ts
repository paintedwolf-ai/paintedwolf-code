import { SourceOperationStoppedError } from "../../api/source-operation.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { ProjectSourceLifecycleResponse } from "../../api/types.ts";
import type { SourceChange } from "../../api/types.ts";
import {
  closeFilesBuffersUnderPath,
} from "../documents/project-files-buffers.ts";
import { childTreePath, fileDisplayName } from "../components/project-files-model.ts";
import { retargetOpenFilesUnderPath } from "./project-files-retarget.ts";

export type LifecycleClient = Pick<
  LycaonClient,
  "renameProjectSource" | "copyProjectSource" | "deleteProjectSource" | "retrySourceOperation"
>;

export type DirtyGuardResult = "proceed" | "cancel";

export type LifecycleHooks = {
  /** Resolves dirty buffers before deletion. */
  guardDirtyUnderPath: (
    rootId: string,
    path: string,
  ) => Promise<DirtyGuardResult>;
  /** Applies host-confirmed changes to the shared tree. */
  confirmChange: (change: SourceChange) => void;
  openFile?: (args: {
    rootId: string;
    rootLabel: string;
    path: string;
  }) => void;
  rootLabelFor: (rootId: string) => string;
};

function parentOf(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "." : path.slice(0, i) || ".";
}

export function nextDuplicatePath(
  path: string,
  occupied: ReadonlySet<string>,
): string {
  const name = fileDisplayName(path);
  const dir = parentOf(path);
  const dot = name.includes(".") ? name.lastIndexOf(".") : -1;
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : "";
  let candidate = childTreePath(dir, `${stem} copy${ext}`);
  if (!occupied.has(candidate)) return candidate;
  for (let n = 2; ; n++) {
    candidate = childTreePath(dir, `${stem} copy ${n}${ext}`);
    if (!occupied.has(candidate)) return candidate;
  }
}

export async function renameSourcePath(
  client: LifecycleClient,
  projectId: string,
  args: { rootId: string; from: string; to: string; isDir: boolean; operationId?: string; retry?: boolean },
  hooks: LifecycleHooks,
  sessionId?: string,
): Promise<ProjectSourceLifecycleResponse> {
  const operationId = args.operationId ?? crypto.randomUUID();
  const res = args.retry
    ? await client.retrySourceOperation(projectId, operationId) as ProjectSourceLifecycleResponse
    : await client.renameProjectSource(projectId, {
    operation_id: operationId,
    root_id: args.rootId,
    from: args.from,
    to: args.to,
  }, sessionId);
  retargetOpenFilesUnderPath(projectId, args.rootId, args.from, res.path);
  hooks.confirmChange({
    root_id: res.root_id,
    path: res.path,
    from_path: args.from,
    op: "rename",
    origin: "user",
    is_dir: args.isDir,
    changed_at: new Date().toISOString(),
  });
  return res;
}

export async function copySourcePath(
  client: LifecycleClient,
  projectId: string,
  args: { rootId: string; from: string; to: string; isDir: boolean },
  hooks: LifecycleHooks,
  sessionId?: string,
): Promise<ProjectSourceLifecycleResponse> {
  const res = await client.copyProjectSource(projectId, {
    operation_id: crypto.randomUUID(),
    root_id: args.rootId,
    from: args.from,
    to: args.to,
  }, sessionId);
  hooks.confirmChange({
    root_id: res.root_id,
    path: res.path,
    op: "create",
    origin: "user",
    is_dir: args.isDir,
    changed_at: new Date().toISOString(),
  });
  if (!args.isDir) {
    hooks.openFile?.({
      rootId: res.root_id,
      rootLabel: hooks.rootLabelFor(res.root_id),
      path: res.path,
    });
  }
  return res;
}

export type DeleteSourceResult =
  | { ok: true }
  | { ok: false; cancelled: true }
  | { ok: false; code: string; message: string; retryable: boolean; suggestedAction?: string };

export async function deleteSourcePath(
  client: LifecycleClient,
  projectId: string,
  args: {
    rootId: string;
    path: string;
    recursive: boolean;
    isDir: boolean;
    operationId: string;
    retry?: boolean;
  },
  hooks: LifecycleHooks,
  sessionId?: string,
): Promise<DeleteSourceResult> {
  try {
    const guard = await hooks.guardDirtyUnderPath(args.rootId, args.path);
    if (guard === "cancel") return { ok: false, cancelled: true };
    if (args.retry) await client.retrySourceOperation(projectId, args.operationId);
    else await client.deleteProjectSource(projectId, args.path, {
      operationId: args.operationId,
      rootId: args.rootId,
      recursive: args.recursive,
      sessionId,
    });
  } catch (err) {
    if (err instanceof SourceOperationStoppedError && err.state === "canceled") return { ok: false, cancelled: true };
    if (err instanceof LycaonApiError) {
      return {
        ok: false,
        code: err.code ?? "internal_error",
        retryable: err.retryable === true,
        suggestedAction: err.suggestedAction,
        message: err.message.trim() || "Could not move to trash.",
      };
    }
    return {
      ok: false,
      code: "internal_error",
      retryable: true,
      message: err instanceof Error ? err.message : "Could not move to trash.",
    };
  }
  closeFilesBuffersUnderPath(projectId, args.rootId, args.path);
  hooks.confirmChange({
    root_id: args.rootId,
    path: args.path,
    op: "delete",
    origin: "user",
    is_dir: args.isDir,
    changed_at: new Date().toISOString(),
  });
  return { ok: true };
}

export function trashConfirmBody(name: string, isDir: boolean): string {
  return `${isDir ? `"${name}" and its contents` : `"${name}"`} will move to the Trash. You can undo this change in file history.`;
}

export function sourceLifecycleErrorMessage(err: unknown): string {
  if (err instanceof LycaonApiError) {
    if (err.code === "source_already_exists") {
      return "A file or folder with that name already exists.";
    }
    if (err.message.trim()) return err.message.trim();
  }
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  return "Could not complete that change.";
}
