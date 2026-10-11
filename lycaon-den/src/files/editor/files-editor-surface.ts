import { createEffect, onCleanup } from "solid-js";
import { clearScopePreview } from "../../components/source/editor/codemirror-theme.ts";
import { inlineEditOpen } from "../../components/source/inline-edit/inline-edit-controller.ts";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { filesBufferKeyAtAddress } from "../documents/project-files-buffers.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { getFilesEditorView } from "./files-editor-host.ts";
import type { FilesScope } from "../components/files-scope.ts";
import type { ProjectFilesViewProps, ProjectFileDocumentSession } from "../components/project-files-view-props.ts";
import type { createFileDocumentCommands } from "../documents/file-document-commands.ts";

type EditorSurface = Pick<FilesScope, "activeBuffer" | "filesRoots"> & {
  projectId: string;
  editorPaneEl: () => HTMLDivElement | undefined;
  document: () => ProjectFilesViewProps["document"];
  performSave: ReturnType<typeof createFileDocumentCommands>["performSave"];
};

export function bindFilesEditorSurface(options: EditorSurface): void {
  const { projectId, activeBuffer, filesRoots, editorPaneEl, performSave } = options;
  createEffect((wasOpen?: boolean) => {
    const isOpen = inlineEditOpen() != null;
    if (wasOpen && !isOpen) {
      const buf = activeBuffer();
      if (buf && !isComposedBufferKind(buf.kind)) {
        const view = getFilesEditorView(projectId, buf.key);
        if (view) clearScopePreview(view);
      }
    }
    return isOpen;
  });

  bindFindableView({
    id: "files-editor",
    provider: "codemirror",
    root: () => editorPaneEl(),
    getEditorView: () => {
      const key = projectFilesState(projectId).activeKey;
      if (!key) return null;
      return getFilesEditorView(projectId, key);
    },
    enabled: () => {
      const buf = activeBuffer();
      return (
        buf != null && !isComposedBufferKind(buf.kind) && !buf.loading && !buf.loadError
      );
    },
  });

  createEffect(() => {
    const document = options.document();
    const path = document?.path.trim();
    if (!document || !path) return;
    const root = filesRoots().find((entry) => entry.id === document.rootId);
    if (!root) return;
    const session: ProjectFileDocumentSession = {
      save: async () => {
        const key = filesBufferKeyAtAddress(projectId, root.id, path);
        if (!key) return false;
        flushFilesDraftSyncFor(projectId, key);
        const buffer = projectFilesState(projectId).byKey[key];
        if (!buffer) return false;
        return buffer.dirty ? performSave(key) : true;
      },
    };
    document.onSessionChange?.(session);
    onCleanup(() => document.onSessionChange?.(null));
  });

}
