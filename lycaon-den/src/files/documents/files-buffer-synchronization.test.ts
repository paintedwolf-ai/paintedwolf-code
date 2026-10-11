// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { produce } from "solid-js/store";
import { afterEach, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { ProjectSourceReadResponse, SourceChange } from "../../api/types.ts";
import { applyBufferSourceChanged, setBufferLiveHandlers } from "./buffer-live.ts";
import { bindFilesBufferSynchronization } from "./files-buffer-synchronization.ts";
import { observeEditorDocument } from "./editor-document.ts";
import { projectFilesState, setFilesStore } from "./files-buffer-state.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, filesBufferText, openFilesBuffer, resetProjectFilesForTests } from "./project-files-buffers.ts";

vi.mock("./editor-document.ts", async (original) => ({
  ...await original<typeof import("./editor-document.ts")>(),
  observeEditorDocument: vi.fn(),
}));

type ObservedDocument = Awaited<ReturnType<typeof observeEditorDocument>>;

const projectId = "binding-project";
const disposers: Array<() => void> = [];
const change: SourceChange = { root_id: "root", path: "file.ts", op: "write", origin: "agent", changed_at: "2026-10-11T00:00:00Z" };
function source(content: string): ProjectSourceReadResponse {
  return { file_id: "", version_id: "version", workspace_id: "workspace", workspace_kind: "project",
    path: "file.ts", content, over_limit: false, writable: true, binary: false,
    size_bytes: content.length, sha256: content } as ProjectSourceReadResponse;
}
function open(path = "file.ts") {
  const key = openFilesBuffer(projectId, { rootId: "root", rootLabel: "Root", path, intent: "permanent" });
  applyFilesBufferLoad(projectId, key, { ...source("original"), path });
  return key;
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
function bind(read = vi.fn().mockResolvedValue(source("current"))) {
  const client = stubClient({ getProjectSource: read });
  const stop = createRoot((dispose) => {
    bindFilesBufferSynchronization({
      projectId: () => projectId, client: () => client, sourceSessionId: () => undefined,
      state: () => projectFilesState(projectId), versionForBuffer: () => null,
      dropFileVersionState: vi.fn(), loadVersionHistory: vi.fn(), setSaveError: vi.fn(),
      flashUpdatedNote: vi.fn(), setBulkClose: vi.fn(), setConfirm: vi.fn(),
    });
    return dispose;
  });
  disposers.push(stop);
  return stop;
}
const emit = () => applyBufferSourceChanged(projectId, "project", change);
async function settle() { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); }

afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose();
  setBufferLiveHandlers(projectId, null);
  resetProjectFilesForTests();
  vi.clearAllMocks();
});

it("an older registration cannot remove its replacement", () => {
  open();
  const first = vi.fn();
  const second = vi.fn();
  const handlers = (onCleanDiskChange: typeof first) => ({ onCleanDiskChange, onDirtyDiskChange: vi.fn(), onForeignDeleteDirty: vi.fn() });
  const a = setBufferLiveHandlers(projectId, handlers(first));
  const b = setBufferLiveHandlers(projectId, handlers(second));
  a.dispose();
  expect(a.isCurrent()).toBe(false);
  expect(b.isCurrent()).toBe(true);
  emit();
  expect(first).not.toHaveBeenCalled();
  expect(second).toHaveBeenCalledOnce();
  b.dispose();
  emit();
  expect(second).toHaveBeenCalledOnce();
});

it.each(["success", "failure"] as const)("ignores a delayed source %s after replacement", async (outcome) => {
  const key = open();
  const pending = deferred<ProjectSourceReadResponse>();
  const readOld = vi.fn().mockReturnValue(pending.promise);
  const disposeOld = bind(readOld);
  emit();
  expect(readOld).toHaveBeenCalledOnce();
  const readCurrent = vi.fn().mockResolvedValue(source("current"));
  bind(readCurrent);
  disposeOld();
  if (outcome === "success") pending.resolve(source("stale"));
  else pending.reject(new Error("obsolete source failure"));
  await settle();
  expect(filesBufferText(projectFilesState(projectId).byKey[key]!)).toBe("original");
  expect(projectFilesState(projectId).byKey[key]!.loadError).toBeNull();
  emit();
  await vi.waitFor(() => expect(filesBufferText(projectFilesState(projectId).byKey[key]!)).toBe("current"));
  expect(readCurrent).toHaveBeenCalledOnce();
});

it.each(["replacement", "disposal"] as const)("ignores document results after %s", async (retirement) => {
  const key = open();
  setFilesStore(projectId, produce((state) => { state.byKey[key]!.documentId = "document"; }));
  const pending = deferred<ObservedDocument>();
  vi.mocked(observeEditorDocument).mockReturnValueOnce(pending.promise);
  const readOld = vi.fn();
  const dispose = bind(readOld);
  emit();
  expect(observeEditorDocument).toHaveBeenCalledOnce();
  if (retirement === "replacement") bind();
  else dispose();
  pending.resolve({ diverged: true } as ObservedDocument);
  await settle();
  expect(readOld).not.toHaveBeenCalled();
  expect(projectFilesState(projectId).byKey[key]!.loadError).toBeNull();
});

it("ignores a retired document failure and dirty-file reveal", async () => {
  const key = open();
  setFilesStore(projectId, produce((state) => { state.byKey[key]!.documentId = "document"; }));
  const failed = deferred<ObservedDocument>();
  vi.mocked(observeEditorDocument).mockReturnValueOnce(failed.promise);
  const dispose = bind();
  emit();
  bind();
  dispose();
  failed.reject(new Error("obsolete document failure"));
  await settle();
  expect(projectFilesState(projectId).byKey[key]!.loadError).toBeNull();

  applyFilesBufferDraft(projectId, key, "draft");
  const selected = open("other.ts");
  const dirty = deferred<ObservedDocument>();
  vi.mocked(observeEditorDocument).mockReturnValueOnce(dirty.promise);
  emit();
  bind();
  dirty.resolve({ diverged: true } as ObservedDocument);
  await settle();
  expect(projectFilesState(projectId).activeKey).toBe(selected);
});
