// @vitest-environment jsdom
import { createSignal } from "solid-js";
import { render, cleanup } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { filesControllerFixture } from "../../test/files-controller-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { bindFilesCommands } from "./files-command-bindings.ts";
import { invokeCommand, resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { applyFilesBufferDraft, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { setFilesStore } from "../documents/files-buffer-state.ts";
import { produce } from "solid-js/store";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { requestScopePicker, toggleComparison, toggleDeletedLines, cycleSidebarScope } from "../review/review-pane.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { saveEditorWordWrap, saveEditorLineNumbers, saveEditorTabMovesFocus } from "../../settings/editor/editor-prefs.ts";
vi.mock("../../shortcuts/focus-region.ts", () => ({ focusRegion: vi.fn() }));
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface: vi.fn() }));
vi.mock("../review/review-pane.ts", async original => ({ ...await original<typeof import("../review/review-pane.ts")>(),
  requestScopePicker: vi.fn(), toggleComparison: vi.fn(), toggleDeletedLines: vi.fn(), cycleSidebarScope: vi.fn() }));
vi.mock("../../settings/editor/editor-prefs.ts", async original => ({ ...await original<typeof import("../../settings/editor/editor-prefs.ts")>(),
  saveEditorWordWrap: vi.fn().mockResolvedValue(undefined), saveEditorLineNumbers: vi.fn().mockResolvedValue(undefined), saveEditorTabMovesFocus: vi.fn().mockResolvedValue(undefined) }));
afterEach(() => { cleanup(); resetDispatcherForTests(); resetProjectFilesForTests(); vi.clearAllMocks(); });

function fixture() {
  const f = filesControllerFixture("command-routing"); const key = f.open();
  const [presence, setPresence] = createSignal<"active" | "idle">("active");
  let aimed: string | null = key;
  const pane = document.createElement("div"); const versionButton = document.createElement("button");
  versionButton.dataset.testid = "file-version-trigger"; pane.append(versionButton); const clicked = vi.fn(); versionButton.onclick = clicked;
  const performSave = vi.fn(async (savedKey: string) => { setFilesStore(f.projectId, produce(state => { if (state.byKey[savedKey]) state.byKey[savedKey]!.dirty = false; })); return true; });
  const options = {
    scope: { ...f.scope, aimedKey: () => aimed, versionForBuffer: () => null, setSaveError: vi.fn() }, appStore: createAppStore(),
    tree: () => ({ startCreate: vi.fn(), collapseAll: vi.fn(), expandAll: vi.fn(), confirmChange: vi.fn(), revealPath: vi.fn() }),
    createTargetDir: () => ({ rootId: "root", dir: "." }), showFilesTree: vi.fn(), toggleFilesTree: vi.fn(), onCloseTabIdle: vi.fn(),
    treeReveal: { unavailable: () => null }, revealInTree: vi.fn(), editorPaneEl: () => pane,
    readerHandles: new Map([[key, { gotoLine: vi.fn() }]]),
    tabs: { moveTab: vi.fn(), cycleTab: vi.fn(), restoreJumpEntry: vi.fn(), openFilesList: vi.fn(), reopenClosedTab: vi.fn() },
    requestCloseTab: vi.fn(), documents: { performSave, toggleLineEndings: vi.fn() },
    versions: { returnToCurrent: vi.fn(), stepVersion: vi.fn() },
    restoration: { runSelectedVersionRestore: vi.fn().mockResolvedValue(undefined), runFileRevert: vi.fn().mockResolvedValue(undefined), runEditorHunkReject: vi.fn().mockResolvedValue(undefined) },
    editorActions: { handoffSelectionToChat: vi.fn(), secretScreenReport: vi.fn(() => "Secret report"), openMarkSecretDialog: vi.fn(), buildEditorActionContext: () => null, abortEditorAction: vi.fn() },
    runSourceHistory: vi.fn().mockResolvedValue(undefined), runGoToDefinition: vi.fn().mockResolvedValue(undefined),
  } satisfies Parameters<typeof bindFilesCommands>[0];
  const tree = options.tree(); options.tree = () => tree;
  function Probe() { bindFilesCommands(options); return null; }
  render(() => <ResidentPresenceProvider presence={presence()}><Probe /></ResidentPresenceProvider>);
  return { ...f, key, options, tree, clicked, setPresence, setAimed: (next: string | null) => { aimed = next; } };
}

it("routes tab navigation and stops answering when retained", () => {
  const f = fixture(); invokeCommand("files.moveTabLeft"); invokeCommand("files.moveTabRight");
  expect(f.options.tabs.moveTab.mock.calls).toEqual([[-1], [1]]);
  invokeCommand("files.nextTab"); invokeCommand("files.prevTab"); expect(f.options.tabs.cycleTab.mock.calls).toEqual([[1], [-1]]);
  invokeCommand("files.openFilesList"); invokeCommand("files.reopenClosedTab");
  expect(f.options.tabs.openFilesList).toHaveBeenCalledOnce(); expect(f.options.tabs.reopenClosedTab).toHaveBeenCalledOnce();
  f.setPresence("idle"); expect(invokeCommand("files.nextTab")).toBe("missing");
  f.setPresence("active"); invokeCommand("files.nextTab"); expect(f.options.tabs.cycleTab).toHaveBeenCalledTimes(3);
});

it("routes editor, history, and surface actions to their owning controller", () => {
  const f = fixture(); const calls = [
    ["files.save", f.options.documents.performSave], ["files.previousVersion", f.options.versions.stepVersion],
    ["files.nextVersion", f.options.versions.stepVersion], ["files.undoLifecycle", f.options.runSourceHistory],
    ["files.redoLifecycle", f.options.runSourceHistory], ["files.addSelectionToChat", f.options.editorActions.handoffSelectionToChat],
    ["files.toggleTree", f.options.toggleFilesTree], ["files.goToDefinition", f.options.runGoToDefinition],
    ["files.markSecret", f.options.editorActions.openMarkSecretDialog], ["files.revertFileChanges", f.options.restoration.runFileRevert],
    ["files.rejectHunk", f.options.restoration.runEditorHunkReject], ["files.toggleLineEndings", f.options.documents.toggleLineEndings],
  ] as const;
  for (const [id, fn] of calls) { expect(invokeCommand(id)).toBe("ran"); expect(fn).toHaveBeenCalled(); }
  expect(f.options.runSourceHistory.mock.calls).toEqual([["undo"], ["redo"]]);
  expect(f.options.restoration.runFileRevert).toHaveBeenCalledWith("root", "file.ts");
  invokeCommand("files.showSecretScreen"); expect(f.options.scope.setSaveError).toHaveBeenCalledWith("Secret report");
  invokeCommand("editor.goToLine"); expect(f.options.readerHandles.get(f.key)!.gotoLine).toHaveBeenCalledOnce();
  invokeCommand("files.openVersionHistory"); expect(f.clicked).toHaveBeenCalledOnce();
});

it("routes tree creation, reveal, focus and comparison commands", async () => {
  const f = fixture(); invokeCommand("files.newFile"); invokeCommand("files.newFolder");
  expect(f.tree.startCreate.mock.calls).toEqual([["file", "root", "."], ["folder", "root", "."]]);
  invokeCommand("files.collapseAll"); invokeCommand("files.expandAll");
  expect(f.tree.collapseAll).toHaveBeenCalledOnce(); expect(f.tree.expandAll).toHaveBeenCalledOnce();
  invokeCommand("files.reveal"); expect(f.options.revealInTree).toHaveBeenCalledWith("root", "file.ts", false);
  invokeCommand("go.filesTree"); invokeCommand("go.filesTabs"); await Promise.resolve();
  expect(focusRegion).toHaveBeenCalledWith("filesTree"); expect(focusRegion).toHaveBeenCalledWith("filesTabs");
  invokeCommand("files.chooseComparison"); expect(requestScopePicker).toHaveBeenCalledWith(f.projectId);
  invokeCommand("files.openAllDiffs"); expect(openFilesSurface).toHaveBeenCalledWith({ kind: "diffs", projectId: f.projectId, address: { kind: "lens" } });
  invokeCommand("files.toggleComparison"); expect(toggleComparison).toHaveBeenCalledWith(f.projectId);
  invokeCommand("files.toggleDeletedLines"); expect(toggleDeletedLines).toHaveBeenCalledWith(f.projectId);
  invokeCommand("files.comparisonNext"); invokeCommand("files.comparisonPrev");
  expect(vi.mocked(cycleSidebarScope).mock.calls.map(call => call[1])).toEqual([1, -1]);
});

it("saves dirty tabs before close and delegates an empty close to the window", async () => {
  const f = fixture(); applyFilesBufferDraft(f.projectId, f.key, "draft"); invokeCommand("files.saveAndClose");
  await vi.waitFor(() => expect(f.options.requestCloseTab).toHaveBeenCalledWith(f.key));
  expect(f.options.documents.performSave).toHaveBeenCalledWith(f.key);
  f.options.documents.performSave.mockClear(); invokeCommand("files.saveAndClose"); expect(f.options.documents.performSave).not.toHaveBeenCalled();
  invokeCommand("files.closeTab"); f.setAimed(null); invokeCommand("files.closeTab"); expect(f.options.onCloseTabIdle).toHaveBeenCalledOnce();
  invokeCommand("files.saveAndClose"); expect(f.options.documents.performSave).not.toHaveBeenCalled();
  cleanup(); expect(f.options.editorActions.abortEditorAction).toHaveBeenCalledOnce();
});

it("updates editor preferences through command handlers", () => {
  fixture(); invokeCommand("editor.toggleWrap"); invokeCommand("editor.toggleLineNumbers"); invokeCommand("editor.toggleTabFocus");
  expect(saveEditorWordWrap).toHaveBeenCalledOnce(); expect(saveEditorLineNumbers).toHaveBeenCalledOnce(); expect(saveEditorTabMovesFocus).toHaveBeenCalledOnce();
});
