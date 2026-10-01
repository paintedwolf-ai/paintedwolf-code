// @vitest-environment jsdom
import { createSignal } from "solid-js";
import { required } from "../../test/at.ts";
import "../../test/document-outbox-fixture.ts";
import { filesBufferText } from "./project-files-buffers.ts";
import { render, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { receiveOpenedEditorDocument, editorReplica, evictEditorDocument, resetEditorDocumentsForTests, resolveEditorDocument } from "./editor-document.ts";
import { createFilesEditorSynchronization } from "../editor/files-editor-synchronization.ts";
import { applyFilesBufferLoad, markFilesBufferLoading, openFilesBuffer, resetProjectFilesForTests, setFilesActiveBuffer } from "./project-files-buffers.ts";
import { projectFilesState } from "./files-buffer-state.ts";

beforeEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });
afterEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });

function setup(overrides = {}, reachable = () => true) {
  const host = new DocumentFixture();
  const open = vi.fn(async () => host.snapshot());
  const observe = vi.fn(async () => host.snapshot());
  const client = stubClient({ openEditorDocument: open, syncEditorDocument: host.sync,
    submitEditorDocumentUpdate: host.submit, observeEditorDocument: observe,
    createEditorDocumentSnapshot: async () => host.snapshot(), replaceEditorDocumentRetention: async () => undefined,
    leaveEditorDocument: async () => undefined, publishEditorDocumentPresence: async () => undefined, ...overrides });
  let key = openFilesBuffer(host.wire.project_id, { rootId: host.wire.root_id, rootLabel: "root", path: host.wire.path, intent: "permanent" });
  const load = () => {
    key = applyFilesBufferLoad(host.wire.project_id, key, { file_id: host.wire.file_id, version_id: "", workspace_id: host.wire.workspace_id,
      workspace_kind: "project", root_id: host.wire.root_id, path: host.wire.path, content: "base", sha256: "base-sha",
      encoding: "utf-8", writable: true, over_limit: false, binary: false, size_bytes: 4 });
  };
  const buffer = () => projectFilesState(host.wire.project_id).byKey[key]!;
  const mount = () => render(() => {
    createFilesEditorSynchronization({ projectId: host.wire.project_id, client: () => client, sessionId: () => undefined,
      workspaceId: () => host.wire.workspace_id, workspaceSettled: () => true, reachable, onUpdated: () => {} });
    return <div />;
  });
  const rendered = mount();
  return { host, client, open, observe, load, buffer, mount, rendered };
}

describe("opening loaded files into collaborative documents", () => {
  it("settles unreachable first paint without repeatedly replacing its opening state", async () => {
    const [reachable, setReachable] = createSignal(false);
    const f = setup({}, reachable);
    expect(() => f.load()).not.toThrow();
    expect(f.buffer().editorOpening?.status).toBe("reconnecting");
    expect(f.open).not.toHaveBeenCalled();
    setReachable(true);
    await waitFor(() => expect(editorReplica(f.buffer().documentId)).toBeDefined());
    expect(f.buffer().editorOpening).toBeUndefined();
    f.rendered.unmount();
  });

  it("restores the selected document before its neighbors and leaves unrelated cached tabs unjoined", async () => {
    const fixtures = Array.from({ length: 20 }, (_, index) => new DocumentFixture("base", {
      id: `document-${index}`, file_id: `file-${index}`, path: `file-${index}.txt`,
    }));
    const keys = fixtures.map(host => {
      const key = openFilesBuffer(host.wire.project_id, { rootId: host.wire.root_id, rootLabel: "root", path: host.wire.path, intent: "permanent" });
      return applyFilesBufferLoad(host.wire.project_id, key, { file_id: host.wire.file_id, version_id: "", workspace_id: host.wire.workspace_id,
        workspace_kind: "project", root_id: host.wire.root_id, path: host.wire.path, content: "base", sha256: "base-sha",
        encoding: "utf-8", writable: true, over_limit: false, binary: false, size_bytes: 4 });
    });
    const selected = fixtures[10]!;
    setFilesActiveBuffer(selected.wire.project_id, keys[10]!);
    let release!: () => void;
    const ready = new Promise<void>(resolve => { release = resolve; });
    const open = vi.fn(async (_project: string, request: { path: string }) => fixtures.find(host => host.wire.path === request.path)!.snapshot());
    const client = stubClient({ openEditorDocument: open, syncEditorDocument: async (...args: Parameters<DocumentFixture["sync"]>) => {
      if (args[1] === selected.wire.id) await ready;
      return fixtures.find(host => host.wire.id === args[1])!.sync(...args);
    }, leaveEditorDocument: async () => undefined, publishEditorDocumentPresence: async () => undefined });
    const rendered = render(() => {
      createFilesEditorSynchronization({ projectId: selected.wire.project_id, client: () => client, sessionId: () => undefined,
        workspaceId: () => selected.wire.workspace_id, workspaceSettled: () => true, onUpdated: () => {} });
      return <div />;
    });
    await waitFor(() => expect(open).toHaveBeenCalledOnce());
    expect(open.mock.calls[0]![1].path).toBe(selected.wire.path);
    release();
    await waitFor(() => expect(open).toHaveBeenCalledTimes(3));
    expect(open.mock.calls.map(call => call[1].path)).toEqual(["file-10.txt", "file-9.txt", "file-11.txt"]);
    setFilesActiveBuffer(selected.wire.project_id, keys[19]!);
    await waitFor(() => expect(projectFilesState(selected.wire.project_id).byKey[keys[19]!]!.documentId).toBe("document-19"));
    expect(open.mock.calls.map(call => call[1].path)).not.toContain("file-0.txt");
    rendered.unmount();
  });

  it("reselects an unread tab without starting a competing document open", async () => {
    const f = setup();
    const select = () => openFilesBuffer(f.host.wire.project_id, {
      rootId: f.host.wire.root_id, rootLabel: "root", path: f.host.wire.path, intent: "permanent",
    });
    select();
    select();
    await Promise.resolve();
    await Promise.resolve();
    expect(f.open).not.toHaveBeenCalled();
    const adoption = receiveOpenedEditorDocument(f.host.wire.project_id, f.client, f.host.snapshot());
    f.load();
    select();
    await adoption;
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    expect(f.open).not.toHaveBeenCalled();
    f.rendered.unmount();
  });

  it("adopts the document from the source response before publishing the editor", async () => {
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const f = setup();
    const sync = vi.spyOn(f.client, "syncEditorDocument").mockImplementation(async (...args) => {
      await barrier;
      return f.host.sync(...args);
    });
    const adoption = receiveOpenedEditorDocument(f.host.wire.project_id, f.client, f.host.snapshot());
    f.load();
    await waitFor(() => expect(sync).toHaveBeenCalledOnce());
    expect(f.buffer().documentId).toBeNull();
    expect(f.buffer().editorOpening?.status).toBe("opening");
    expect(f.open).not.toHaveBeenCalled();
    release();
    await adoption;
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    expect(f.buffer().editorOpening).toBeUndefined();
    expect(f.open).not.toHaveBeenCalled();
    f.rendered.unmount();
  });

  it("reuses a supplied document even if synchronization finishes before buffer loading", async () => {
    const f = setup();
    const adoption = receiveOpenedEditorDocument(f.host.wire.project_id, f.client, f.host.snapshot());
    await adoption;
    await waitFor(() => expect(editorReplica(f.host.wire.id)).toBeDefined());
    f.load();
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    expect(f.open).not.toHaveBeenCalled();
    f.rendered.unmount();
  });

  it("releases an acquired replica if its view closes during synchronization", async () => {
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const leave = vi.fn(async () => undefined);
    const f = setup({ leaveEditorDocument: leave });
    const sync = vi.spyOn(f.client, "syncEditorDocument").mockImplementation(async (...args) => {
      await barrier;
      return f.host.sync(...args);
    });
    const adoption = receiveOpenedEditorDocument(f.host.wire.project_id, f.client, f.host.snapshot());
    f.load();
    await waitFor(() => expect(sync).toHaveBeenCalledOnce());
    f.rendered.unmount();
    release();
    await adoption;
    await waitFor(() => {
      expect(leave).toHaveBeenCalledWith(f.host.wire.project_id, f.host.wire.id, {
        client_id: expect.any(String), incarnation: expect.any(String),
      });
      expect(editorReplica(f.host.wire.id)).toBeUndefined();
    });
  });

  it("initializes automatically and reconciles a later source reload with the admitted replica", async () => {
    const f = setup();
    f.load();
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    expect(f.buffer().editorOpening).toBeUndefined();
    expect(f.open).toHaveBeenCalledOnce();
    f.host.replace("outside change");
    markFilesBufferLoading(f.host.wire.project_id, f.buffer().key);
    f.load();
    await waitFor(() => expect(filesBufferText(required(f.buffer()))).toBe("outside change"));
    expect(f.buffer().editorOpening).toBeUndefined();
    expect(f.observe).toHaveBeenCalledOnce();
    expect(f.open).toHaveBeenCalledOnce();
    f.rendered.unmount();
  });

  it("keeps an initialization failure on the file and recovers after an ordinary reload", async () => {
    const open = vi.fn().mockRejectedValueOnce(new LycaonApiError("Document storage is unavailable.", 500, "internal_error"));
    const f = setup({ openEditorDocument: open });
    open.mockResolvedValue(f.host.snapshot());
    f.load();
    await waitFor(() => expect(f.buffer().editorOpening).toEqual({ status: "error", message: "Document storage is unavailable." }));
    expect(filesBufferText(required(f.buffer()))).toBe("base");
    expect(f.buffer().documentId).toBeNull();
    markFilesBufferLoading(f.host.wire.project_id, f.buffer().key);
    f.load();
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    expect(f.buffer().editorOpening).toBeUndefined();
    expect(open).toHaveBeenCalledTimes(2);
    f.rendered.unmount();
  });

  it("does not return a replica to another opener before its initialization completes", async () => {
    let release!: () => void;
    const barrier = new Promise<void>((resolve) => { release = resolve; });
    const host = new DocumentFixture();
    const sync = vi.fn(async (...args: Parameters<typeof host.sync>) => { await barrier; return host.sync(...args); });
    const f = setup({ syncEditorDocument: sync });
    f.load();
    await waitFor(() => expect(sync).toHaveBeenCalledOnce());
    expect(f.buffer().editorOpening?.status).toBe("opening");
    let returned = false;
    const second = resolveEditorDocument(f.host.wire.project_id, { ...f.buffer(), documentId: f.host.wire.id }).then((document) => { returned = true; return document; });
    await Promise.resolve();
    await Promise.resolve();
    expect(returned).toBe(false);
    release();
    await second;
    await waitFor(() => expect(f.buffer().editorOpening).toBeUndefined());
    expect(editorReplica(f.host.wire.id)?.text.toString()).toBe("base");
    f.rendered.unmount();
  });

  it("keeps a file editable when the Files view remounts while its predecessor releases the document", async () => {
    const leave = vi.fn(async () => undefined);
    const f = setup({ leaveEditorDocument: leave });
    f.load();
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    const replica = editorReplica(f.host.wire.id);
    f.rendered.unmount();
    const release = evictEditorDocument(f.host.wire.id);
    const remounted = f.mount();
    await expect(release).resolves.toBe(false);
    await waitFor(() => expect(f.buffer().editorOpening).toBeUndefined());
    expect(editorReplica(f.host.wire.id)).toBe(replica);
    expect(leave).not.toHaveBeenCalled();
    expect(f.open).toHaveBeenCalledOnce();
    remounted.unmount();
  });

  it("reopens a file whose document was released before a remounted Files view could reclaim it", async () => {
    const f = setup();
    f.load();
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id));
    const replica = editorReplica(f.host.wire.id)!;
    f.rendered.unmount();
    await waitFor(() => expect(editorReplica(f.host.wire.id)).toBeUndefined());
    const remounted = f.mount();
    f.load();
    await waitFor(() => {
      const reopened = editorReplica(f.host.wire.id);
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe(replica);
    });
    await waitFor(() => expect(f.buffer().editorOpening).toBeUndefined());
    expect(f.open).toHaveBeenCalledOnce();
    remounted.unmount();
  });

  it("reuses its participant when a join was admitted but its response was lost", async () => {
    const host = new DocumentFixture();
    const participants: Array<number | undefined> = [];
    let lost = false;
    const sync = vi.fn(async (...args: Parameters<typeof host.sync>) => {
      const frame = await host.sync(...args);
      participants.push(frame.replica_id);
      if (!lost) { lost = true; throw new LycaonApiError("Response interrupted", 429, "rate_limited"); }
      return frame;
    });
    const f = setup({ syncEditorDocument: sync });
    f.load();
    await waitFor(() => expect(f.buffer().editorOpening?.status).toBe("reconnecting"));
    await waitFor(() => expect(f.buffer().documentId).toBe(f.host.wire.id), { timeout: 5000 });
    expect(sync).toHaveBeenCalledTimes(2);
    expect(sync.mock.calls[1]![2].incarnation).toBe(sync.mock.calls[0]![2].incarnation);
    expect(participants[1]).toBe(participants[0]);
    expect(editorReplica(f.host.wire.id)?.doc.clientID).toBe(participants[0]);
    f.rendered.unmount();
  });
});
