import { editorWordWrapPref, editorLineNumbersPref, saveEditorWordWrap, saveEditorLineNumbers } from "../../settings/editor/editor-prefs.ts";
import { EDITOR_STANDARD_COMMANDS } from "../../components/source/editor/editor-standard-commands.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { editorTabMovesFocusPref, saveEditorTabMovesFocus } from "../../settings/editor/editor-prefs.ts";
import { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { createFileDocumentCommands } from "../documents/file-document-commands.ts";
import { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { createDefinitionNavigation } from "../editor/definition-navigation.ts";
import { createFileRestoration } from "../history/file-restoration-commands.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import { createVersionNavigation } from "../history/version-navigation.ts";
import { createSourceHistoryCommands } from "../history/project-files-history.ts";
import { createFilesTreeReveal } from "../tree/files-tree-reveal.ts";
import type { EditorView } from "@codemirror/view";
import { createEffect, onCleanup, onMount } from "solid-js";
import { editorIndentStylePref, editorIndentWidthPref } from "../../settings/editor/editor-prefs.ts";
import { attachDispatcher, registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { openEditorGotoLine } from "../../components/source/editor/codemirror-theme.ts";
import { EDITOR_COMMAND_IMPL, EDITOR_EDITING_COMMAND_IDS } from "../../components/source/editor/editor-commands.ts";
import { indentInfoFromEditorConfig } from "../../components/source/editor/editorconfig.ts";
import { goToNextFinding, goToPreviousFinding } from "../../components/source/annotations/findings-diagnostics.ts";
import { convertIndentationCommand } from "../../components/source/editor/indent-convert.ts";
import { beginInlineEditPanel, beginRenameCard } from "../../components/source/inline-edit/run-editor-action.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { filesTreeCollapsed } from "../tree/files-tree-window-state.ts";
import { selectedFileVersion, toggleVersionComparison } from "../history/files-version-selection.ts";
import { versionRestoreOffered } from "../history/file-version-restore.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { type FilesTreeHandle } from "../tree/files-tree-context.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { applyFilesBufferDraft, setFilesBufferIndentOverride, toggleFilesBufferPinned } from "../documents/project-files-buffers.ts";
import type { NewEntryKind } from "../commands/project-files-create.ts";
import { navigateProjectJumpBack, navigateProjectJumpForward } from "../components/project-files-jump-bridge.ts";
import { isComparisonOff, requestScopePicker, cycleSidebarScope, toggleComparison, toggleDeletedLines } from "../review/review-pane.ts";
import { resolvedScope } from "../tree/scope-resolution.ts";
import { enterWalk, isWalking, leaveWalk, stepWalk } from "../walk/walk-store.ts";
import { currentTurnFromStore } from "../../components/project/SidebarScopePicker.tsx";

type CommandBindings = {
  scope: Pick<FilesScope, "projectId" | "client" | "state" | "activeBuffer" | "aimedKey" | "versionForBuffer" | "setSaveError">;
  appStore: AppStore;
  tree: () => FilesTreeHandle | undefined;
  createTargetDir: () => { rootId: string; dir: string } | null;
  showFilesTree: () => void;
  toggleFilesTree: () => void;
  onCloseTabIdle?: () => void;
  treeReveal: Pick<ReturnType<typeof createFilesTreeReveal>, "unavailable">;
  revealInTree: (rootId: string, path: string, isDir?: boolean) => void;
  editorPaneEl: () => HTMLDivElement | undefined;
  readerHandles: Map<string, { gotoLine: () => void | Promise<void> }>;
  tabs: Pick<ReturnType<typeof createFilesTabCommands>, "moveTab" | "cycleTab" | "restoreJumpEntry" | "openFilesList" | "reopenClosedTab">;
  requestCloseTab: ReturnType<typeof createFileConfirmations>["requestCloseTab"];
  documents: Pick<ReturnType<typeof createFileDocumentCommands>, "performSave" | "toggleLineEndings">;
  versions: Pick<ReturnType<typeof createVersionNavigation>, "returnToCurrent" | "stepVersion">;
  restoration: Pick<ReturnType<typeof createFileRestoration>, "runSelectedVersionRestore" | "runFileRevert" | "runEditorHunkReject">;
  editorActions: Pick<ReturnType<typeof createFilesEditorActions>, "handoffSelectionToChat" | "secretScreenReport" | "openMarkSecretDialog" | "buildEditorActionContext" | "abortEditorAction">;
  runSourceHistory: ReturnType<typeof createSourceHistoryCommands>["runSourceHistory"];
  runGoToDefinition: ReturnType<typeof createDefinitionNavigation>["runGoToDefinition"];
};

export function bindFilesCommands(options: CommandBindings): void {
  const { scope, appStore, tree, createTargetDir, showFilesTree, toggleFilesTree,
    onCloseTabIdle, treeReveal, revealInTree, editorPaneEl, readerHandles,
    tabs: tabCommands, requestCloseTab, documents, versions, restoration,
    editorActions, runSourceHistory, runGoToDefinition } = options;
  const { client, state, activeBuffer, aimedKey, versionForBuffer, setSaveError } = scope;
  const { cycleTab, restoreJumpEntry, openFilesList, reopenClosedTab } = tabCommands;
  const { performSave } = documents;
  const { returnToCurrent, stepVersion } = versions;
  const { runFileRevert, runEditorHunkReject } = restoration;
  const { handoffSelectionToChat, secretScreenReport, openMarkSecretDialog,
    buildEditorActionContext, abortEditorAction } = editorActions;
  // Retained views stay mounted; only an active one answers commands.
  const presence = useResidentPresence();
  createEffect(() => {
    if (presence() !== "active") return;
    const on = (id: string, run: () => void) => onCleanup(registerCommandHandler(id, run));
    // Editor commands act on the active text editor; edits also need it writable.
    const withEditor = (run: (view: EditorView) => unknown, edits = false) => () => {
      const buf = activeBuffer();
      if (!buf || isComposedBufferKind(buf.kind)) return;
      const view = getFilesEditorView(scope.projectId(), buf.key);
      if (view && !(edits && view.state.readOnly)) run(view);
    };
    const startCreate = (kind: NewEntryKind) => {
      const target = createTargetDir();
      if (target) tree()?.startCreate(kind, target.rootId, target.dir);
    };
    const convertIndent = (toStyle: "spaces" | "tabs") => withEditor((view) => {
      const buf = activeBuffer();
      if (!buf) return;
      const current =
        buf.indentOverride ??
        indentInfoFromEditorConfig(buf.editorConfig) ??
        buf.detectedIndent ?? {
          style: editorIndentStylePref(),
          width: editorIndentWidthPref(),
        };
      const to = { style: toStyle, width: current.width } as const;
      convertIndentationCommand(to, current.width)(view);
      setFilesBufferIndentOverride(scope.projectId(), buf.key, to);
      applyFilesBufferDraft(scope.projectId(), buf.key, view.state.doc.toString());
    }, true);
    for (const [id, command] of Object.entries(EDITOR_STANDARD_COMMANDS)) on(id, withEditor(command));
    on("editor.toggleWrap", () => { void saveEditorWordWrap(!editorWordWrapPref()); });
    on("editor.toggleLineNumbers", () => { void saveEditorLineNumbers(!editorLineNumbersPref()); });
    on("editor.toggleTabFocus", () => { void saveEditorTabMovesFocus(!editorTabMovesFocusPref()); });
    on("files.moveTabLeft", () => tabCommands.moveTab(-1));
    on("files.moveTabRight", () => tabCommands.moveTab(1));
    on("go.filesTree", () => {
      if (filesTreeCollapsed()) showFilesTree();
      queueMicrotask(() => focusRegion("filesTree"));
    });
    on("go.filesTabs", () => focusRegion("filesTabs"));
    on("files.saveAndClose", () => {
      const key = aimedKey();
      if (!key) return;
      flushFilesDraftSyncFor(scope.projectId(), key);
      if (!state().byKey[key]?.dirty) requestCloseTab(key);
      else void performSave(key).then(saved => { if (saved && !state().byKey[key]?.dirty) requestCloseTab(key); });
    });
    on("files.nextTab", () => cycleTab(1));
    on("files.prevTab", () => cycleTab(-1));
    on("files.closeTab", () => {
      const key = aimedKey();
      if (key) requestCloseTab(key);
      else onCloseTabIdle?.();
    });
    on("files.save", () => {
      const key = state().activeKey;
      if (key) void performSave(key);
    });
    on("files.reveal", () => {
      const key = aimedKey();
      const buf = key ? state().byKey[key] : undefined;
      if (buf && !treeReveal.unavailable(buf)) revealInTree(buf.rootId, buf.path, false);
    });
    on("files.collapseAll", () => tree()?.collapseAll());
    on("files.expandAll", () => tree()?.expandAll());
    on("files.openAllDiffs", () => {
      if (isComparisonOff(scope.projectId())) requestScopePicker(scope.projectId());
      else openFilesSurface({ kind: "diffs", projectId: scope.projectId(), address: { kind: "lens" } });
    });
    on("files.chooseComparison", () => requestScopePicker(scope.projectId()));
    on("files.toggleComparison", () => {
      const buf = activeBuffer();
      const version = buf ? selectedFileVersion(scope.projectId(), buf.key) : null;
      if (buf && version) toggleVersionComparison(scope.projectId(), buf.key, version);
      else toggleComparison(scope.projectId());
    });
    const scopeAvailability = () => ({
      chatSelected: currentTurnFromStore(appStore) != null,
      commitAvailable: resolvedScope(scope.projectId()).commitAvailable,
    });
    on("files.comparisonNext", () => {
      cycleSidebarScope(scope.projectId(), 1, scopeAvailability());
    });
    on("files.comparisonPrev", () => {
      cycleSidebarScope(scope.projectId(), -1, scopeAvailability());
    });
    on("files.walkToggle", () => {
      if (isWalking(scope.projectId())) {
        leaveWalk(scope.projectId());
        return;
      }
      const chat = currentTurnFromStore(appStore);
      if (!chat) return;
      const key = state().activeKey;
      const buf = key ? state().byKey[key] : undefined;
      const initialFile = buf ? { path: buf.path, rootId: buf.rootId } : null;
      void enterWalk(scope.projectId(), client(), chat.sessionId, initialFile);
    });
    on("files.walkNextStep", () => {
      if (isWalking(scope.projectId())) stepWalk(scope.projectId(), 1);
    });
    on("files.walkPrevStep", () => {
      if (isWalking(scope.projectId())) stepWalk(scope.projectId(), -1);
    });
    on("files.newFile", () => startCreate("file"));
    on("files.newFolder", () => startCreate("folder"));
    on("files.undoLifecycle", () => void runSourceHistory("undo"));
    on("files.redoLifecycle", () => void runSourceHistory("redo"));
    on("editor.goToLine", () => {
      const buf = activeBuffer();
      const reader = buf ? readerHandles.get(buf.key) : undefined;
      if (reader) void reader.gotoLine();
      else withEditor(openEditorGotoLine)();
    });
    on("editor.nextFinding", withEditor(goToNextFinding));
    on("editor.previousFinding", withEditor(goToPreviousFinding));
    on("files.jumpBack", () => {
      const entry = navigateProjectJumpBack(scope.projectId());
      if (entry) restoreJumpEntry(entry);
    });
    on("files.jumpForward", () => {
      const entry = navigateProjectJumpForward(scope.projectId());
      if (entry) restoreJumpEntry(entry);
    });
    on("files.currentVersion", () => {
      const buf = activeBuffer();
      if (buf && versionForBuffer(buf)) returnToCurrent(buf.key);
    });
    on("files.openVersionHistory", () => {
      editorPaneEl()?.querySelector<HTMLButtonElement>('[data-testid="file-version-trigger"]')?.click();
    });
    on("files.previousVersion", () => {
      const buf = activeBuffer();
      if (buf) stepVersion(buf, -1);
    });
    on("files.nextVersion", () => {
      const buf = activeBuffer();
      if (buf) stepVersion(buf, 1);
    });
    on("files.restoreVersion", () => {
      const buf = activeBuffer();
      const version = buf ? versionForBuffer(buf) : null;
      if (buf && version && versionRestoreOffered(buf, version)) void restoration.runSelectedVersionRestore(buf, version);
    });
    on("files.toggleDeletedLines", () => {
      toggleDeletedLines(scope.projectId());
    });
    on("files.toggleLineEndings", () => {
      const buf = activeBuffer();
      if (buf) documents.toggleLineEndings(buf);
    });
    on("files.openFilesList", openFilesList);
    on("files.togglePin", () => {
      const key = aimedKey();
      if (key) toggleFilesBufferPinned(scope.projectId(), key);
    });
    on("files.reopenClosedTab", reopenClosedTab);
    on("files.addSelectionToChat", () => handoffSelectionToChat());
    on("files.toggleTree", toggleFilesTree);
    on("files.goToDefinition", () => void runGoToDefinition());
    on("files.markSecret", openMarkSecretDialog);
    on("files.showSecretScreen", () => setSaveError(secretScreenReport()));
    on("editor.inlineEdit", () => {
      const ctx = buildEditorActionContext();
      if (ctx) void beginInlineEditPanel(ctx);
    });
    on("editor.renameSymbol", () => {
      const ctx = buildEditorActionContext();
      if (ctx) void beginRenameCard(ctx);
    });
    on("files.revertFileChanges", () => {
      const buf = activeBuffer();
      if (buf) void runFileRevert(buf.rootId, buf.path);
    });
    on("files.rejectHunk", () => void runEditorHunkReject());
    for (const id of EDITOR_EDITING_COMMAND_IDS) on(id, withEditor((view) => EDITOR_COMMAND_IMPL[id](view), true));
    on("editor.convertIndentationToSpaces", convertIndent("spaces"));
    on("editor.convertIndentationToTabs", convertIndent("tabs"));
  });
  onMount(() => {
    onCleanup(attachDispatcher());
    onCleanup(abortEditorAction);
  });

}
