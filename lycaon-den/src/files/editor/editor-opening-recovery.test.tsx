// @vitest-environment jsdom
import { required } from "../../test/at.ts";
import "../../test/document-outbox-fixture.ts";
import { documentOutbox } from "../documents/document-outbox.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { createSignal } from "solid-js";
import { render, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { configureEditorDocuments, editorReplica, evictEditorDocument, preserveEditorDocumentDraft, resetEditorDocumentsForTests, resolveEditorDocument, subscribeEditorDocuments } from "../documents/editor-document.ts";
import { flushFilesHotExitToDisk } from "../documents/files-hot-exit.ts";
import { createFilesEditorSynchronization } from "./files-editor-synchronization.ts";
import { applyFilesBufferEditorDocument, applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

beforeEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });
afterEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });

function fixture() {
  const host = new DocumentFixture();
  const project = host.wire.project_id;
  let key = openFilesBuffer(project, { rootId: host.wire.root_id, rootLabel: "root", path: host.wire.path, intent: "permanent" });
  key = applyFilesBufferLoad(project, key, { file_id: host.wire.file_id, version_id: "", workspace_id: host.wire.workspace_id,
    workspace_kind: "project", root_id: host.wire.root_id, path: host.wire.path, content: "base", sha256: "base-sha",
    encoding: "utf-8", writable: true, over_limit: false, binary: false, size_bytes: 4 });
  const buffer = () => projectFilesState(project).byKey[key]!;
  const open = vi.fn<LycaonClient["openEditorDocument"]>(async () => host.snapshot());
  const client = stubClient({ openEditorDocument: open, createEditorDocumentSnapshot: async () => host.snapshot(),
    replaceEditorDocumentRetention: async () => undefined, syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
    leaveEditorDocument: async () => undefined, publishEditorDocumentPresence: async () => undefined });
  return { host, project, key, buffer, open, client };
}

it("recognizes a durably released draft when hot exit runs after its view closes", async () => {
  const f = fixture();
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: () => f.host.wire.workspace_id, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
  editorReplica(f.host.wire.id)!.replaceLocal("retained after leaving the view");
  await waitFor(() => expect(f.buffer().dirty).toBe(true));
  mounted.unmount();
  await waitFor(() => expect(editorReplica(f.host.wire.id)).toBeUndefined());

  await expect(preserveEditorDocumentDraft(f.buffer())).resolves.toBe(true);
  await expect(flushFilesHotExitToDisk()).resolves.toBeUndefined();
  await expect(preserveEditorDocumentDraft({ ...f.buffer(), editRevision: f.buffer().editRevision + 1 })).resolves.toBe(false);
  await evictEditorDocument(f.host.wire.id);
  await expect(preserveEditorDocumentDraft(f.buffer())).resolves.toBe(true);
});

it("suspends offline without waiting for host retention and keeps its dirty descriptor", async () => {
  const f = fixture();
  let client: LycaonClient | null = f.client;
  const disconnect = configureEditorDocuments(f.project, () => client, () => undefined, { get: f.buffer, all: () => [f.buffer()] });
  const unsubscribe = subscribeEditorDocuments(state => applyFilesBufferEditorDocument(f.project, f.key, state));
  const state = await resolveEditorDocument(f.project, f.buffer());
  expect(state).not.toBeNull();
  const replica = required(editorReplica(f.host.wire.id));
  replica.replaceLocal("durable while disconnected");
  await waitFor(() => expect(f.buffer().dirty).toBe(true));
  client = null;
  await expect(evictEditorDocument(f.host.wire.id, { retainRecovery: true })).resolves.toBe(true);
  expect(editorReplica(f.host.wire.id)).toBeUndefined();
  expect(f.buffer()).toMatchObject({ dirty: true, content: { state: "suspended" } });
  const records = await documentOutbox().read(f.host.wire.id);
  expect(records.some(record => record.kind === "checkpoint")).toBe(true);
  expect(records.some(record => record.kind === "update" && !record.acknowledged)).toBe(true);
  expect((await documentOutbox().list(f.project))[0]?.retainedClients).not.toHaveLength(0);
  unsubscribe();
  disconnect();
});

it("readmits a released tab through document acquisition", async () => {
  const f = fixture();
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: () => f.host.wire.workspace_id, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
  const previous = editorReplica(f.host.wire.id);
  await evictEditorDocument(f.host.wire.id);
  expect(f.buffer().content.state).toBe("suspended");
  expect(editorReplica(f.host.wire.id)).toBeUndefined();
  await resolveEditorDocument(f.project, f.buffer());
  await waitFor(() => { expect(editorReplica(f.host.wire.id)).toBeDefined(); expect(editorReplica(f.host.wire.id)).not.toBe(previous); });
  await waitFor(() => expect(f.buffer().editorOpening).toBeUndefined());
  expect(f.open).toHaveBeenCalledOnce();
  mounted.unmount();
});

it("reconciles the workspace before admitting an opening response from another checkout", async () => {
  const f = fixture();
  const [workspace, setWorkspace] = createSignal("old-workspace");
  const refresh = vi.fn((next: string) => { setWorkspace(next); return true; });
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: workspace, refreshWorkspace: refresh, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
  expect(refresh).toHaveBeenCalledWith(f.host.wire.workspace_id);
  expect(f.open).toHaveBeenCalledTimes(2);
  expect(f.buffer().editorOpening).toBeUndefined();
  mounted.unmount();
});

it("reports a missing workspace identity as an incompatible backend and can retry after it is updated", async () => {
  const f = fixture();
  f.open.mockResolvedValueOnce({ ...f.host.snapshot(), workspace_id: "" });
  let retry!: (key: string) => void;
  const mounted = render(() => {
    retry = createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: () => f.host.wire.workspace_id, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.buffer().editorOpening?.status).toBe("error"));
  expect(f.buffer().editorOpening).toEqual({ status: "error", message: expect.stringContaining("Update and restart the backend") });
  expect(filesBufferText(required(f.buffer()))).toBe("base");
  expect(editorReplica(f.host.wire.id)).toBeUndefined();
  retry(f.key);
  await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
  expect(f.buffer().editorOpening).toBeUndefined();
  mounted.unmount();
});

it("bounds workspace reconciliation when the backend keeps returning another checkout", async () => {
  const f = fixture();
  let responses = 0;
  f.open.mockImplementation(async () => ({ ...f.host.snapshot(), workspace_id: `workspace-${++responses}` }));
  const [workspace, setWorkspace] = createSignal("workspace-0");
  const refresh = vi.fn((next: string) => { setWorkspace(next); return true; });
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: workspace, refreshWorkspace: refresh, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.buffer().editorOpening?.status).toBe("error"));
  expect(refresh).toHaveBeenCalledTimes(3);
  expect(f.open).toHaveBeenCalledTimes(4);
  expect(f.buffer().documentId).toBeNull();
  mounted.unmount();
});

it("ignores a late workspace mismatch after switching to a different workspace", async () => {
  const f = fixture();
  let complete!: (document: ReturnType<typeof f.host.snapshot>) => void;
  f.open.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
  const [workspace, setWorkspace] = createSignal("old-workspace");
  const refresh = vi.fn(() => false);
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: workspace, refreshWorkspace: refresh, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.open).toHaveBeenCalledOnce());
  setWorkspace(f.host.wire.workspace_id);
  await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
  complete({ ...f.host.snapshot(), workspace_id: "obsolete-workspace" });
  await new Promise((resolve) => setTimeout(resolve, 0));
  await waitFor(() => expect(f.buffer().editorOpening).toBeUndefined());
  expect(refresh).not.toHaveBeenCalled();
  mounted.unmount();
});

it("does not share an unfinished document open across different decoding choices", async () => {
  const f = fixture();
  let complete!: () => void;
  const barrier = new Promise<void>((resolve) => { complete = resolve; });
  const open = vi.spyOn(f.client, "openEditorDocument").mockImplementation(async () => {
    await barrier;
    throw new Error("End of decoding probe");
  });
  const disconnect = configureEditorDocuments(f.project, () => f.client, () => undefined, { get: f.buffer, all: () => [f.buffer()] });
  const completed = Promise.allSettled([
    resolveEditorDocument(f.project, { ...f.buffer(), encoding: "utf-16le" }),
    resolveEditorDocument(f.project, { ...f.buffer(), encoding: "utf-16be" }),
  ]);
  await waitFor(() => expect(open).toHaveBeenCalledTimes(2));
  expect(open.mock.calls.map((call) => call[1].decode_as)).toEqual(["utf-16le", "utf-16be"]);
  complete();
  await completed;
  disconnect();
});

it("does not refresh a disposed Files view after its pending open reports a workspace mismatch", async () => {
  const f = fixture();
  let complete!: (document: ReturnType<typeof f.host.snapshot>) => void;
  f.open.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
  const refresh = vi.fn(() => false);
  const mounted = render(() => {
    createFilesEditorSynchronization({ projectId: f.project, client: () => f.client, sessionId: () => undefined,
      workspaceId: () => "old-workspace", refreshWorkspace: refresh, onUpdated: () => {} });
    return <div />;
  });
  await waitFor(() => expect(f.open).toHaveBeenCalledOnce());
  mounted.unmount();
  complete(f.host.snapshot());
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(refresh).not.toHaveBeenCalled();
});

it("classifies a missing editor connection as recoverable transport unavailability", async () => {
  const f = fixture();
  const disconnect = configureEditorDocuments(f.project, () => null, () => undefined, { get: f.buffer, all: () => [f.buffer()] });
  await expect(resolveEditorDocument(f.project, f.buffer())).rejects.toBeInstanceOf(BackendTransportError);
  disconnect();
});
