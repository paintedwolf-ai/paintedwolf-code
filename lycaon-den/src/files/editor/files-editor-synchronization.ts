import { createFileDocumentMetadata } from "../documents/files-document-metadata.ts";
import { onCleanup, onMount } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { configureEditorDocuments, evictEditorDocument, subscribeEditorDocuments } from "../documents/editor-document.ts";
import { dropFilesEditorHandlers } from "./files-editor-host.ts";
import { createFilesDocumentOpening } from "../documents/files-document-opening.ts";
import { setFilesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import { applyFilesBufferEditorDocument, markFilesBufferLoading } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

export function connectFilesEditorDocuments(projectId: string, client: () => LycaonClient | null,
  sessionId: () => string | undefined, workspaceId?: () => string): () => void {
  return configureEditorDocuments(projectId, client, sessionId, {
    get: (key) => projectFilesState(projectId).byKey[key],
    all: () => Object.values(projectFilesState(projectId).byKey),
  }, workspaceId);
}

export function createFilesEditorSynchronization(options: {
  projectId: string; client: () => LycaonClient | null; sessionId: () => string | undefined; workspaceId?: () => string; workspaceSettled?: () => boolean; reachable?: () => boolean; onUpdated: () => void;
  refreshWorkspace?: (workspaceId: string) => boolean;
}): (key: string) => void {
  const { projectId } = options;
  createFileDocumentMetadata(projectId, options.client, options.reachable);
  onCleanup(connectFilesEditorDocuments(projectId, options.client, options.sessionId, options.workspaceId));
  let retry: ((key: string) => void) | undefined;
  onMount(() => {
    retry = createFilesDocumentOpening(options);
    const stop = subscribeEditorDocuments((document) => {
      if (document.projectId !== projectId) return;
      if (options.workspaceSettled && !options.workspaceSettled()) return;
      const key = fileBufferKey(document.rootId, document.path, undefined, document.fileId);
      setFilesEditorSecretScreen(key, document.secretScreen ?? null);
      const buffer = projectFilesState(projectId).byKey[key];
      if (!buffer || buffer.content.state === "suspended" || buffer.deleted || buffer.editorOpening || buffer.documentId !== document.documentId) return;
      applyFilesBufferEditorDocument(projectId, key, document);
      options.onUpdated();
      // Without a draft, an absent document reloads as the deleted file's card.
      if (document.absent && !document.dirty && !document.heldAgentVersionId) {
        void evictEditorDocument(document.documentId).then(() => {
          const current = projectFilesState(projectId).byKey[key];
          if (!current || (current.documentId !== null && current.documentId !== document.documentId)) return;
          dropFilesEditorHandlers(projectId, key);
          markFilesBufferLoading(projectId, key);
        }).catch(() => undefined);
      }
    });
    onCleanup(stop);
  });
  return (key) => retry?.(key);
}
