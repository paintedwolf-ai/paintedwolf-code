// @vitest-environment jsdom
import { required } from "../../test/at.ts";
import "../../test/document-outbox-fixture.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { DocumentReplica } from "../documents/document-replica.ts";
import { documentOutbox } from "../documents/document-outbox.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import type { ClientStubs } from "../../test/client-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OpenEditorDocumentRequest, EditorDocumentCommandRequest, SyncEditorDocumentRequest, SubmitEditorDocumentUpdateRequest, EditorDocument } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  resetEditorDocumentsForTests,
  editorReplica,
  resolveEditorDocument,
} from "../documents/editor-document.ts";
import { connectFilesEditorDocuments } from "../editor/files-editor-synchronization.ts";
import { applyFilesBufferDraft, applyFilesBufferEditorDocument, applyFilesBufferLoad, closeFilesBuffer, closeFilesBuffers, FILES_CLOSE_FAILED_MESSAGE, focusedFilesBufferKey, openFilesBuffer, prepareFilesRootDetach, resetProjectFilesForTests, setFilesActiveBuffer, switchFilesRootBranches, type FilesBufferRelease } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";

vi.mock("../../platform/connection/client-identity.ts", () => ({
  clientIdentity: () => "view-main",
  prepareClientIdentity: async () => "view-main",
}));

const PROJECT = "project-1";

function documentId(path: string): string {
  return `document-${path}`;
}

const fixtures = new Map<string, DocumentFixture>();
function fixture(path: string): DocumentFixture {
  let host = fixtures.get(path);
  if (!host) {
    host = new DocumentFixture(`content of ${path}`, { id: documentId(path), path, file_id: `file-${path}`, base_sha256: `sha-${path}` });
    fixtures.set(path, host);
  }
  return host;
}
function wire(path: string): EditorDocument { return fixture(path).snapshot(); }
function connect(overrides: ClientStubs = {}): void {
  const client = stubClient({
    openEditorDocument: async (_project: string, request: OpenEditorDocumentRequest) => wire(request.path),
    createEditorDocumentSnapshot: async (_project: string, id: string) => wire(id.slice("document-".length)),
    replaceEditorDocumentRetention: async () => undefined,
    syncEditorDocument: async (project: string, id: string, request: SyncEditorDocumentRequest) => fixture(id.slice("document-".length)).sync(project, id, request),
    submitEditorDocumentUpdate: async (project: string, id: string, request: SubmitEditorDocumentUpdateRequest) => fixture(id.slice("document-".length)).submit(project, id, request),
    leaveEditorDocument: async () => undefined,
    ...overrides,
  });
  connectFilesEditorDocuments(PROJECT, () => client, () => undefined);
}

function open(path: string): FileBufferKey {
  return openFilesBuffer(PROJECT, {
    intent: "permanent",
    rootId: "root-1",
    rootLabel: "repo",
    path,
  });
}

async function openJoined(path: string): Promise<FileBufferKey> {
  const key = open(path);
  applyFilesBufferLoad(PROJECT, key, {
    file_id: "",
    version_id: "",
    workspace_id: "workspace-1",
    workspace_kind: "project",
    path,
    content: `content of ${path}`,
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: 10,
    sha256: `sha-${path}`,
  });
  applyFilesBufferEditorDocument(PROJECT, key, {
    encoding: "utf-8",
    fileId: `file-${path}`,
    documentId: documentId(path),
    revision: 1,
    absent: false,
    localGeneration: 1, dirty: false,
    baseSha256: `sha-${path}`,
    sizeBytes: 10,
    eol: "lf",
    baseEol: "lf",
    mixedEol: false,
    baseMixedEol: false,
    diverged: false,
    heldAgentVersionId: null,
  });
  await resolveEditorDocument(PROJECT, projectFilesState(PROJECT).byKey[key]!);
  return key;
}

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((accept, fail) => {
    resolve = accept;
    reject = fail;
  });
  return { promise, resolve, reject };
}

/** Local durability held independently of network departure. */
function gatedPreservation() {
  const gates = new Map<string, ReturnType<typeof deferred>>();
  const gate = (id: string) => {
    const existing = gates.get(id);
    if (existing) return existing;
    const created = deferred();
    gates.set(id, created);
    return created;
  };
  const preserve = DocumentReplica.prototype.preserve;
  const preserved = vi.fn(async (id: string) => { await gate(id).promise; });
  vi.spyOn(DocumentReplica.prototype, "preserve").mockImplementation(async function (this: DocumentReplica) {
    await preserved(this.accepted.id);
    await preserve.call(this);
  });
  const openEditorDocument = vi.fn(async (_project: string, request: { path: string }) =>
    wire(request.path));
  connect({ openEditorDocument });
  return {
    preserved,
    openEditorDocument,
    finish: (path: string) => gate(documentId(path)).resolve(),
    fail: (path: string) => gate(documentId(path)).reject(new Error("offline")),
  };
}

function closeAimed(): Promise<FilesBufferRelease> | null {
  const key = focusedFilesBufferKey(PROJECT);
  return key ? closeFilesBuffer(PROJECT, key) : null;
}

describe("closing files tabs", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    connect();
  });
  afterEach(() => {
    vi.restoreAllMocks();
    resetEditorDocumentsForTests();
    resetProjectFilesForTests();
    for (const host of fixtures.values()) host.doc.destroy();
    fixtures.clear();
  });

  it("preserves a checkout's dirty replica before reopening that root in another branch", async () => {
    await switchFilesRootBranches(PROJECT, { "root-1": "worktree:first", shared: "" }, () => true);
    const a = await openJoined("a.ts");
    const shared = openFilesBuffer(PROJECT, { rootId: "shared", rootLabel: "shared", path: "common.txt", intent: "permanent" });
    const replica = editorReplica(documentId("a.ts"));
    expect(replica).toBeDefined();
    replica?.replaceLocal("unsaved worktree text");
    applyFilesBufferDraft(PROJECT, a, "unsaved worktree text");
    setFilesActiveBuffer(PROJECT, a);

    await expect(switchFilesRootBranches(PROJECT, { "root-1": "", shared: "" }, () => true)).resolves.toBe(true);
    const state = projectFilesState(PROJECT);
    const reopened = Object.values(state.byKey).find((buffer) => buffer.path === "a.ts");
    expect(reopened).toMatchObject({ loading: true, fileId: null, documentId: null, dirty: false });
    expect(filesBufferText(required(reopened))).toEqual("");
    expect(state.byKey[shared]).toBeDefined();
    expect(state.order).toEqual([reopened?.key, shared]);
    expect(state.pendingKey ?? state.activeKey).toBe(reopened?.key);
    expect((await documentOutbox().read(documentId("a.ts"))).some((record) => record.kind === "update")).toBe(true);
  });

  it("keeps the old checkout when a prepared workspace selection is superseded", async () => {
    await switchFilesRootBranches(PROJECT, { "root-1": "worktree:first" }, () => true);
    const a = await openJoined("a.ts");
    await expect(switchFilesRootBranches(PROJECT, { "root-1": "" }, () => false)).resolves.toBe(false);
    expect(projectFilesState(PROJECT).byKey[a]?.documentId).toBe(documentId("a.ts"));
    expect(projectFilesState(PROJECT).rootBranches["root-1"]).toBe("worktree:first");
  });

  it("takes each tab out of the strip while earlier releases are still in flight", async () => {
    const releases = gatedPreservation();
    const a = await openJoined("a.ts");
    await openJoined("b.ts");
    await openJoined("c.ts");
    setFilesActiveBuffer(PROJECT, a);

    const closes = [closeAimed(), closeAimed(), closeAimed()].filter(
      (close): close is Promise<FilesBufferRelease> => close != null,
    );

    expect(closes).toHaveLength(3);
    expect(projectFilesState(PROJECT).order).toEqual([]);
    expect(focusedFilesBufferKey(PROJECT)).toBeNull();
    await vi.waitFor(() => expect(releases.preserved).toHaveBeenCalledTimes(3));

    for (const path of ["a.ts", "b.ts", "c.ts"]) releases.finish(path);
    await expect(Promise.all(closes)).resolves.toEqual(["released", "released", "released"]);
    expect(projectFilesState(PROJECT).order).toEqual([]);
  });

  it("passes the aim to the closed tab's neighbour while the painted file stays on screen", async () => {
    const a = await openJoined("a.ts");
    const b = open("b.ts");
    const c = open("c.ts");
    setFilesActiveBuffer(PROJECT, b);
    expect(projectFilesState(PROJECT)).toMatchObject({ activeKey: a, pendingKey: b });

    expect(closeAimed()).not.toBeNull();

    expect(projectFilesState(PROJECT).order).toEqual([a, c]);
    expect(projectFilesState(PROJECT)).toMatchObject({ activeKey: a, pendingKey: c });
  });

  it("presents the tab the strip already selects when the painted tab closes", async () => {
    const a = await openJoined("a.ts");
    const b = open("b.ts");
    setFilesActiveBuffer(PROJECT, b);

    expect(closeFilesBuffer(PROJECT, a)).not.toBeNull();

    expect(projectFilesState(PROJECT)).toMatchObject({ order: [b], activeKey: b, pendingKey: null });
  });

  it("closes a run of tabs, stopping at one that holds a draft with no host document", async () => {
    const a = await openJoined("a.ts");
    const b = open("b.ts");
    applyFilesBufferLoad(PROJECT, b, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "b.ts",
      content: "base",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 4,
      sha256: "sha",
    });
    applyFilesBufferDraft(PROJECT, b, "typed before any host document");
    const c = open("c.ts");

    expect(closeFilesBuffers(PROJECT, [a, b, c])).toBe(false);

    expect(projectFilesState(PROJECT).order).toEqual([b, c]);
    expect(filesBufferText(projectFilesState(PROJECT).byKey[b]!)).toBe("typed before any host document");
  });

  it("restores a tab whose local preservation fails, in place and without taking the aim", async () => {
    const releases = gatedPreservation();
    const a = await openJoined("a.ts");
    const b = await openJoined("b.ts");
    const c = await openJoined("c.ts");
    setFilesActiveBuffer(PROJECT, b);

    const closing = closeAimed();
    expect(projectFilesState(PROJECT).order).toEqual([a, c]);
    expect(focusedFilesBufferKey(PROJECT)).toBe(c);

    await vi.waitFor(() => expect(releases.preserved).toHaveBeenCalledOnce());
    releases.fail("b.ts");
    await expect(closing).resolves.toBe("restored");

    const state = projectFilesState(PROJECT);
    expect(state.order).toEqual([a, b, c]);
    expect(focusedFilesBufferKey(PROJECT)).toBe(c);
    expect(state.byKey[b]).toMatchObject({ documentId: documentId("b.ts"), closeError: FILES_CLOSE_FAILED_MESSAGE });
    expect(filesBufferText(required(state.byKey[b]))).toEqual("content of b.ts");
  });

  it("presents a restored tab when nothing else is open", async () => {
    const releases = gatedPreservation();
    const a = await openJoined("a.ts");

    const closing = closeAimed();
    expect(projectFilesState(PROJECT).order).toEqual([]);
    await vi.waitFor(() => expect(releases.preserved).toHaveBeenCalledOnce());
    releases.fail("a.ts");

    await expect(closing).resolves.toBe("restored");
    expect(projectFilesState(PROJECT)).toMatchObject({ order: [a], activeKey: a });
  });

  it("lets a reopen during the release take over the document instead of restoring the tab", async () => {
    const releases = gatedPreservation();
    const a = await openJoined("a.ts");

    const closing = closeAimed();
    await vi.waitFor(() => expect(releases.preserved).toHaveBeenCalledOnce());
    expect(open("a.ts")).toBe(a);
    const reopening = resolveEditorDocument(PROJECT, projectFilesState(PROJECT).byKey[a]!);
    releases.fail("a.ts");

    await expect(closing).resolves.toBe("superseded");
    await expect(reopening).resolves.toMatchObject({ documentId: documentId("a.ts") });
    expect(releases.openEditorDocument).toHaveBeenCalledOnce();
    const state = projectFilesState(PROJECT);
    expect(state.order).toEqual([a]);
    expect(state.byKey[a]?.closeError).toBeNull();
  });

  it("holds a root detach until a close under that root settles", async () => {
    const releases = gatedPreservation();
    await openJoined("a.ts");

    const closing = closeAimed();
    let prepared = false;
    const preparing = prepareFilesRootDetach(PROJECT, "root-1").then(() => {
      prepared = true;
    });
    await vi.waitFor(() => expect(releases.preserved).toHaveBeenCalledOnce());
    expect(prepared).toBe(false);

    releases.finish("a.ts");
    await expect(closing).resolves.toBe("released");
    await preparing;
    expect(prepared).toBe(true);
  });

  it("drops a discarded draft behind a close that leaves the strip at once", async () => {
    const discardEditorDocument = vi.fn(async (_project: string, _id: string, request: EditorDocumentCommandRequest) => ({
      ...wire("a.ts"), command_history: { operation_id: request.operation_id, epoch: 1, before_update: "AAA=", update: "AAA=" },
    }));
    const leaveEditorDocument = vi.fn(async () => wire("a.ts"));
    connect({ discardEditorDocument, leaveEditorDocument });
    const a = await openJoined("a.ts");
    required(editorReplica(documentId("a.ts"))).replaceLocal("unsaved");
    applyFilesBufferDraft(PROJECT, a, "unsaved");

    const closing = closeFilesBuffer(PROJECT, a, { discardDraft: true });

    expect(projectFilesState(PROJECT).order).toEqual([]);
    await expect(closing).resolves.toBe("released");
    expect(discardEditorDocument).toHaveBeenCalledOnce();
    expect(discardEditorDocument.mock.invocationCallOrder[0]).toBeLessThan(
      leaveEditorDocument.mock.invocationCallOrder[0]!,
    );
  });

  it("closes a tab whose recovery-reference release fails, leaving the pin for a later commit", async () => {
    const key = await openJoined("a.ts");
    const replica = required(editorReplica(documentId("a.ts")));
    replica.replaceLocal("durably preserved text");
    await replica.preserve();
    const release = vi.spyOn(replica, "releaseRetention").mockRejectedValueOnce(new Error("Storage unavailable"));
    expect(await closeFilesBuffer(PROJECT, key)).toBe("released");
    expect(editorReplica(documentId("a.ts"))).toBeUndefined();
    expect(projectFilesState(PROJECT).byKey[key]).toBeUndefined();
    // The preserved history and its pin outlive the failed release; nothing was discarded.
    expect(await documentOutbox().read(documentId("a.ts"))).not.toHaveLength(0);
    expect((await documentOutbox().inspect(documentId("a.ts")))?.retainedClients).toContain(replica.clientId);
    release.mockRestore();
  });

  it("restores a tab with its draft when the discard behind its close fails", async () => {
    const discardEditorDocument = vi.fn(async (): Promise<EditorDocument> => {
      throw new Error("offline");
    });
    const leaveEditorDocument = vi.fn(async () => wire("a.ts"));
    connect({ discardEditorDocument, leaveEditorDocument });
    const a = await openJoined("a.ts");
    required(editorReplica(documentId("a.ts"))).replaceLocal("unsaved");
    applyFilesBufferDraft(PROJECT, a, "unsaved");

    const closing = closeFilesBuffer(PROJECT, a, { discardDraft: true });

    expect(projectFilesState(PROJECT).order).toEqual([]);
    await expect(closing).resolves.toBe("restored");
    expect(projectFilesState(PROJECT).byKey[a]).toMatchObject({ closeError: FILES_CLOSE_FAILED_MESSAGE });
    expect(filesBufferText(required(projectFilesState(PROJECT).byKey[a]))).toEqual("unsaved");
    expect(leaveEditorDocument).not.toHaveBeenCalled();
  });

  it("discards a draft that never reached a host document and closes", async () => {
    const b = open("b.ts");
    applyFilesBufferLoad(PROJECT, b, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      path: "b.ts",
      content: "base",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 4,
      sha256: "sha",
    });
    applyFilesBufferDraft(PROJECT, b, "typed before any host document");

    expect(closeFilesBuffer(PROJECT, b, { discardDraft: true })).not.toBeNull();

    expect(projectFilesState(PROJECT).order).toEqual([]);
  });
});
