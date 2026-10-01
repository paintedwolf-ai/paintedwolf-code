import { createEffect, onCleanup, untrack } from "solid-js";
import { unwrap } from "solid-js/store";
import { useResidentLive } from "../../ui/resident-activity.ts";
import type { LycaonClient } from "../../api/client.ts";
import { canEditLoadedSource } from "../../components/source/editor/source-editor-model.ts";
import { editorReplica, evictEditorDocument, observeEditorDocument, resolveEditorDocument, type EditorDocumentState } from "./editor-document.ts";
import { FileDocumentOpeningController, type DocumentOpeningTarget } from "./file-document-opening.ts";
import { applyFilesBufferEditorDocument, setFilesBufferDocumentOpening } from "./project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "./files-buffer-state.ts";
import { workspaceMismatchId } from "../source/source-workspace-identity.ts";
import { WORKSPACE_IDENTITY_RECONCILE_LIMIT } from "../source/files-workspace-fault.ts";

function supportsDocument(buffer: FileBuffer): boolean {
  return buffer.content.state !== "suspended" && !buffer.loading && !buffer.loadError && !buffer.deleted && canEditLoadedSource({
    kind: buffer.kind, overLimit: buffer.overLimit, sha256: buffer.baseSha256,
    encoding: buffer.encoding, jobId: buffer.jobId, writable: buffer.writable,
  });
}

function identity(buffer: FileBuffer, workspaceId: string): string {
  return [workspaceId, buffer.rootId, buffer.path, buffer.fileId, buffer.encoding, buffer.baseSha256, buffer.editRevision].join("\0");
}

export function createFilesDocumentOpening(options: {
  projectId: string;
  client: () => LycaonClient | null;
  reachable?: () => boolean;
  workspaceId?: () => string;
  workspaceSettled?: () => boolean;
  refreshWorkspace?: (workspaceId: string) => boolean;
}): (key: string) => void {
  const { projectId } = options;
  const live = useResidentLive();
  const workspace = () => options.workspaceId?.() ?? "";
  const settled = () => options.workspaceSettled?.() ?? true;
  const admitted = new Set<string>();
  let workspaceRecoveries = 0;
  let disposed = false;
  const current = (target: DocumentOpeningTarget<FileBuffer>) => {
    const buffer = projectFilesState(projectId).byKey[target.key];
    return !disposed && live() && settled() && !!buffer && unwrap(buffer) === target.instance && supportsDocument(buffer) && identity(buffer, workspace()) === target.identity;
  };
  const controller = new FileDocumentOpeningController<FileBuffer, EditorDocumentState>({
    current,
    open: async (buffer, incarnation, background, wanted) => {
      const target = { key: buffer.key, identity: identity(buffer, workspace()),
        instance: unwrap(projectFilesState(projectId).byKey[buffer.key] ?? buffer), value: buffer };
      const previous = editorReplica(buffer.documentId);
      // Document acquisition precedes asynchronous release checks.
      let document: EditorDocumentState | null;
      try {
        document = await resolveEditorDocument(projectId, buffer, incarnation, undefined, background ? "background" : "foreground", wanted);
      } catch (error) {
        const replacementWorkspace = workspaceMismatchId(error);
        if (replacementWorkspace && current(target) && workspaceRecoveries < WORKSPACE_IDENTITY_RECONCILE_LIMIT) {
          workspaceRecoveries++;
          options.refreshWorkspace?.(replacementWorkspace);
        }
        throw error;
      }
      if (previous && buffer.editorOpening && editorReplica(buffer.documentId) === previous) {
        return await observeEditorDocument(projectId, buffer) ?? document;
      }
      return document;
    },
    state: (buffer, state) => setFilesBufferDocumentOpening(projectId, buffer.key, state),
    accept: (buffer, document) => {
      workspaceRecoveries = 0;
      admitted.add(document.documentId);
      applyFilesBufferEditorDocument(projectId, buffer.key, document);
    },
    release: async (document) => {
      const wanted = Object.values(projectFilesState(projectId).byKey).some((buffer) =>
        buffer.documentId === document.documentId || (workspace() === document.workspaceId &&
          !buffer.jobId && !buffer.deleted && buffer.path === document.path && buffer.rootId === document.rootId));
      if (!wanted) await evictEditorDocument(document.documentId);
    },
  });
  onCleanup(() => { disposed = true; controller.dispose(); });
  createEffect(() => {
    const client = options.client();
    const reachable = options.reachable?.() ?? client != null;
    const workspaceId = workspace();
    const state = projectFilesState(projectId);
    const targets: DocumentOpeningTarget<FileBuffer>[] = [];
    if (live() && settled()) {
      const selected = state.pendingKey ?? state.activeKey;
      const active = selected ? state.byKey[selected] : undefined;
      const selectedReady = active && !active.loading && !active.editorOpening &&
        (!supportsDocument(active) || !!editorReplica(active.documentId));
      const index = selected ? state.order.indexOf(selected) : -1;
      const nearby = selectedReady ? [state.order[index - 1], state.order[index + 1]] : [];
      const keys = new Set([selected, ...controller.runningKeys,
        ...nearby]);
      for (const key of keys) {
        if (!key) continue;
        const buffer = state.byKey[key];
        if (!buffer || !supportsDocument(buffer)) continue;
        if (buffer.documentId && admitted.has(buffer.documentId) && editorReplica(buffer.documentId) && !buffer.editorOpening) continue;
        targets.push({ key, identity: identity(buffer, workspaceId), instance: unwrap(buffer), value: { ...unwrap(buffer) }, background: key !== selected });
      }
    }
    untrack(() => controller.reconcile(client, reachable, targets));
  });
  return (key) => { workspaceRecoveries = 0; controller.retry(key); };
}
