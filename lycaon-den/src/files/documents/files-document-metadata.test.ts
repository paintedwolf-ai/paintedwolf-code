// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { documentOutbox } from "./document-outbox.ts";
import { createFileDocumentMetadata, refreshFileDocumentMetadata } from "./files-document-metadata.ts";
import { markFilesBufferTyping, openFilesBuffer, resetProjectFilesForTests } from "./project-files-buffers.ts";
import { projectFilesState, suspendFilesBufferContent } from "./files-buffer-state.ts";
import { preserveEditorDocumentDraft, resetEditorDocumentsForTests } from "./editor-document.ts";
import { required } from "../../test/at.ts";

const project = "project-1";
let dispose = () => {};
beforeEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });
afterEach(() => { dispose(); resetEditorDocumentsForTests(); resetProjectFilesForTests(); });

it("refreshes hundreds of restored descriptors in bounded metadata batches without recovery bodies", async () => {
  const documents = Array.from({ length: 150 }, (_, index) => new DocumentFixture("", { id: `document-${index}`, path: `${index}.txt`, dirty: index === 0 }).snapshot());
  for (const document of documents) openFilesBuffer(project, { rootId: document.root_id, rootLabel: "Root", path: document.path, documentId: document.id, intent: "permanent" });
  const read = vi.fn(async (_project: string, ids: string[]) => ({ documents: documents.filter(document => ids.includes(document.id)), missing: [] }));
  const client = stubClient({ readEditorDocumentStatuses: read, replaceEditorDocumentRetention: async () => undefined });
  const body = vi.spyOn(documentOutbox(), "read");
  createRoot(stop => { dispose = stop; createFileDocumentMetadata(project, () => client); });
  await refreshFileDocumentMetadata(project);
  expect(read.mock.calls.map(call => call[1].length)).toEqual([64, 64, 22]);
  expect(body).not.toHaveBeenCalled();
  const buffers = Object.values(projectFilesState(project).byKey);
  expect(buffers.every(buffer => buffer.content.state === "unloaded")).toBe(true);
  const dirty = required(buffers.find(buffer => buffer.path === "0.txt"));
  expect(dirty.dirty).toBe(true);
  expect(buffers.filter(buffer => buffer.dirty)).toHaveLength(1);
  await expect(preserveEditorDocumentDraft(dirty)).resolves.toBe(true);
});

it("keeps a missing cold document addressable without dropping its recovery identity", async () => {
  const key = openFilesBuffer(project, { rootId: "root-1", rootLabel: "Root", path: "gone.txt", documentId: "gone", intent: "permanent" });
  const client = stubClient({ readEditorDocumentStatuses: async () => ({ documents: [], missing: ["gone"] }), replaceEditorDocumentRetention: async () => undefined });
  createRoot(stop => { dispose = stop; createFileDocumentMetadata(project, () => client); });
  await refreshFileDocumentMetadata(project);
  expect(projectFilesState(project).byKey[key]).toMatchObject({ documentId: "gone", content: { state: "unloaded" }, loadError: expect.stringContaining("no longer available") });
});

it.each([false, true])("ignores stale metadata after a tab changes during the request (missing: %s)", async missing => {
  const document = new DocumentFixture("saved").snapshot();
  const key = openFilesBuffer(project, { rootId: document.root_id, rootLabel: "Root", path: document.path, documentId: document.id, intent: "permanent" });
  let complete!: () => void;
  const pending = new Promise<void>(resolve => { complete = resolve; });
  const read = vi.fn(async () => {
    await pending;
    return { documents: missing ? [] : [document], missing: missing ? [document.id] : [] };
  });
  const client = stubClient({ readEditorDocumentStatuses: read, replaceEditorDocumentRetention: async () => undefined });
  createRoot(stop => { dispose = stop; createFileDocumentMetadata(project, () => client); });
  const refresh = refreshFileDocumentMetadata(project);
  await vi.waitFor(() => expect(read).toHaveBeenCalledOnce());
  markFilesBufferTyping(project, key);
  suspendFilesBufferContent(project, key);
  complete();
  await refresh;
  expect(projectFilesState(project).byKey[key]).toMatchObject({ dirty: true, content: { state: "suspended" } });
  expect(projectFilesState(project).byKey[key]?.loadError).toBeFalsy();
});

it("publishes retained tabs when an offline window reconnects", async () => {
  const publish = vi.fn(async () => undefined);
  openFilesBuffer(project, { rootId: "root-1", rootLabel: "Root", path: "a.txt", documentId: "document-1", intent: "permanent" });
  const [client, setClient] = createSignal<ReturnType<typeof stubClient> | null>(null);
  createRoot(stop => { dispose = stop; createFileDocumentMetadata(project, client); });
  expect(publish).not.toHaveBeenCalled();
  setClient(stubClient({ replaceEditorDocumentRetention: publish }));
  await vi.waitFor(() => expect(publish).toHaveBeenCalledOnce());
  expect(publish.mock.calls[0]).toEqual([project, expect.any(String), ["document-1"], undefined]);
});
