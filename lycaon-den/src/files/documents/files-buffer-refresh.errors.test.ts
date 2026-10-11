// @vitest-environment jsdom
import { produce } from "solid-js/store";
import { afterEach, expect, it, vi } from "vitest";
import { filesControllerFixture, controllerSource } from "../../test/files-controller-fixture.ts";
import { createBufferRefresh } from "./files-buffer-refresh.ts";
import { editorReplica, evictEditorDocument, observeEditorDocument } from "./editor-document.ts";
import { setFilesStore } from "./files-buffer-state.ts";
import { filesBufferText, resetProjectFilesForTests } from "./project-files-buffers.ts";
vi.mock("./editor-document.ts", async original => ({ ...await original<typeof import("./editor-document.ts")>(),
  editorReplica: vi.fn(), evictEditorDocument: vi.fn(), observeEditorDocument: vi.fn() }));
afterEach(() => { resetProjectFilesForTests(); vi.clearAllMocks(); });

it.each(["observe", "evict"] as const)("reports current document %s failure without losing its identity", async boundary => {
  const f = filesControllerFixture(`refresh-${boundary}`); const key = f.open();
  setFilesStore(f.projectId, produce(state => { state.byKey[key]!.documentId = "document"; }));
  const error = new Error(`${boundary} failed`); const setSaveError = vi.fn();
  vi.mocked(editorReplica).mockReturnValue(boundary === "observe" ? {} as NonNullable<ReturnType<typeof editorReplica>> : undefined);
  vi.mocked(observeEditorDocument).mockRejectedValue(error); vi.mocked(evictEditorDocument).mockRejectedValue(error);
  vi.mocked(f.client.getProjectSource).mockResolvedValue(controllerSource("changed", { binary: boundary === "evict" }));
  const refresh = createBufferRefresh({ ...f.scope, dropFileVersionState: vi.fn(), loadVersionHistory: vi.fn(), setSaveError,
    versionForBuffer: () => null, reloadEditorConfig: vi.fn(), flashUpdatedNote: vi.fn(), shouldApply: () => true });
  refresh(f.state().byKey[key]!);
  await vi.waitFor(() => expect(setSaveError).toHaveBeenCalledWith(expect.stringContaining(`${boundary} failed`)));
  if (boundary === "observe") { expect(observeEditorDocument).toHaveBeenCalledWith(f.projectId, f.state().byKey[key]); expect(filesBufferText(f.state().byKey[key]!)).toBe("original"); }
  else expect(evictEditorDocument).toHaveBeenCalledWith("document");
});
