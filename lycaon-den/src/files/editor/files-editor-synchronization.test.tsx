// @vitest-environment jsdom
import { render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { EditorDocumentState } from "../documents/editor-document.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { createFilesEditorSynchronization } from "./files-editor-synchronization.ts";

const fixture = vi.hoisted(() => ({
  buffer: {} as FileBuffer,
  live: "before",
  listener: (_document: EditorDocumentState) => {},
  open: vi.fn(),
  evict: vi.fn(async (_id: string) => true),
}));
vi.mock("../documents/files-document-metadata.ts", () => ({ createFileDocumentMetadata: () => {}, ensureFileDocumentRetention: async () => {} }));
vi.mock("../documents/editor-document.ts", () => ({
  configureEditorDocuments: () => () => {},
  editorReplica: () => undefined,
  resolveEditorDocument: (...args: unknown[]) => fixture.open(...args),
  evictEditorDocument: (id: string) => fixture.evict(id),
  subscribeEditorDocuments: (listener: typeof fixture.listener) => {
    fixture.listener = listener;
    return () => {};
  },
}));
vi.mock("./files-editor-host.ts", () => ({
  getFilesEditorView: () => ({ state: { doc: { toString: () => fixture.live } } }),
  setFilesEditorViewDoc: vi.fn(),
}));
vi.mock("./files-editor-secret-screen.ts", () => ({ setFilesEditorSecretScreen: vi.fn() }));
vi.mock("../components/project-files-model.ts", () => ({ fileBufferKey: () => "file" }));
vi.mock("../documents/files-buffer-state.ts", () => ({
  projectFilesState: () => ({ order: ["file"], activeKey: "file", byKey: { file: fixture.buffer } }),
}));
vi.mock("../documents/project-files-buffers.ts", () => ({
  setFilesBufferDocumentOpening: (_project: string, _key: string, state: FileBuffer["editorOpening"]) => { fixture.buffer.editorOpening = state; },
  applyFilesBufferEditorDocument: (_project: string, _key: string, document: EditorDocumentState) => {
    Object.assign(fixture.buffer, {
      content: { state: "document", generation: document.localGeneration }, dirty: document.dirty,
      documentRevision: document.revision, documentId: document.documentId,
    });
  },
}));

beforeEach(() => {
  fixture.buffer = { key: "file", path: "a.txt", rootId: "root", kind: "text", loading: true, content: { state: "source", text: "before", base: "before" }, encoding: "utf-8", baseSha256: "hash", writable: true } as FileBuffer;
  fixture.live = "before";
  fixture.open.mockResolvedValue(null);
});
afterEach(() => vi.clearAllMocks());

function snapshot(workspaceId = "workspace"): EditorDocumentState {
  return { projectId: "project", workspaceId, documentId: "document", rootId: "root", path: "a.txt",
    localGeneration: 2, dirty: true, revision: 2 } as EditorDocumentState;
}

describe("collaborative editor synchronization", () => {
  it("preserves live replica text when accepted metadata arrives behind local typing", () => {
    fixture.buffer.documentId = "document";
    fixture.live = "accepted and local typing";
    const updated = vi.fn();
    const rendered = render(() => {
      createFilesEditorSynchronization({ projectId: "project", client: () => null, sessionId: () => undefined,
        workspaceId: () => "workspace", workspaceSettled: () => true, onUpdated: updated });
      return <div />;
    });
    fixture.listener(snapshot());
    expect(fixture.buffer.content).toEqual({ state: "document", generation: 2 });
    expect(fixture.buffer).not.toHaveProperty("draft");
    expect(fixture.buffer.dirty).toBe(true);
    expect(updated).toHaveBeenCalledOnce();
    rendered.unmount();
  });

  it("ignores updates for the same logical path in another physical workspace", () => {
    fixture.buffer.documentId = "document";
    const updated = vi.fn();
    const rendered = render(() => {
      createFilesEditorSynchronization({ projectId: "project", client: () => null, sessionId: () => undefined,
        workspaceId: () => "workspace", workspaceSettled: () => true, onUpdated: updated });
      return <div />;
    });
    fixture.listener({ ...snapshot("other-workspace"), documentId: "another-document" });
    expect(fixture.buffer.content).toEqual({ state: "source", text: "before", base: "before" });
    expect(updated).not.toHaveBeenCalled();
    rendered.unmount();
  });

  it("accepts shared-root updates across workspace views when the document identity matches", () => {
    fixture.buffer.documentId = "document";
    fixture.live = "shared root text";
    const updated = vi.fn();
    const rendered = render(() => {
      createFilesEditorSynchronization({ projectId: "project", client: () => null, sessionId: () => undefined,
        workspaceId: () => "workspace", workspaceSettled: () => true, onUpdated: updated });
      return <div />;
    });
    fixture.listener(snapshot("other-workspace"));
    expect(fixture.buffer.content).toEqual({ state: "document", generation: 2 });
    expect(updated).toHaveBeenCalledOnce();
    rendered.unmount();
  });

  it("retires an opening document when the workspace changes before it arrives", async () => {
    fixture.buffer.loading = false;
    let complete!: (document: EditorDocumentState) => void;
    fixture.open.mockImplementationOnce(() => new Promise<EditorDocumentState>((resolve) => { complete = resolve; }));
    const [workspace, setWorkspace] = createSignal("workspace");
    const client = {} as LycaonClient;
    const rendered = render(() => {
      createFilesEditorSynchronization({ projectId: "project", client: () => client, sessionId: () => undefined,
        workspaceId: workspace, workspaceSettled: () => true, onUpdated: vi.fn() });
      return <div />;
    });
    await waitFor(() => expect(fixture.open).toHaveBeenCalledOnce());
    setWorkspace("other-workspace");
    complete(snapshot());
    await waitFor(() => expect(fixture.evict).toHaveBeenCalledWith("document"));
    expect(fixture.buffer.documentId).toBeUndefined();
    expect(fixture.buffer.content).toEqual({ state: "source", text: "before", base: "before" });
    rendered.unmount();
  });
});
