import type { FileTreeRevealAction } from "../tree/files-tree-reveal.ts";
import { contextAction, bindContextAction } from "../../components/context-actions.ts";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";
import { contributionContextMenuItems } from "../../contributions/context-menu.ts";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import { pathMenuItems } from "../../components/path-menu-items.ts";
import { textEditSystemMenuItems } from "../../components/text-edit-menu-items.ts";
import {
  editableHasSelection,
  resolveTextEditContext,
  snapshotEditable,
  type TextEditContext,
} from "../../platform/interaction/text-edit-context.ts";
import { filePathActionItems } from "../components/project-files-info-actions.ts";
import {
  bufferTabMenuItems,
  tabStripMenuItems,
} from "../tabs/project-files-buffer-menu.ts";
import { askSelectionVerbs } from "../../components/source/verbs/selection-verbs.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { openInMenuItems } from "../../components/open-in-menu-items.ts";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import type { EditorSymbolTarget } from "../editor/editor-symbol-target.ts";

export type FilesContextTarget =
  | {
      surface: "tree-row";
      rootId: string;
      path: string;
      name: string;
      isDir: boolean;
      isRoot: boolean;
      deleted?: boolean;
    }
  | { surface: "tree-background" }
  | {
      surface: "tab";
      bufferKey: string;
      rootId: string;
      path: string;
      name: string;
    }
  | { surface: "tab-strip" }
  | { surface: "gutter"; line: number | null }
  | {
      surface: "editor";
      /** Captured before opening the menu. */
      textEdit: TextEditContext;
      hasSelection: boolean;
    }
  | {
      surface: "breadcrumb";
      rootId: string;
      path: string;
      isDir: boolean;
    }
  | { surface: "image-viewer" }
  | { surface: "info-card" }
  | {
      surface: "changed-file";
      rootId: string;
      path: string;
      name: string;
    }
  | { surface: "no-menu" }
  | { surface: "fallback" };

export type FilesContextMenuActions = {
  newFile: (rootId: string, dir: string) => void;
  newFolder: (rootId: string, dir: string) => void;
  rename: (rootId: string, path: string, isDir: boolean) => void;
  moveTo: (rootId: string, path: string, isDir: boolean) => void;
  duplicate: (rootId: string, path: string, isDir: boolean) => void;
  moveToTrash: (rootId: string, path: string, name: string, isDir: boolean) => void;
  copyPath: (rootId: string, path: string) => void;
  copyRelativePath: (rootId: string, path: string) => void;
  addToChat: (rootId: string, path: string, isDir: boolean) => void;
  explainFile?: (rootId: string, path: string) => void;
  openInTarget?: (target: FilesContextTarget) => LocalPathTarget | null;
  collapseAllFolders: (scope?: { rootId: string; dir: string }) => void;
  expandAllFolders: (scope?: { rootId: string; dir: string }) => void;
  closeTab: (bufferKey: string) => void;
  closeOtherTabs: (bufferKey: string) => void;
  closeToTheRight: (bufferKey: string) => void;
  closeSavedTabs: () => void;
  closeAllTabs: () => void;
  keepOpenTab: (bufferKey: string) => void;
  tabIsPreview: (bufferKey: string) => boolean;
  togglePinTab: (bufferKey: string) => void;
  tabIsPinned: (bufferKey: string) => boolean;
  openFilesList: () => void;
  openInNewWindow?: (bufferKey?: string) => void;
  openFileInNewWindow?: (rootId: string, path: string, title: string) => void;
  renameTab: (bufferKey: string) => void;
  trashTab: (bufferKey: string) => void;
  copyTabPath: (bufferKey: string) => void;
  copyTabRelativePath: (bufferKey: string) => void;
  addTabToChat: (bufferKey: string) => void;
  showTabChanges?: (bufferKey: string) => void;
  openFile?: (rootId: string, path: string) => void;
  revertChangedFile?: (rootId: string, path: string) => void;
  copyPathLine: (path: string, line: number) => void;
  copyRelativePathLine: (path: string, line: number) => void;
  addPathLineToChat: (path: string, line: number) => void;
  revealInTree: (rootId: string, path: string, isDir?: boolean) => void;
  tabRevealInTree: (bufferKey: string) => FileTreeRevealAction;
  editorRevealInTree?: FileTreeRevealAction;
  infoCopyPath?: () => void;
  infoCopyRelativePath?: () => void;
  infoAddToChat?: () => void;
  editorCopyPath?: () => void;
  editorCopyRelativePath?: () => void;
  editorAddSelectionToChat?: () => void;
  editorSymbol: {
    target: EditorSymbolTarget;
    rename: () => void;
    goToDefinition: () => void;
  };
  editorSelectionVerb?: (verb: import("../../components/source/verbs/selection-verbs.ts").SelectionVerbId) => void;
  renameShortcutHint?: string;
  goToDefinitionShortcutHint?: string;
  editorEmphasizeScope?: () => void;
  editorHasFinding?: boolean;
  /** Host facts at the caret or selection. */
  editorSecret?: EditorSecretActions;
  addSelectionShortcutHint?: string;
};

/** Host-backed secret actions for the captured range. */
export type EditorSecretActions = {
  span: {
    fact: string;
    state: "tracked" | "retired" | "detected";
    reference: string;
    siblings: number;
  } | null;
  markSelection?: () => void;
  trackSpan?: () => void;
  ignoreSpan?: () => void;
  selectSiblings?: () => void;
  copyReference?: () => void;
  openInSecrets?: () => void;
};

function editorSecretItems(actions: EditorSecretActions | undefined): ContextMenuItem[] {
  if (!actions) return [];
  const span = actions.span;
  const items: ContextMenuItem[] = [];
  if (!span) {
    if (!actions.markSelection) return [];
    items.push(bindContextAction({
      label: SECRET_SPAN_COPY.markSelection,
      testId: "files-ctx-editor-mark-secret",
      onSelect: actions.markSelection,
    }));
    return items;
  }
  items.push({ label: span.fact, testId: "files-ctx-editor-secret-fact", header: true });
  if (span.state === "detected" && actions.trackSpan) {
    items.push(bindContextAction({
      label: SECRET_SPAN_COPY.trackSpan,
      testId: "files-ctx-editor-track-secret",
      onSelect: actions.trackSpan,
    }));
  }
  if (span.state === "tracked" && actions.copyReference) {
    items.push(bindContextAction({
      label: SECRET_SPAN_COPY.copyReference,
      testId: "files-ctx-editor-copy-reference",
      onSelect: actions.copyReference,
    }));
  }
  if (span.siblings > 0 && actions.selectSiblings) {
    items.push(bindContextAction({
      label: `${SECRET_SPAN_COPY.findOtherUses} (${span.siblings})`,
      testId: "files-ctx-editor-secret-siblings",
      onSelect: actions.selectSiblings,
    }));
  }
  if (span.state === "detected" && actions.ignoreSpan) {
    items.push(bindContextAction({
      label: SECRET_SPAN_COPY.ignoreInProject,
      testId: "files-ctx-editor-ignore-secret",
      onSelect: actions.ignoreSpan,
    }));
  }
  if (actions.openInSecrets) {
    items.push(bindContextAction({
      label: SECRET_SPAN_COPY.openInSecrets,
      testId: "files-ctx-editor-open-secrets",
      onSelect: actions.openInSecrets,
    }));
  }
  items.push({ separator: true, testId: "files-ctx-editor-sep-secret" });
  return items;
}

const ROW_SEL = "[data-files-ctx='tree-row']";
const BG_SEL = "[data-files-ctx='tree-background']";
const NO_MENU_SEL = "[data-files-ctx='no-menu']";
const TAB_SEL = "[data-files-ctx='tab']";
const TAB_STRIP_SEL = "[data-files-ctx='tab-strip']";
const GUTTER_SEL = "[data-files-ctx='gutter']";
const CRUMB_SEL = "[data-files-ctx='breadcrumb']";
const IMAGE_VIEWER_SEL = "[data-files-ctx='image-viewer']";
const INFO_CARD_SEL = "[data-files-ctx='info-card']";
const CHANGED_FILE_SEL = "[data-files-ctx='changed-file']";
const FILTER_SEL = "[data-testid='files-tree-filter']";
const RENAME_SEL = "[data-testid='files-tree-rename-input']";
const CREATE_SEL = "[data-testid='files-tree-new-entry-input']";

export function resolveFilesContextTarget(
  target: EventTarget | null,
): FilesContextTarget {
  if (!(target instanceof Element)) return { surface: "fallback" };
  if (
    target.closest(FILTER_SEL) ||
    target.closest(RENAME_SEL) ||
    target.closest(CREATE_SEL) ||
    target.closest(NO_MENU_SEL)
  ) {
    return { surface: "no-menu" };
  }
  const changedFile = target.closest<HTMLElement>(CHANGED_FILE_SEL);
  if (changedFile) {
    const rootId = changedFile.dataset.rootId?.trim() ?? "";
    const path = changedFile.dataset.path?.trim() ?? "";
    const name =
      changedFile.dataset.name?.trim() ||
      path.split("/").filter(Boolean).pop() ||
      path;
    if (!path) return { surface: "fallback" };
    return { surface: "changed-file", rootId, path, name };
  }
  const tab = target.closest<HTMLElement>(TAB_SEL);
  if (tab) {
    const bufferKey = tab.dataset.key?.trim() ?? "";
    const rootId = tab.dataset.root?.trim() ?? "";
    const path = tab.dataset.path?.trim() ?? "";
    const name = tab.dataset.name?.trim() || path;
    if (!bufferKey) return { surface: "fallback" };
    return { surface: "tab", bufferKey, rootId, path, name };
  }
  const gutter = target.closest<HTMLElement>(GUTTER_SEL);
  if (gutter || target.closest(".cm-gutters")) {
    const lineRaw =
      target.closest<HTMLElement>(".cm-gutterElement")?.textContent?.trim() ||
      "";
    const line = lineRaw ? Number.parseInt(lineRaw, 10) : NaN;
    return {
      surface: "gutter",
      line: Number.isFinite(line) && line >= 1 ? line : null,
    };
  }
  const editorContent = target.closest<HTMLElement>(".cm-content");
  if (editorContent) {
    const textEdit = resolveTextEditContext(target);
    if (textEdit?.kind === "editable") {
      return {
        surface: "editor",
        textEdit,
        hasSelection: editableHasSelection(textEdit.snapshot),
      };
    }
    // Read-only content retains copy and annotation actions.
    const snapshot = snapshotEditable(editorContent);
    return {
      surface: "editor",
      textEdit: { kind: "editable", snapshot },
      hasSelection: editableHasSelection(snapshot),
    };
  }
  const crumb = target.closest<HTMLElement>(CRUMB_SEL);
  if (crumb) {
    const rootId = crumb.dataset.root?.trim() ?? "";
    const path = crumb.dataset.path?.trim() ?? "";
    const isDir = crumb.dataset.isdir === "true";
    if (!rootId || !path) return { surface: "fallback" };
    return { surface: "breadcrumb", rootId, path, isDir };
  }
  if (target.closest(INFO_CARD_SEL)) {
    return { surface: "info-card" };
  }
  if (target.closest(IMAGE_VIEWER_SEL)) {
    return { surface: "image-viewer" };
  }
  if (target.closest(TAB_STRIP_SEL)) {
    return { surface: "tab-strip" };
  }
  const row = target.closest<HTMLElement>(ROW_SEL);
  if (row) {
    const rootId = row.dataset.root?.trim() ?? "";
    const path = row.dataset.path?.trim() || ".";
    const name = row.dataset.name?.trim() || path;
    const isDir = row.dataset.isdir === "true";
    const isRoot = row.dataset.isroot === "true";
    if (!rootId) return { surface: "fallback" };
    return {
      surface: "tree-row",
      rootId,
      path,
      name,
      isDir,
      isRoot,
      deleted: row.dataset.deleted === "true",
    };
  }
  if (target.closest(BG_SEL)) return { surface: "tree-background" };
  return { surface: "fallback" };
}

export function filesContextMenuItems(
  target: FilesContextTarget,
  actions: FilesContextMenuActions,
): ContextMenuItem[] | null {
  const openIn = actions.openInTarget?.(target);
  const showTabChanges = actions.showTabChanges;
  const openInNewWindow = actions.openInNewWindow;
  switch (target.surface) {
    case "fallback":
      return null;
    case "no-menu":
      return [];
    case "tab-strip":
      return tabStripMenuItems({
        openFilesList: () => actions.openFilesList(),
        closeSaved: () => actions.closeSavedTabs(),
        closeAll: () => actions.closeAllTabs(),
        openInNewWindow: actions.openInNewWindow,
      });
    case "tab":
      return bufferTabMenuItems({
        close: () => actions.closeTab(target.bufferKey),
        closeOthers: () => actions.closeOtherTabs(target.bufferKey),
        closeToTheRight: () => actions.closeToTheRight(target.bufferKey),
        closeSaved: () => actions.closeSavedTabs(),
        closeAll: () => actions.closeAllTabs(),
        showKeepOpen: actions.tabIsPreview(target.bufferKey),
        keepOpen: () => actions.keepOpenTab(target.bufferKey),
        pin: () => actions.togglePinTab(target.bufferKey),
        pinned: actions.tabIsPinned(target.bufferKey),
        copyPath: () => actions.copyTabPath(target.bufferKey),
        copyRelativePath: () =>
          actions.copyTabRelativePath(target.bufferKey),
        openIn,
        revealInTree: actions.tabRevealInTree(target.bufferKey),
        addToChat: () => actions.addTabToChat(target.bufferKey),
        showChanges: showTabChanges
          ? () => showTabChanges(target.bufferKey)
          : undefined,
        openInNewWindow: openInNewWindow
          ? () => openInNewWindow(target.bufferKey)
          : undefined,
        rename: () => actions.renameTab(target.bufferKey),
        moveToTrash: () => actions.trashTab(target.bufferKey),
      });
    case "changed-file": {
      const open = actions.openFile;
      const items = pathMenuItems({
        open: open ? { testId: "files-ctx-changed-open", onSelect: () => open(target.rootId, target.path) } : undefined,
        revealInTree: { testId: "files-ctx-changed-reveal-tree", onSelect: () => actions.revealInTree(target.rootId, target.path) },
        openIn,
        copyAbsolute: () => actions.copyPath(target.rootId, target.path),
        copyRelative: () => actions.copyRelativePath(target.rootId, target.path),
        absoluteTestId: "files-ctx-changed-copy", relativeTestId: "files-ctx-changed-copy-relative",
        addToChat: { testId: "files-ctx-changed-add-to-chat", onSelect: () => actions.addToChat(target.rootId, target.path, false) },
      });
      const revertChangedFile = actions.revertChangedFile;
      if (revertChangedFile) {
        items.push(
          {
            separator: true,
            testId: "files-ctx-changed-sep",
          },
          contextAction("restoreComparisonBaseline", {
            testId: "files-ctx-changed-revert",

            onSelect: () => revertChangedFile(target.rootId, target.path),
          }),
        );
      }
      return items;
    }
    case "gutter": {
      if (target.line == null) return [];
      const line = target.line;
      return pathMenuItems({
        openIn,
        revealInTree: actions.editorRevealInTree ? { testId: "files-ctx-gutter-reveal-tree", ...actions.editorRevealInTree } : undefined,
        withLine: true,
        copyAbsolute: () => actions.copyPathLine("", line),
        copyRelative: () => actions.copyRelativePathLine("", line),
        absoluteTestId: "files-ctx-gutter-copy", relativeTestId: "files-ctx-gutter-copy-relative",
        addToChat: { testId: "files-ctx-gutter-add-to-chat", onSelect: () => actions.addPathLineToChat("", line) },
      });
    }
    case "editor": {
      const contributions = contributionContextMenuItems(["editor.context.analysis", "editor.context.edit"]);
      const readOnly =
        target.textEdit.kind === "editable" &&
        target.textEdit.snapshot.readOnly;
      const hint = actions.addSelectionShortcutHint?.trim();
      const symbolTarget = actions.editorSymbol.target;
      const symbol =
        symbolTarget.kind === "symbol"
          ? symbolTarget.symbol.trim() || null
          : null;
      const renameHint = actions.renameShortcutHint?.trim();
      const defHint = actions.goToDefinitionShortcutHint?.trim();
      const symbolItems: ContextMenuItem[] = symbol && !readOnly
        ? [
            bindContextAction({
              label: `Rename “${symbol}”…`,
              shortcut: renameHint,
              testId: "files-ctx-editor-rename",
              onSelect: actions.editorSymbol.rename,
            }),
            bindContextAction({
              label: `Go to definition of “${symbol}”`,
              shortcut: defHint,
              testId: "files-ctx-editor-go-to-definition",
              onSelect: actions.editorSymbol.goToDefinition,
            }),
          ]
        : [];
      const addToChat = actions.editorAddSelectionToChat;
      const selectionVerb = actions.editorSelectionVerb;
      const items: ContextMenuItem[] = [
        ...textEditSystemMenuItems(target.textEdit, {
          omitMutatorsWhenReadOnly: true,
        }),
        {
          separator: true,
          testId: "files-ctx-editor-sep-actions",
        },
        ...symbolItems,
      ];
      items.push(...editorSecretItems(actions.editorSecret));
      if (contributions.length > 0) items.push(...contributions, { separator: true });
      if (!readOnly) {
        items.push(contextAction("inlineEdit", {
          testId: "files-ctx-editor-inline-edit",
          onHighlight: actions.editorEmphasizeScope,
          onSelect: () => invokeCommand("editor.inlineEdit"),
        }));
      }
      if (selectionVerb && !readOnly) {
        items.push(contextAction("askAboutSelection", {
          testId: "files-ctx-editor-ask",
          submenu: askSelectionVerbs({
            hasFinding: actions.editorHasFinding === true,
          }).map((verb) => (bindContextAction({
            label: verb.menuLabel,
            testId: verb.testId,
            onHighlight: actions.editorEmphasizeScope,
            onSelect: () => selectionVerb(verb.id),
          }))),
        }));
      }
      items.push(...pathMenuItems({
        openIn,
        revealInTree: actions.editorRevealInTree ? { testId: "files-ctx-editor-reveal-tree", ...actions.editorRevealInTree } : undefined,
        copyAbsolute: actions.editorCopyPath, copyRelative: actions.editorCopyRelativePath,
        absoluteTestId: "files-ctx-editor-copy-path", relativeTestId: "files-ctx-editor-copy-relative-path",
        addToChat: addToChat ? { testId: "files-ctx-editor-add-to-chat", shortcut: hint, onSelect: addToChat } : undefined,
      }));
      return items;
    }
    case "breadcrumb":
      return pathMenuItems({
        openIn,
        revealInTree: actions.editorRevealInTree ? { testId: "files-ctx-crumb-reveal-tree", ...("disabled" in actions.editorRevealInTree ? actions.editorRevealInTree : { onSelect: () => actions.revealInTree(target.rootId, target.path, target.isDir) }) } : undefined,
        copyAbsolute: () => actions.copyPath(target.rootId, target.path),
        copyRelative: () => actions.copyRelativePath(target.rootId, target.path),
        absoluteTestId: "files-ctx-crumb-copy", relativeTestId: "files-ctx-crumb-copy-relative",
        addToChat: { testId: "files-ctx-crumb-add-to-chat", onSelect: () => actions.addToChat(target.rootId, target.path, target.isDir) },
      });
    case "info-card":
    case "image-viewer":
      if (
        !actions.infoCopyPath ||
        !actions.infoAddToChat
      ) {
        return [];
      }
      return filePathActionItems({
        openIn,
        revealInTree: actions.editorRevealInTree,
        copyPath: actions.infoCopyPath,
        copyRelativePath: actions.infoCopyRelativePath,
        addToChat: actions.infoAddToChat,
      }, target.surface === "info-card" ? "files-info" : "files-image");
    case "tree-background":
      return [
        contextAction("newFile", {
          testId: "files-ctx-new-file",
          onSelect: () => actions.newFile("", "."),
        }),
        contextAction("newFolder", {
          testId: "files-ctx-new-folder",
          onSelect: () => actions.newFolder("", "."),
        }),
        ...openInMenuItems(openIn),
        contextAction("collapseAll", {
          testId: "files-ctx-collapse-all",
          onSelect: () => actions.collapseAllFolders(),
        }),
        contextAction("expandAll", {
          testId: "files-ctx-expand-all",
          onSelect: () => actions.expandAllFolders(),
        }),
      ];
    case "tree-row": {
      const dir = target.isDir ? target.path : parentDir(target.path);
      const items: ContextMenuItem[] = [];
      const revertChangedFile = actions.revertChangedFile;
      if (target.deleted && !target.isDir && revertChangedFile) {
        items.push(contextAction("restoreComparisonBaseline", {
          testId: "files-ctx-tree-revert",
          onSelect: () => revertChangedFile(target.rootId, target.path),
        }));
      }
      const openFileInNewWindow = actions.openFileInNewWindow;
      if (!target.isDir && openFileInNewWindow) {
        items.push(contextAction("openInNewWindow", {
          testId: "files-ctx-open-window",
          onSelect: () =>
            openFileInNewWindow(
              target.rootId,
              target.path,
              target.name,
            ),
        }));
      }
      if (target.isDir) {
        items.push(
          contextAction("newFile", {
            testId: "files-ctx-new-file",
            onSelect: () => actions.newFile(target.rootId, dir),
          }),
          contextAction("newFolder", {
            testId: "files-ctx-new-folder",
            onSelect: () => actions.newFolder(target.rootId, dir),
          }),
          contextAction("collapseAll", {
            testId: "files-ctx-collapse-all",
            onSelect: () =>
              actions.collapseAllFolders({
                rootId: target.rootId,
                dir: target.path,
              }),
          }),
          contextAction("expandAll", {
            testId: "files-ctx-expand-all",
            onSelect: () =>
              actions.expandAllFolders({
                rootId: target.rootId,
                dir: target.path,
              }),
          }),
        );
      }
      if (!target.isRoot) {
        items.push(contextAction("rename", {
          testId: "files-ctx-rename",
          onSelect: () => actions.rename(target.rootId, target.path, target.isDir),
        }));
        items.push(contextAction("moveTo", {
          testId: "files-ctx-move-to",
          onSelect: () => actions.moveTo(target.rootId, target.path, target.isDir),
        }));
      }
      items.push(contextAction("duplicate", {
        testId: "files-ctx-duplicate",
        onSelect: () =>
          actions.duplicate(target.rootId, target.path, target.isDir),
      }));
      items.push(...pathMenuItems({
        open: !target.isDir && actions.openFile ? { testId: "files-ctx-open", onSelect: () => actions.openFile?.(target.rootId, target.path) } : undefined,
        openIn,
        copyAbsolute: () => actions.copyPath(target.rootId, target.path),
        copyRelative: target.isRoot
          ? null
          : () => actions.copyRelativePath(target.rootId, target.path),
        absoluteTestId: "files-ctx-copy-path",
        relativeTestId: "files-ctx-copy-relative-path",
        addToChat: {
          testId: "files-ctx-add-to-chat",
          onSelect: () => actions.addToChat(target.rootId, target.path, target.isDir),
        },
      }));
      const explainFile = actions.explainFile;
      if (!target.isDir && explainFile) {
        items.push(contextAction("explainInReview", {
          testId: "files-ctx-explain-file",
          onSelect: () => explainFile(target.rootId, target.path),
        }));
      }
      if (!target.isRoot) {
        items.push(contextAction("moveToTrash", {
          testId: "files-ctx-trash",

          onSelect: () =>
            actions.moveToTrash(
              target.rootId,
              target.path,
              target.name,
              target.isDir,
            ),
        }));
      }
      return items;
    }
  }
}

function parentDir(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "." : path.slice(0, i) || ".";
}
