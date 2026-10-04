import "../../test/document-outbox-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { ProjectSourceReadResponse } from "../../api/types.ts";
import { documentResidency, DOCUMENT_MEMORY_BUDGET, windowDocumentBudget } from "../documents/document-residency.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { LycaonApiError } from "../../api/http.ts";
import { openFileBufferSource, readFileBufferSource } from "./files-source-read.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { decodeUpdate, documentOutbox, encodeUpdate } from "../documents/document-outbox.ts";
import * as Y from "yjs";

vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "window", prepareClientIdentity: async () => "window" }));

const buffer = { rootId: "r1", path: "wide.txt", encoding: "utf-16le" as const };
function source(overrides: Partial<ProjectSourceReadResponse> = {}): ProjectSourceReadResponse {
  return { workspace_id: "workspace-1", workspace_kind: "project", file_id: "file-1", version_id: "v1",
    path: "wide.txt", content: "hello", binary: false, over_limit: false, writable: true, size_bytes: 5,
    encoding: "utf-8", ...overrides };
}

describe("Source reads with remembered encoding", () => {
  it("uses ordinary resolution until a root is known", async () => {
    const getProjectSource = vi.fn().mockResolvedValue(source());
    await readFileBufferSource(stubClient({ getProjectSource }), "p1", { ...buffer, rootId: "" });
    expect(getProjectSource).toHaveBeenCalledWith("p1", "wide.txt", { includeDeleted: false });
  });

  it.each(["binary", "unsupported"])("reuses the explicit byte order for %s content", async (kind) => {
    const getProjectSource = vi.fn();
    if (kind === "binary") getProjectSource.mockResolvedValueOnce(source({ binary: true, encoding: undefined, content: "" }));
    else getProjectSource.mockRejectedValueOnce(new LycaonApiError("Unsupported", 415, "unsupported_encoding"));
    getProjectSource.mockResolvedValueOnce(source({ encoding: "utf-16le" }));
    const result = await readFileBufferSource(stubClient({ getProjectSource }), "p1", buffer, "s1");
    expect(getProjectSource).toHaveBeenNthCalledWith(2, "p1", "wide.txt", {
      rootId: "r1", sessionId: "s1", decodeAs: "utf-16le", includeDeleted: true,
    });
    expect(result.encoding).toBe("utf-16le");
  });

  it.each([
    source(),
    source({ binary: true, content: "", mime: "image/png" }),
    source({ binary: true, over_limit: true, content: "" }),
  ])("keeps authoritative detected content without an encoding override", async (response) => {
    const getProjectSource = vi.fn(async () => response);
    const result = await readFileBufferSource(stubClient({ getProjectSource }), "p1", buffer);
    expect(result).toBe(response);
    expect(getProjectSource).toHaveBeenCalledOnce();
    expect(getProjectSource.mock.calls[0]).toEqual(["p1", "wide.txt", { rootId: "r1", includeDeleted: true }]);
  });

  it("does not turn unrelated failures into decoding retries", async () => {
    const error = new LycaonApiError("Permission denied", 403, "source_path_denied");
    const getProjectSource = vi.fn().mockRejectedValue(error);
    await expect(readFileBufferSource(stubClient({ getProjectSource }), "p1", buffer)).rejects.toBe(error);
    expect(getProjectSource).toHaveBeenCalledOnce();
  });
});


describe("file acquisition", () => {
  it("opens a known working file as its host document in one request", async () => {
    const fixture = new DocumentFixture("hello", { project_id: "p1", root_id: "r1", path: "wide.txt" });
    const openEditorDocument = vi.fn(async () => fixture.snapshot());
    const result = await openFileBufferSource(stubClient({ openEditorDocument }), "p1", buffer, "s1");
    result.reservation?.release();
    expect(result.document?.id).toBe(fixture.wire.id);
    expect(result.source).toMatchObject({ root_id: "r1", path: "wide.txt", writable: true, binary: false });
    expect(openEditorDocument).toHaveBeenCalledExactlyOnceWith("p1", {
      root_id: "r1", path: "wide.txt", client_id: expect.any(String),
    }, "s1");
  });
  it.each(["source_binary", "source_content_too_large", "source_read_only"] as const)("reads a file the host refuses as a document (%s) as source", async (code) => {
    const binary = code === "source_read_only" ? source({ writable: false }) : source({ binary: true, content: "", mime: "image/png" });
    const openEditorDocument = vi.fn().mockRejectedValue(new LycaonApiError("Not a document", 422, code));
    const getProjectSource = vi.fn(async () => binary);
    const result = await openFileBufferSource(stubClient({ openEditorDocument, getProjectSource }), "p1", buffer, "s1");
    result.reservation?.release();
    expect(result.source).toBe(binary);
    expect(result.document).toBeUndefined();
  });
  it("asks for only the state its preserved checkpoint lacks when no replica is live", async () => {
    const fixture = new DocumentFixture("preserved text", { id: "11111111-1111-4111-8111-111111111111", project_id: "p1", root_id: "r1", path: "wide.txt", epoch: 3 });
    const outbox = documentOutbox();
    await outbox.commit([{ kind: "checkpoint", replicaId: 11, incarnation: "incarnation", confirmed: fixture.snapshot(), documentId: fixture.wire.id, projectId: "p1", rootId: "r1", fileId: "file-1", path: "wide.txt",
      clientId: "window", epoch: 3, state: encodeUpdate(Y.encodeStateAsUpdate(fixture.doc)), synchronized: true, pendingOperations: [], history: {} }]);
    fixture.text.insert(0, "host typed ");
    const openEditorDocument = vi.fn(async (_project: string, request: { replica?: { state_vector: string } }) =>
      fixture.snapshot(request.replica?.state_vector));
    const result = await openFileBufferSource(stubClient({ openEditorDocument }), "p1", { ...buffer, documentId: fixture.wire.id }, "s1");
    result.reservation?.release();
    const request = openEditorDocument.mock.calls[0]?.[1] as { replica: { document_id: string; epoch: number; state_vector: string; base_sha256: string } };
    expect(request.replica).toMatchObject({ document_id: fixture.wire.id, epoch: 3, base_sha256: "" });
    expect(Y.decodeStateVector(decodeUpdate(request.replica.state_vector)).get(1)).toBe("preserved text".length);
    // The frame carries only the host's addition; the preserved checkpoint completes it.
    expect(result.document?.crdt_update.length).toBeLessThan(encodeUpdate(Y.encodeStateAsUpdate(fixture.doc)).length);
    expect(result.retained?.checkpoint?.documentId).toBe(fixture.wire.id);
  });
  it("opens in full when its preserved checkpoint still holds unacknowledged work", async () => {
    const fixture = new DocumentFixture("pending", { id: "22222222-2222-4222-8222-222222222222", project_id: "p1", root_id: "r1", path: "wide.txt" });
    await documentOutbox().commit([{ kind: "checkpoint", replicaId: 11, incarnation: "incarnation", confirmed: fixture.snapshot(), documentId: fixture.wire.id, projectId: "p1", rootId: "r1", fileId: "file-1", path: "wide.txt",
      clientId: "window", epoch: 1, state: encodeUpdate(Y.encodeStateAsUpdate(fixture.doc)), synchronized: false, pendingOperations: ["op"], history: {} }]);
    const openEditorDocument = vi.fn(async (_project: string, _request: Record<string, unknown>) => fixture.snapshot());
    const result = await openFileBufferSource(stubClient({ openEditorDocument }), "p1", buffer, "s1");
    result.reservation?.release();
    expect(openEditorDocument.mock.calls[0]?.[1]).not.toHaveProperty("replica");
    expect(result.retained?.replica).toBeUndefined();
  });
  it.each([2, 3, 4, 8])("admits a supported 4 MiB document with %i windows", async windows => {
    const fixture = new DocumentFixture("x".repeat(4 * 1024 * 1024));
    documentResidency.setBudget(windowDocumentBudget(windows));
    try {
      const result = await openFileBufferSource(stubClient({ openEditorDocument: async () => fixture.snapshot() }), "p1", buffer);
      expect(documentResidency.snapshot.bytes).toBeLessThanOrEqual(documentResidency.snapshot.budget);
      result.reservation?.release();
    } finally { documentResidency.setBudget(DOCUMENT_MEMORY_BUDGET); fixture.doc.destroy(); }
  });

  it.each(["offline", "missing"])("uses preserved text only for an unreachable host, not %s authority", async kind => {
    const fixture = new DocumentFixture("preserved", { project_id: "p1", root_id: "r1", path: "wide.txt" });
    await documentOutbox().commit([{ kind: "checkpoint", replicaId: 11, incarnation: "incarnation", confirmed: fixture.snapshot(), documentId: fixture.wire.id, projectId: "p1", rootId: "r1", fileId: "file-1", path: "wide.txt",
      clientId: "window", epoch: 1, state: encodeUpdate(Y.encodeStateAsUpdate(fixture.doc)), synchronized: true }]);
    const error = kind === "offline" ? new BackendTransportError(new Error("offline"), "unreachable") : new LycaonApiError("Missing", 404, "not_found");
    const openEditorDocument = vi.fn().mockRejectedValue(error);
    const opening = openFileBufferSource(stubClient({ openEditorDocument }), "p1", { ...buffer, documentId: fixture.wire.id, writable: true });
    if (kind === "missing") await expect(opening).rejects.toBe(error);
    else {
      const opened = await opening;
      expect(opened.offline).toBe(true);
      expect(opened.source.writable).toBe(true);
      opened.reservation?.release();
    }
    expect(openEditorDocument).toHaveBeenCalledOnce();
  });
  it("reads a deleted path the host holds no document for as source", async () => {
    const deleted = source({ content: "", deleted: { deleted_at: "2026-10-03T00:00:00Z" } as ProjectSourceReadResponse["deleted"] });
    const openEditorDocument = vi.fn().mockRejectedValue(new LycaonApiError("Not found", 404, "editor_document_not_found"));
    const getProjectSource = vi.fn(async () => deleted);
    const result = await openFileBufferSource(stubClient({ openEditorDocument, getProjectSource }), "p1", buffer, "s1");
    result.reservation?.release();
    expect(result.source).toBe(deleted);
    expect(result.document).toBeUndefined();
    expect(getProjectSource).toHaveBeenCalledWith("p1", "wide.txt", { rootId: "r1", sessionId: "s1", includeDeleted: true });
  });
  it("keeps worker files on the read-only source path", async () => {
    const getProjectSource = vi.fn().mockResolvedValue(source());
    (await openFileBufferSource(stubClient({ getProjectSource }), "p1", { ...buffer, jobId: "job1" })).reservation?.release();
    expect(getProjectSource).toHaveBeenCalledOnce();
  });
  it("retries unsupported bytes with the remembered explicit encoding", async () => {
    const fixture = new DocumentFixture("wide", { project_id: "p1", root_id: "r1", path: "wide.txt" });
    const openEditorDocument = vi.fn().mockRejectedValueOnce(new LycaonApiError("Unsupported", 415, "unsupported_encoding"))
      .mockResolvedValueOnce(fixture.snapshot());
    const result = await openFileBufferSource(stubClient({ openEditorDocument }), "p1", buffer);
    result.reservation?.release();
    expect(openEditorDocument.mock.calls[1]?.[1].decode_as).toBe("utf-16le");
  });
});
