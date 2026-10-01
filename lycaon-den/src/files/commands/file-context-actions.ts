import type { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { fileOpenTarget } from "../source/file-open-target.ts";
import type { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import type { createFileMutationCommands } from "../commands/file-mutation-commands.ts";
import type { createFilesTreeReveal } from "../tree/files-tree-reveal.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { EditorView } from "@codemirror/view";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { editorSymbolTarget, type EditorSymbolTarget } from "../editor/editor-symbol-target.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { type FilesTreeSelection } from "../tree/files-tree-context.ts";
import { promoteFilesBuffer, toggleFilesBufferPinned } from "../documents/project-files-buffers.ts";
import type { BufferOpenIntent } from "../documents/files-buffer-state.ts";
import { filesContextMenuItems, resolveFilesContextTarget, type FilesContextMenuActions } from "../commands/project-files-context-menu.ts";
import type { NewEntryKind } from "../commands/project-files-create.ts";
import { trashConfirmBody } from "../commands/project-files-lifecycle.ts";
import { absolutePathForBuffer, fileDisplayName } from "../components/project-files-model.ts";
import { setFilesStagePaneMode } from "../review/review-pane.ts";
import { type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";
import { emphasizeSymbolRangeOnly } from "../../components/source/editor/codemirror-theme.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import type { FilesScope } from "../components/files-scope.ts";

type FileContextDependencies = Pick<FilesScope, "projectId" | "sourceSessionId" | "state" | "activeBuffer" |
  "filesRoots" | "copyAbsolutePathRef" | "copyRelativePathRef"> & Pick<ReturnType<typeof createFileMutationCommands>,
  "setMoveError" | "setMoveSource" | "duplicateEntry" | "setTrashConfirm"> & {
  resolveRootId: (rootId: string) => string | undefined;
  startTreeCreate: () => ((kind: NewEntryKind, rootId: string, dir: string) => void) | undefined;
  setRenaming: (target: { rootId: string; path: string; isDir: boolean }) => void;
  addPathToChat: FilesContextMenuActions["addToChat"];
  explainFileInReview: NonNullable<FilesContextMenuActions["explainFile"]>;
  collapseTreeAll: () => FilesContextMenuActions["collapseAllFolders"] | undefined;
  expandTreeAll: () => FilesContextMenuActions["expandAllFolders"] | undefined;
  requestCloseTab: FilesContextMenuActions["closeTab"];
  requestCloseFamily: ReturnType<typeof createFilesTabCommands>["requestCloseFamily"];
  openFilesList: () => void;
  onOpenInNewWindow: () => ((key?: string) => void | Promise<void>) | undefined;
  onOpenFileInNewWindow: () => ((rootId: string, path: string, title: string) => void | Promise<void>) | undefined;
  openFile: (selection: FilesTreeSelection, intent: BufferOpenIntent) => void;
  runFileRevert: (rootId: string, path: string) => Promise<void>;
  revealInTree: FilesContextMenuActions["revealInTree"];
  treeReveal: Pick<ReturnType<typeof createFilesTreeReveal>, "action">;
  activeInfoActions: () => { copyPath: () => void; copyRelativePath: () => void; addToChat: () => void } | null;
  editorContextActions: ReturnType<typeof createFilesEditorActions>["editorContextActions"];
  setCtxMenu: (menu: { anchor: ContextMenuAnchor; items: ContextMenuItem[] }) => void;
};

export function createFileContextActions(options: FileContextDependencies) {
  const bufferForKey = (key: string) => options.state().byKey[key];
  const moveToTrash: FilesContextMenuActions["moveToTrash"] = (rootId, path, name, isDir) =>
    options.setTrashConfirm({
      operationId: crypto.randomUUID(),
      sessionId: options.sourceSessionId(),
      rootId,
      path,
      name,
      isDir,
      body: trashConfirmBody(name, isDir),
    });

  const ctxActions = (
    capturedSymbolTarget: EditorSymbolTarget = {
      kind: "missing",
      reason: "whitespace",
    },
    capturedEditorView?: EditorView,
    capturedPosition?: number | null,
  ): FilesContextMenuActions => ({
    newFile: (rootId, dir) => {
      const rid = options.resolveRootId(rootId);
      if (!rid) return;
      options.startTreeCreate()?.("file", rid, dir);
    },
    newFolder: (rootId, dir) => {
      const rid = options.resolveRootId(rootId);
      if (!rid) return;
      options.startTreeCreate()?.("folder", rid, dir);
    },
    rename: (rootId, path, isDir) => options.setRenaming({ rootId, path, isDir }),
    moveTo: (rootId, path, isDir) => {
      options.setMoveError(null);
      options.setMoveSource({ rootId, path, isDir });
    },
    duplicate: (rootId, path, isDir) =>
      void options.duplicateEntry(rootId, path, isDir),
    moveToTrash,
    copyPath: (rootId, path) => options.copyAbsolutePathRef(rootId, path),
    copyRelativePath: (rootId, path) => options.copyRelativePathRef(rootId, path),
    addToChat: (rootId, path, isDir) => void options.addPathToChat(rootId, path, isDir),
    explainFile: options.explainFileInReview,
    openInTarget: (target) => {
      const roots = options.filesRoots();
      if (target.surface === "tab") {
        const buffer = bufferForKey(target.bufferKey);
        return buffer ? fileOpenTarget(buffer, roots) : null;
      }
      if (target.surface === "editor" || target.surface === "gutter" || target.surface === "info-card" || target.surface === "image-viewer") {
        const buffer = options.activeBuffer();
        const view = buffer ? getFilesEditorView(options.projectId(), buffer.key) : null;
        const line = target.surface === "gutter" ? target.line ?? undefined
          : view ? view.state.doc.lineAt(view.state.selection.main.head).number : undefined;
        return buffer ? fileOpenTarget(buffer, roots, line) : null;
      }
      if (target.surface === "tree-background") {
        const root = roots[0];
        return root ? { absolutePath: root.path, projectRoots: roots.map((r) => r.path), entryKind: "folder" } : null;
      }
      if (target.surface !== "tree-row" && target.surface !== "breadcrumb" && target.surface !== "changed-file") return null;
      const root = roots.find((r) => r.id === target.rootId);
      if (!root) return null;
      return { absolutePath: absolutePathForBuffer(root.path, target.path), projectRoots: roots.map((r) => r.path),
        entryKind: "isDir" in target && target.isDir ? "folder" : "file",
        unavailable: "deleted" in target && target.deleted ? "This file is not present on disk." : undefined };
    },
    collapseAllFolders: (scope) => options.collapseTreeAll()?.(scope),
    expandAllFolders: (scope) => options.expandTreeAll()?.(scope),
    closeTab: (bufferKey) => options.requestCloseTab(bufferKey),
    closeOtherTabs: (bufferKey) =>
      options.requestCloseFamily("closeOthers", bufferKey),
    closeToTheRight: (bufferKey) =>
      options.requestCloseFamily("closeToTheRight", bufferKey),
    closeSavedTabs: () => options.requestCloseFamily("closeSaved", options.state().activeKey),
    closeAllTabs: () => options.requestCloseFamily("closeAll", options.state().activeKey),
    keepOpenTab: (bufferKey) =>
      promoteFilesBuffer(options.projectId(), bufferKey),
    tabIsPreview: (bufferKey) => Boolean(bufferForKey(bufferKey)?.preview),
    togglePinTab: (bufferKey) =>
      toggleFilesBufferPinned(options.projectId(), bufferKey),
    tabIsPinned: (bufferKey) => Boolean(bufferForKey(bufferKey)?.pinned),
    openFilesList: options.openFilesList,
    openInNewWindow: (bufferKey) => {
      void Promise.resolve(options.onOpenInNewWindow()?.(bufferKey)).catch((err) =>
        console.debug("[item-window] open file failed", err),
      );
    },
    ...(options.onOpenFileInNewWindow()
      ? {
          openFileInNewWindow: (rootId: string, path: string, title: string) => {
            void Promise.resolve(
              options.onOpenFileInNewWindow()?.(rootId, path, title),
            ).catch((err) =>
              console.debug("[item-window] open tree file failed", err),
            );
          },
        }
      : {}),
    renameTab: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (buf) {
        options.setRenaming({ rootId: buf.rootId, path: buf.path, isDir: false });
      }
    },
    trashTab: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (!buf) return;
      moveToTrash(buf.rootId, buf.path, buf.name, false);
    },
    copyTabPath: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (buf) options.copyAbsolutePathRef(buf.rootId, buf.path);
    },
    copyTabRelativePath: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (buf) options.copyRelativePathRef(buf.rootId, buf.path);
    },
    showTabChanges: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (buf) setFilesStagePaneMode(options.projectId(), "review");
    },
    openFile: (rootId, path) => {
      const root = options.filesRoots().find((r) => r.id === rootId);
      options.openFile(
        {
          rootId,
          rootLabel: root?.label ?? "repo",
          path,
        },
        "permanent",
      );
    },
    revertChangedFile: (rootId, path) => {
      void options.runFileRevert(rootId, path);
    },
    addTabToChat: (bufferKey) => {
      const buf = bufferForKey(bufferKey);
      if (buf) void options.addPathToChat(buf.rootId, buf.path, false);
    },
    copyPathLine: (path, line) => {
      const buf = options.activeBuffer();
      const p = path.trim() || buf?.path || "";
      if (!p) return;
      const root = options.filesRoots().find((r) => r.id === buf?.rootId);
      if (!root) return;
      void copyTextToClipboard(`${absolutePathForBuffer(root.path, p)}:${line}`);
    },
    copyRelativePathLine: (path, line) => {
      const buf = options.activeBuffer();
      const p = path.trim() || buf?.path || "";
      if (!p) return;
      void copyTextToClipboard(`${p}:${line}`);
    },
    addPathLineToChat: (path, line) => {
      const buf = options.activeBuffer();
      const p = path.trim() || buf?.path || "";
      const rootId = buf?.rootId ?? "";
      if (!p || !rootId) return;
      void copyTextToClipboard(`${p}:${line}`);
      void options.addPathToChat(rootId, p, false);
    },
    revealInTree: options.revealInTree,
    tabRevealInTree: (key) => options.treeReveal.action(() => bufferForKey(key), options.revealInTree),
    editorRevealInTree: options.treeReveal.action(options.activeBuffer, options.revealInTree),
    infoCopyPath: () => options.activeInfoActions()?.copyPath(),
    infoCopyRelativePath: () => options.activeInfoActions()?.copyRelativePath(),
    infoAddToChat: () => options.activeInfoActions()?.addToChat(),
    ...options.editorContextActions(capturedSymbolTarget, capturedEditorView, capturedPosition),
  });

  const captureEditorSymbolTarget = (
    pointer?: { x: number; y: number },
    target?: EventTarget | null,
  ): { view: EditorView; target: EditorSymbolTarget; position: number | null } | null => {
    const buf = options.activeBuffer();
    if (!buf || isComposedBufferKind(buf.kind)) return null;
    const editorRoot = target instanceof Element
      ? target.closest<HTMLElement>(".cm-editor")
      : null;
    const view = editorRoot
      ? EditorView.findFromDOM(editorRoot)
      : getFilesEditorView(options.projectId(), buf.key);
    if (!view) return null;
    // A right-click leaves the caret where it was, so the document position
    // under the pointer is what the menu addresses.
    const position = pointer ? view.posAtCoords(pointer) : null;
    return {
      view,
      position,
      target: pointer
        ? editorSymbolTarget(view.state, { source: "pointer", position })
        : editorSymbolTarget(view.state, { source: "caret" }),
    };
  };

  const showCapturedSymbol = (
    captured: { view: EditorView; target: EditorSymbolTarget } | null,
  ) => {
    if (!captured || captured.target.kind !== "symbol") return;
    emphasizeSymbolRangeOnly(
      captured.view,
      captured.target.from,
      captured.target.to,
    );
  };

  const openContextMenu = (e: MouseEvent) => {
    if (e.defaultPrevented) return;
    const target = resolveFilesContextTarget(e.target);
    // macOS spells a secondary click either as button 2 or as Ctrl and the
    // primary button; only the keyboard menu key arrives without a pointer.
    const pointerDriven = e.button !== 0 || e.ctrlKey;
    const captured =
      target.surface === "editor"
        ? captureEditorSymbolTarget(pointerDriven ? { x: e.clientX, y: e.clientY } : undefined, e.target)
        : null;
    const items = filesContextMenuItems(
      target,
      ctxActions(captured?.target, captured?.view, captured?.position),
    );
    if (items === null) return;
    e.preventDefault();
    if (items.length === 0) return;
    showCapturedSymbol(captured);
    options.setCtxMenu({ anchor: { x: e.clientX, y: e.clientY }, items });
  };

  return { ctxActions, openContextMenu };
}

/** Path references and handoffs shared by menus, info cards, and summaries. */
export function createFilePathIntents(options: Pick<FilesScope, "projectId" | "filesRoots" | "rootLabelFor" | "activeBuffer"> & {
  openReviewRowFile: (rootId: string, path: string) => void;
}) {
  const copyAbsolutePathRef = (rootId: string, path: string) => {
    const root = options.filesRoots().find((r) => r.id === rootId);
    if (!root) return;
    void copyTextToClipboard(absolutePathForBuffer(root.path, path));
  };

  const copyRelativePathRef = (_rootId: string, path: string) => {
    void copyTextToClipboard(path);
  };

  const addPathToChat = (rootId: string, path: string, isDir: boolean) => {
    const name =
      path === "." || path === ""
        ? options.rootLabelFor(rootId)
        : fileDisplayName(path);
    void addToChat({
      kind: isDir ? "path-folder" : "path-file",
      projectId: options.projectId(),
      rootId,
      path,
      name,
    });
  };

  const explainFileInReview = (rootId: string, path: string) => {
    options.openReviewRowFile(rootId, path);
    setFilesStagePaneMode(options.projectId(), "review");
  };

  const activeInfoActions = () => {
    const buf = options.activeBuffer();
    if (!buf) return null;
    const absRoot = options.filesRoots().find((r) => r.id === buf.rootId);
    const abs = absRoot
      ? absolutePathForBuffer(absRoot.path, buf.path)
      : undefined;
    return {
      copyPath: () => {
        if (abs) {
          void copyTextToClipboard(abs);
        }
      },
      copyRelativePath: () => void copyTextToClipboard(buf.path),
      addToChat: () => {
        const rootId = buf.rootId.trim();
        if (!rootId) return;
        void addToChat({
          kind: "path-file",
          projectId: options.projectId(),
          rootId,
          path: buf.path,
          name: buf.name,
        });
      },
    };
  };

  return { copyAbsolutePathRef, copyRelativePathRef, addPathToChat, explainFileInReview, activeInfoActions };
}
