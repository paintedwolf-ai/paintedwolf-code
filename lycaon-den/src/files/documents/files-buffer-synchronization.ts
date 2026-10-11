import { onCleanup } from "solid-js";
import { loadEditorConfigForBuffer, refreshOpenEditorConfigs } from "../../components/source/editor/editorconfig-load.ts";
import { createBufferRefresh } from "./files-buffer-refresh.ts";
import { setBufferLiveHandlers } from "./buffer-live.ts";
import { projectFilesState } from "./files-buffer-state.ts";
import { observeEditorDocument } from "./editor-document.ts";
import { flushFilesDraftSyncFor } from "./files-draft-sync.ts";
import { setFilesActiveBuffer } from "./project-files-buffers.ts";
import { applyProjectSourceLoadFailure } from "../components/project-files-load-error.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import type { createFileConfirmations } from "../commands/file-confirmations.ts";

type BufferSynchronization = Omit<Parameters<typeof createBufferRefresh>[0], "reloadEditorConfig" | "shouldApply"> &
  Pick<ReturnType<typeof createFileConfirmations>, "setBulkClose" | "setConfirm">;

export function bindFilesBufferSynchronization(options: BufferSynchronization): void {
  const { projectId, client, sourceSessionId, state, versionForBuffer,
    flashUpdatedNote, setBulkClose, setConfirm } = options;
  let stopped = false;
  let isCurrent = () => !stopped;
  const reloadEditorConfig = (key: FileBufferKey) => {
    const c = client();
    const buf = projectFilesState(projectId()).byKey[key];
    if (!c || !buf) return;
    void loadEditorConfigForBuffer({
      client: c,
      projectId: projectId(),
      key,
      buffer: buf,
      sessionId: sourceSessionId(),
    });
  };
  const refreshBufferPresentation = createBufferRefresh({ ...options, reloadEditorConfig, flashUpdatedNote, shouldApply: () => isCurrent() });

  const stop = setBufferLiveHandlers(projectId(), {
    onEditorConfigChange: (rootId) => {
      if (!isCurrent()) return;
      const c = client();
      if (!c) return;
      void refreshOpenEditorConfigs({
        client: c,
        projectId: projectId(),
        rootId,
        sessionId: sourceSessionId(),
      });
    },
    onCleanDiskChange: (buf) => {
      if (!isCurrent()) return;
      if (!buf.documentId || buf.kind !== "text") {
        refreshBufferPresentation(buf);
        return;
      }
      if (versionForBuffer(buf) || buf.loading) return;
      void observeEditorDocument(projectId(), buf).then((document) => {
        if (!isCurrent()) return;
        const current = projectFilesState(projectId()).byKey[buf.key];
        // A clean diverged document needs its file presentation refreshed.
        if (document?.diverged && current && !current.dirty) {
          refreshBufferPresentation(current);
        }
      }).catch((error) => {
        if (!isCurrent()) return;
        const current = projectFilesState(projectId()).byKey[buf.key];
        if (current && !current.dirty) applyProjectSourceLoadFailure(projectId(), buf.key, error);
      });
    },
    onDirtyDiskChange: (buf) => {
      if (!isCurrent()) return;
      flushFilesDraftSyncFor(projectId(), buf.key);
      void observeEditorDocument(projectId(), buf).then((document) => {
        if (!isCurrent()) return;
        if (!document?.diverged) return;
        if (state().activeKey !== buf.key) {
          setFilesActiveBuffer(projectId(), buf.key, "presentation");
        }
      }).catch(() => undefined);
    },
    onForeignDeleteDirty: (bufs, event) => {
      if (!isCurrent()) return;
      const path = event.path?.trim() || "";
      const rootId = event.root_id?.trim() || "";
      if (!path || !rootId) return;
      setBulkClose({
        keys: bufs.map((b) => b.key),
        dirtyKeys: bufs.filter((b) => b.dirty).map((b) => b.key),
        foreignDelete: { rootId, path },
      });
      setConfirm({
        intent: "bulk-close",
        fileNames: bufs.map((b) => b.name),
      });
    },
  });

  isCurrent = () => !stopped && stop.isCurrent();
  onCleanup(() => { stopped = true; stop.dispose(); });
}
