// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import type { EditorView } from "@codemirror/view";
import type { ProjectFileDocumentSession } from "../components/project-files-view-props.ts";
import { filesControllerFixture } from "../../test/files-controller-fixture.ts";
import { bindFilesEditorSurface } from "./files-editor-surface.ts";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { inlineEditOpen } from "../../components/source/inline-edit/inline-edit-controller.ts";
import { clearScopePreview } from "../../components/source/editor/codemirror-theme.ts";
import { getFilesEditorView } from "./files-editor-host.ts";
import { applyFilesBufferDraft, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
vi.mock("../../find/use-findable-view.ts", () => ({ bindFindableView: vi.fn() }));
vi.mock("../../components/source/inline-edit/inline-edit-controller.ts", () => ({ inlineEditOpen: vi.fn() }));
vi.mock("../../components/source/editor/codemirror-theme.ts", async original => ({ ...await original<typeof import("../../components/source/editor/codemirror-theme.ts")>(), clearScopePreview: vi.fn() }));
vi.mock("./files-editor-host.ts", async original => ({ ...await original<typeof import("./files-editor-host.ts")>(), getFilesEditorView: vi.fn() }));
const disposers: Array<() => void> = [];
afterEach(() => { disposers.splice(0).forEach(dispose => dispose()); resetProjectFilesForTests(); vi.clearAllMocks(); });

it("publishes a file-window save session and retires it with the surface", async () => {
  const f = filesControllerFixture("editor-session"); const key = f.open();
  const onSessionChange = vi.fn(); const performSave = vi.fn().mockResolvedValue(true);
  const dispose = createRoot(stop => {
    bindFilesEditorSurface({ ...f.scope, projectId: f.projectId, editorPaneEl: () => undefined,
      document: () => ({ rootId: "root", path: " file.ts ", onSessionChange }), performSave }); return stop;
  }); disposers.push(dispose);
  const session = onSessionChange.mock.calls[0]![0] as ProjectFileDocumentSession;
  expect(await session.save()).toBe(true); expect(performSave).not.toHaveBeenCalled();
  applyFilesBufferDraft(f.projectId, key, "draft"); expect(await session.save()).toBe(true);
  expect(performSave).toHaveBeenCalledWith(key);
  dispose(); expect(onSessionChange).toHaveBeenLastCalledWith(null);
});

it("clears inline preview on close and binds find to the presented buffer", () => {
  const f = filesControllerFixture("editor-find"); const key = f.open();
  const view = {} as EditorView; vi.mocked(getFilesEditorView).mockReturnValue(view);
  const [opened, setOpened] = createSignal<ReturnType<typeof inlineEditOpen>>(null);
  vi.mocked(inlineEditOpen).mockImplementation(opened);
  const pane = document.createElement("div");
  disposers.push(createRoot(dispose => {
    bindFilesEditorSurface({ ...f.scope, projectId: f.projectId, editorPaneEl: () => pane, document: () => undefined, performSave: vi.fn() }); return dispose;
  }));
  const find = vi.mocked(bindFindableView).mock.calls[0]![0];
  expect(find.enabled?.()).toBe(true); expect(find.root()).toBe(pane); expect(find.getEditorView?.()).toBe(view);
  setOpened({ projectId: f.projectId, rootId: "root", path: "file.ts", bufferKey: key, mode: { kind: "inline" }, scope: { startLine: 1, endLine: 1, kind: "selection" }, anchor: { top: 0, left: 0, width: 10 } }); setOpened(null);
  expect(clearScopePreview).toHaveBeenCalledWith(view); expect(getFilesEditorView).toHaveBeenCalledWith(f.projectId, key);
});

it("does not publish sessions for missing roots or blank document paths", () => {
  const f = filesControllerFixture("editor-missing"); const onSessionChange = vi.fn();
  disposers.push(createRoot(dispose => {
    bindFilesEditorSurface({ ...f.scope, projectId: f.projectId, editorPaneEl: () => undefined,
      document: () => ({ rootId: "missing", path: "file.ts", onSessionChange }), performSave: vi.fn() }); return dispose;
  }));
  expect(onSessionChange).not.toHaveBeenCalled();
  const find = vi.mocked(bindFindableView).mock.calls[0]![0];
  expect(find.enabled?.()).toBe(false); expect(find.getEditorView?.()).toBeNull();
});
