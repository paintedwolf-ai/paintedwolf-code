import { refreshFileDocumentMetadata } from "./files-document-metadata.ts";
/** Apply source changes to open buffers. */

import type { SourceChange, SourceWorkspaceKind } from "../../api/types.ts";
import { closeFilesBuffer } from "./project-files-buffers.ts";
import { filesBufferNeedsBody, projectFilesState, type FileBuffer } from "./files-buffer-state.ts";
import { retargetOpenFilesUnderPath } from "../commands/project-files-retarget.ts";

type BufferLiveHandlers = {
  /** Refresh a clean file buffer, including its presentation kind. */
  onCleanDiskChange: (buf: FileBuffer) => void;
  /** Reconcile a dirty buffer with disk. */
  onDirtyDiskChange: (buf: FileBuffer) => void;
  /** Confirm before closing dirty buffers deleted on disk. */
  onForeignDeleteDirty: (bufs: FileBuffer[], event: SourceChange) => void;
  /** Re-resolve open buffers when a root EditorConfig source changes. */
  onEditorConfigChange?: (rootId: string) => void;
};

const handlersByProject = new Map<string, BufferLiveHandlers>();
const pendingResync = new Set<string>();

export function setBufferLiveHandlers(
  projectId: string,
  handlers: BufferLiveHandlers | null,
): void {
  const id = projectId.trim();
  if (!id) return;
  if (!handlers) handlersByProject.delete(id);
  else {
    handlersByProject.set(id, handlers);
    if (pendingResync.delete(id)) applyBufferSourceResync(id);
  }
}

// Initial reads resolve loading buffers; settled buffers follow their own branch.
function reactsTo(
  buf: FileBuffer,
  workspaceKind: SourceWorkspaceKind,
  jobId: string,
): boolean {
  if (buf.loading) return false;
  if (workspaceKind === "worker") {
    if (!jobId) return false;
    return (buf.jobId ?? "") === jobId;
  }
  return !buf.jobId;
}

function underPath(
  projectId: string,
  rootId: string,
  path: string,
  workspaceKind: SourceWorkspaceKind,
  jobId: string,
): FileBuffer[] {
  const state = projectFilesState(projectId);
  const out: FileBuffer[] = [];
  for (const key of state.order) {
    const buf = state.byKey[key];
    if (!buf || buf.rootId !== rootId || !reactsTo(buf, workspaceKind, jobId)) continue;
    if (path === "." || buf.path === path || buf.path.startsWith(`${path}/`)) {
      out.push(buf);
    }
  }
  return out;
}

/** Event gaps reconcile cold metadata and refresh admitted document bodies. */
export function applyBufferSourceResync(projectId: string): void {
  void refreshFileDocumentMetadata(projectId).catch(() => undefined);
  const handlers = handlersByProject.get(projectId);
  const state = projectFilesState(projectId);
  if (!handlers) {
    if (state.order.some((key) => {
      const buf = state.byKey[key];
      return buf && reactsTo(buf, "project", "");
    })) pendingResync.add(projectId);
    return;
  }
  const roots = new Set<string>();
  for (const key of state.order) {
    const buf = state.byKey[key];
    if (!buf || !reactsTo(buf, "project", "")) continue;
    roots.add(buf.rootId);
    if (filesBufferNeedsBody(buf)) continue;
    if (buf.dirty) handlers.onDirtyDiskChange(buf);
    else handlers.onCleanDiskChange(buf);
  }
  for (const rootId of roots) handlers.onEditorConfigChange?.(rootId);
}

/** Applies a source change to matching open buffers. */
export function applyBufferSourceChanged(
  projectId: string,
  workspaceKind: SourceWorkspaceKind,
  event: SourceChange,
): void {
  const pid = projectId.trim();
  if (!pid) return;
  const rootId = event.root_id?.trim() || "";
  if (!rootId) return;
  const jobId = event.worker_id?.trim() || "";
  const handlers = handlersByProject.get(pid);
  if (
    workspaceKind === "project" &&
    [event.path, event.from_path].some((path) =>
      path === ".editorconfig" || path?.endsWith("/.editorconfig")
    )
  ) {
    handlers?.onEditorConfigChange?.(rootId);
  }

  // Worker buffers expose content updates only.
  if (workspaceKind === "worker" && event.op !== "write" && event.op !== "create") return;

  if (event.op === "rename" && event.from_path) {
    const from = event.from_path.trim();
    const to = event.path?.trim() || "";
    if (from && to) {
      if (workspaceKind === "project") {
        retargetOpenFilesUnderPath(pid, rootId, from, to);
      }
    }
    return;
  }

  if (event.op === "delete") {
    const path = event.path?.trim() || "";
    if (!path) return;
    const bufs = underPath(pid, rootId, path, workspaceKind, jobId);
    const dirty = bufs.filter((b) => b.dirty);
    if (dirty.length > 0 && handlers) {
      handlers.onForeignDeleteDirty(dirty, event);
      return;
    }
    for (const buf of bufs) void closeFilesBuffer(pid, buf.key);
    return;
  }

  if (event.op !== "write" && event.op !== "create") return;
  const path = event.path?.trim() || "";
  if (!path) return;

  for (const buf of underPath(pid, rootId, path, workspaceKind, jobId)) {
    if (filesBufferNeedsBody(buf)) continue;
    if (buf.dirty) {
      handlers?.onDirtyDiskChange(buf);
    } else {
      handlers?.onCleanDiskChange(buf);
    }
  }
}
