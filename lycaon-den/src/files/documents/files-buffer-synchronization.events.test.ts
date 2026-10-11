// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { produce } from "solid-js/store";
import { afterEach, expect, it, vi } from "vitest";
import { filesControllerFixture, controllerSource } from "../../test/files-controller-fixture.ts";
import { applyBufferSourceChanged, setBufferLiveHandlers } from "./buffer-live.ts";
import { bindFilesBufferSynchronization } from "./files-buffer-synchronization.ts";
import { observeEditorDocument } from "./editor-document.ts";
import { refreshOpenEditorConfigs, loadEditorConfigForBuffer } from "../../components/source/editor/editorconfig-load.ts";
import { setFilesStore } from "./files-buffer-state.ts";
import { applyFilesBufferDraft, resetProjectFilesForTests } from "./project-files-buffers.ts";
vi.mock("./editor-document.ts", async (original) => ({ ...await original<typeof import("./editor-document.ts")>(), observeEditorDocument: vi.fn() }));
vi.mock("../../components/source/editor/editorconfig-load.ts", () => ({ refreshOpenEditorConfigs: vi.fn(), loadEditorConfigForBuffer: vi.fn() }));
const disposers: Array<() => void> = [];
afterEach(() => { disposers.splice(0).forEach(dispose => dispose()); resetProjectFilesForTests(); vi.clearAllMocks(); });

function fixture() {
  const f = filesControllerFixture("synchronization-events"); const key = f.open();
  const setBulkClose = vi.fn(); const setConfirm = vi.fn();
  vi.mocked(f.client.getProjectSource).mockResolvedValue(controllerSource("refreshed"));
  const dispose = createRoot(stop => {
    bindFilesBufferSynchronization({ ...f.scope, versionForBuffer: () => null,
      dropFileVersionState: vi.fn(), loadVersionHistory: vi.fn(), setSaveError: vi.fn(), flashUpdatedNote: vi.fn(), setBulkClose, setConfirm });
    return stop;
  }); disposers.push(dispose);
  const emit = (path = "file.ts", op: "write" | "delete" = "write") => applyBufferSourceChanged(f.projectId, "project",
    { root_id: "root", path, op, origin: "agent", changed_at: "2026-10-11T00:00:00Z" });
  return { ...f, key, emit, setBulkClose, setConfirm, dispose };
}

it("refreshes EditorConfig and clean source presentation through live events", async () => {
  const f = fixture(); f.emit(".editorconfig");
  expect(vi.mocked(refreshOpenEditorConfigs).mock.calls[0]![0].client).toBe(f.client);
  expect(refreshOpenEditorConfigs).toHaveBeenCalledWith(expect.objectContaining({ projectId: f.projectId, rootId: "root", sessionId: "chat" }));
  f.emit(); await vi.waitFor(() => expect(loadEditorConfigForBuffer).toHaveBeenCalled());
  expect(f.state().byKey[f.key]!.baseSha256).toBe("refreshed");
  f.dispose(); f.emit(".editorconfig"); expect(refreshOpenEditorConfigs).toHaveBeenCalledOnce();
});

it.each(["diverged", "failed"] as const)("handles a current clean document that %s", async outcome => {
  const f = fixture(); setFilesStore(f.projectId, produce(state => { state.byKey[f.key]!.documentId = "document"; }));
  if (outcome === "diverged") vi.mocked(observeEditorDocument).mockResolvedValueOnce({ diverged: true } as Awaited<ReturnType<typeof observeEditorDocument>>);
  else vi.mocked(observeEditorDocument).mockRejectedValueOnce(new Error("read failed"));
  f.emit();
  await vi.waitFor(() => {
    if (outcome === "diverged") expect(f.client.getProjectSource).toHaveBeenCalledOnce();
    else expect(f.state().byKey[f.key]!.loadError).toBeTruthy();
  });
});

it("reveals a dirty diverged buffer and asks before a foreign deletion", async () => {
  const f = fixture(); applyFilesBufferDraft(f.projectId, f.key, "draft");
  const other = f.open("other.ts"); expect(f.state().activeKey).toBe(other);
  vi.mocked(observeEditorDocument).mockResolvedValueOnce({ diverged: true } as Awaited<ReturnType<typeof observeEditorDocument>>);
  f.emit(); await vi.waitFor(() => expect(f.state().activeKey).toBe(f.key));
  f.emit("file.ts", "delete");
  expect(f.setBulkClose).toHaveBeenCalledWith({ keys: [f.key], dirtyKeys: [f.key], foreignDelete: { rootId: "root", path: "file.ts" } });
  expect(f.setConfirm).toHaveBeenCalledWith({ intent: "bulk-close", fileNames: ["file.ts"] });
});

it("does not refresh EditorConfig without a connection", () => {
  const f = fixture(); f.dispose();
  const stop = createRoot(dispose => {
    bindFilesBufferSynchronization({ ...f.scope, client: () => null, versionForBuffer: () => null,
      dropFileVersionState: vi.fn(), loadVersionHistory: vi.fn(), setSaveError: vi.fn(), flashUpdatedNote: vi.fn(), setBulkClose: vi.fn(), setConfirm: vi.fn() });
    return dispose;
  }); disposers.push(stop); f.emit(".editorconfig");
  expect(refreshOpenEditorConfigs).not.toHaveBeenCalled();
  setBufferLiveHandlers(f.projectId, null);
});
