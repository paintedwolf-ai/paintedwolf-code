// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { filesControllerFixture } from "../../test/files-controller-fixture.ts";
import { createFilesTreeInteraction } from "./files-tree-interaction.ts";
import { applyFilesBufferDraft, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import type { FilesTreeHandle } from "./files-tree-context.ts";
const history = vi.hoisted(() => ({ noteLifecycleChange: vi.fn(), refreshSourceHistory: vi.fn().mockResolvedValue(undefined), runSourceHistory: vi.fn(), reportHistoryFailure: vi.fn() }));
vi.mock("../history/project-files-history.ts", () => ({ createSourceHistoryCommands: () => history }));
const disposers: Array<() => void> = [];
afterEach(() => { disposers.splice(0).forEach(dispose => dispose()); resetProjectFilesForTests(); vi.clearAllMocks(); });

function fixture() {
  const f = filesControllerFixture("tree-interaction");
  const browse = vi.fn().mockResolvedValue({ entries: [] }); const create = vi.fn().mockImplementation(async (_project, request: { path: string }) => ({ path: request.path }));
  Object.assign(f.client, { browseProjectSource: browse, createProjectSourceEntry: create });
  const [settled, setSettled] = createSignal(false);
  const stage = document.createElement("div");
  const options = {
    props: { projectId: f.projectId }, scope: { ...f.scope, lifecycleHooks: () => ({
      guardDirtyUnderPath: async () => "proceed" as const, confirmChange: vi.fn(), openFile: vi.fn(), rootLabelFor: () => "Root" }), copyAbsolutePathRef: vi.fn(), copyRelativePathRef: vi.fn() },
    directoryEntries: () => [], confirmDirectoryChange: vi.fn(), observeDirectory: vi.fn(() => vi.fn()),
    workspaceSettled: settled, filesWorkspaceId: () => "workspace", mountedTreeWorkspace: () => "workspace", scopeMarksVersion: () => 0,
    stageEl: () => stage, showFilesTree: vi.fn(), setDeleteGuard: vi.fn(), requestCloseTab: vi.fn(), requestCloseFamily: vi.fn(), openFilesList: vi.fn(),
    editorContextActions: () => ({ editorSymbol: { target: { kind: "missing" as const, reason: "range" as const }, rename: vi.fn(), goToDefinition: vi.fn() } }), openReviewRowFile: vi.fn(), runFileRevert: vi.fn().mockResolvedValue(undefined),
  } satisfies Parameters<typeof createFilesTreeInteraction>[0];
  const interaction = createRoot(dispose => { disposers.push(dispose); return createFilesTreeInteraction(options); });
  const tree = { startCreate: vi.fn(), confirmChange: vi.fn(), revealPath: vi.fn(), collapseAll: vi.fn(), expandAll: vi.fn() } satisfies FilesTreeHandle;
  interaction.setTreeHandle(tree);
  return { ...f, options, interaction, tree, setSettled, browse, create, stage };
}

it("resolves creation targets from folders, files, and the project root", () => {
  const f = fixture(); expect(f.interaction.createTargetDir()).toEqual({ rootId: "root", dir: "." });
  f.interaction.setSelectedEntry({ rootId: "root", path: "src", kind: "folder" });
  expect(f.interaction.createTargetDir()).toEqual({ rootId: "root", dir: "src" });
  f.interaction.setSelectedEntry({ rootId: "root", path: "src/file.ts", kind: "file" });
  expect(f.interaction.createTargetDir()).toEqual({ rootId: "root", dir: "src" });
  f.interaction.setSelectedEntry({ rootId: "root", path: "file.ts", kind: "file" });
  expect(f.interaction.createTargetDir()).toEqual({ rootId: "root", dir: "." });
});

it("defers explicit reveal until the matching workspace settles", () => {
  const f = fixture(); f.interaction.setFilterQuery("stale filter"); f.interaction.revealInTree(" ", "src/file.ts");
  expect(f.options.showFilesTree).toHaveBeenCalledOnce(); expect(f.interaction.filterQuery()).toBe("");
  expect(f.tree.revealPath).not.toHaveBeenCalled(); f.setSettled(true);
  expect(f.tree.revealPath).toHaveBeenCalledWith("root", "src/file.ts", false);
  expect(f.interaction.explicitTreeReveal()?.path).toBe("src/file.ts");
  f.interaction.revealInTree("root", "src", true); expect(f.interaction.explicitTreeReveal()).toBeNull();
});

it("observes selected directories only after settlement and releases them", () => {
  const f = fixture(); f.interaction.setSelectedEntry({ rootId: "root", path: "src", kind: "folder" });
  expect(f.options.observeDirectory).not.toHaveBeenCalled(); f.setSettled(true);
  expect(f.options.observeDirectory).toHaveBeenCalledWith("root", "src");
  const stop = f.options.observeDirectory.mock.results[0]!.value;
  f.interaction.setSelectedEntry({ rootId: "root", path: "file.ts", kind: "file" }); expect(stop).toHaveBeenCalledOnce();
});

it("uses project and chat addresses for browse and create, then records lifecycle history", async () => {
  const f = fixture(); await f.interaction.browse("root", "src");
  expect(f.browse).toHaveBeenCalledWith(f.projectId, { rootId: "root", dir: "src" }, "chat");
  expect(await f.interaction.createEntry("file", "root", "src/new.ts")).toBe("src/new.ts");
  expect(await f.interaction.createEntry("folder", "root", "src/new")).toBe("src/new");
  expect(f.create).toHaveBeenCalledWith(f.projectId, expect.objectContaining({ root_id: "root", path: "src/new.ts", kind: "file" }), "chat");
  expect(history.noteLifecycleChange.mock.calls).toEqual([["Created file new.ts"], ["Created folder new"]]);
});

it("guards dirty paths and projects confirmed changes into the tree", async () => {
  const f = fixture(); const key = f.open(); applyFilesBufferDraft(f.projectId, key, "draft");
  const hooks = f.interaction.lifecycleHooks(); expect(await hooks.guardDirtyUnderPath("root", "other.ts")).toBe("proceed");
  const pending = hooks.guardDirtyUnderPath("root", "file.ts"); const guard = f.options.setDeleteGuard.mock.calls[0]![0];
  expect(guard).toMatchObject({ rootId: "root", path: "file.ts" }); guard.resolve("cancel"); expect(await pending).toBe("cancel");
  const change = { root_id: "root", path: "file.ts", op: "write" as const, origin: "agent" as const, changed_at: "2026-10-11T00:00:00Z" };
  hooks.confirmChange(change); expect(f.tree.confirmChange).toHaveBeenCalledWith(change); expect(f.options.confirmDirectoryChange).toHaveBeenCalledWith(change);
  const opened = hooks.openFile({ rootId: "root", rootLabel: "Root", path: "new.ts" }); expect(f.state().byKey[opened]?.path).toBe("new.ts");
});

it("opens selected files and builds tab menus from current buffer identities", () => {
  const f = fixture(); f.interaction.openFile({ rootId: "root", rootLabel: "Root", path: "src/file.ts" }, "permanent");
  const key = f.state().activeKey!; expect(f.interaction.selectedEntry()).toEqual({ rootId: "root", path: "src/file.ts", kind: "file" });
  f.interaction.openTabContextMenu(key, { x: 12, y: 20 }); expect(f.interaction.ctxMenu()?.items.length).toBeGreaterThan(0);
  f.interaction.setCtxMenu(null); f.interaction.openTabContextMenu("missing", { x: 12, y: 20 }); expect(f.interaction.ctxMenu()).toBeNull();
});
