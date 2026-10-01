// @vitest-environment jsdom
import { required } from "../../test/at.ts";
import "../../test/document-outbox-fixture.ts";
import { filesBufferBase } from "./project-files-buffers.ts";
import { filesBufferText } from "./project-files-buffers.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Transaction } from "@codemirror/state";
import * as Y from "yjs";
import type { EditorDocument, ReplaceEditorDocumentRequest, SaveEditorDocumentRequest } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { connectFilesEditorDocuments } from "../editor/files-editor-synchronization.ts";
import { editorReplica, resetEditorDocumentsForTests, resolveEditorDocument, subscribeEditorDocuments } from "./editor-document.ts";
import { suspendFileDocument } from "./files-residency.ts";
import { saveProjectFileDocument } from "./project-file-document-save.ts";
import { applyFilesBufferDraft, applyFilesBufferEditorDocument, applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests, setFilesBufferEditorConfig } from "./project-files-buffers.ts";
import { projectFilesState } from "./files-buffer-state.ts";

const projectId = "project-1";
beforeEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });
afterEach(() => { resetEditorDocumentsForTests(); resetProjectFilesForTests(); });

function loaded(path: string, content: string) {
  const key = openFilesBuffer(projectId, { rootId: "root-1", rootLabel: "repo", path, intent: "permanent" });
  return applyFilesBufferLoad(projectId, key, { file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project",
    root_id: "root-1", path, content, sha256: "base-sha", encoding: "utf-8", writable: true,
    over_limit: false, binary: false, size_bytes: content.length });
}

async function setup(path = "a.txt", content = "base") {
  const key = loaded(path, content);
  const host = new DocumentFixture(content, { path });
  const pins = new Map<number, EditorDocument & { text: string }>();
  const pin = vi.fn(async () => { const snapshot = host.accepted(); pins.set(snapshot.revision, { ...snapshot, text: host.text.toString() }); return snapshot; });
  const save = vi.fn(async (_project: string, _id: string, request: SaveEditorDocumentRequest) => {
    const snapshot = pins.get(request.expected_revision)!;
    host.wire.base_content = snapshot.text;
    host.wire.base_sha256 = "saved-sha";
    host.wire.base_eol = snapshot.eol;
    host.wire.dirty = host.text.toString() !== snapshot.text;
    host.wire.revision++;
    host.wire.published_revision = snapshot.revision;
    return host.snapshot();
  });
  const replace = vi.fn(async (_project: string, _id: string, request: ReplaceEditorDocumentRequest) => {
    host.wire.eol = request.eol; host.wire.mixed_eol = request.mixed_eol;
    return host.replace(request.content);
  });
  const client = stubClient({ openEditorDocument: async () => host.snapshot(), syncEditorDocument: host.sync,
    submitEditorDocumentUpdate: host.submit, replaceEditorDocument: replace, createEditorDocumentSnapshot: pin, saveEditorDocument: save,
    leaveEditorDocument: async () => undefined, observeEditorDocument: async () => host.snapshot(),
    getProjectSourceEditorConfig: async (_project: string, requested: string, rootId: string) =>
      ({ path: requested, root_id: rootId, indent_size: 2 }),
  });
  connectFilesEditorDocuments(projectId, () => client, () => undefined);
  subscribeEditorDocuments((document) => applyFilesBufferEditorDocument(projectId, key, document));
  await resolveEditorDocument(projectId, projectFilesState(projectId).byKey[key]!);
  const replica = editorReplica(host.wire.id)!;
  const type = (text: string) => { replica.replaceLocal(text); applyFilesBufferDraft(projectId, key, text); };
  return { key, host, client, pin, save, replace, replica, type };
}

describe("saving collaborative documents", () => {
  it("saves a suspended draft with hygiene and returns its body to storage", async () => {
    const f = await setup();
    setFilesBufferEditorConfig(projectId, f.key, { trimTrailingWhitespace: true, insertFinalNewline: true });
    f.type("cold draft   ");
    expect(await suspendFileDocument(projectId, "root-1", "a.txt", f.host.wire.id)).toBe(true);
    expect(editorReplica(f.host.wire.id)).toBeUndefined();
    expect(projectFilesState(projectId).byKey[f.key]?.content.state).toBe("suspended");
    const result = await saveProjectFileDocument({ client: f.client, projectId, key: f.key });
    expect(result.status).toBe("saved");
    expect(f.host.text.toString()).toBe("cold draft\n");
    expect(editorReplica(f.host.wire.id)).toBeUndefined();
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: false, content: { state: "suspended" } });
    await resolveEditorDocument(projectId, required(projectFilesState(projectId).byKey[f.key]));
    const resumed = required(editorReplica(f.host.wire.id));
    expect(resumed.currentText).toBe("cold draft\n");
    expect(resumed.history.hasHistory).toBe(true);
  });

  it("preserves a cold draft and history when its headless save fails", async () => {
    const f = await setup();
    f.type("recoverable cold draft");
    expect(await suspendFileDocument(projectId, "root-1", "a.txt", f.host.wire.id)).toBe(true);
    f.save.mockRejectedValueOnce(new Error("disk unavailable"));
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("error");
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: true, content: { state: "suspended" } });
    await resolveEditorDocument(projectId, required(projectFilesState(projectId).byKey[f.key]));
    const resumed = required(editorReplica(f.host.wire.id));
    expect(resumed.currentText).toBe("recoverable cold draft");
    resumed.stepHistory("undo");
    expect(resumed.currentText).toBe("base");
  });

  it("separates typing at the saved revision without clearing history", async () => {
    const f = await setup();
    const type = (insert: string, time: number) => {
      f.replica.acceptEditorTransaction(f.replica.history.state.update({
        changes: { from: f.replica.text.length, insert }, userEvent: "input.type", annotations: Transaction.time.of(time),
      }));
      applyFilesBufferDraft(projectId, f.key, f.replica.text.toString());
    };
    const now = Date.now();
    type("A", now);
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    type("B", now + 1);
    f.replica.stepHistory("undo");
    expect(f.replica.text.toString()).toBe("baseA");
    f.replica.stepHistory("undo");
    expect(f.replica.text.toString()).toBe("base");
    f.replica.stepHistory("redo");
    expect(f.replica.text.toString()).toBe("baseA");
  });

  it("pins accepted text before publishing through the document journal", async () => {
    const f = await setup(); f.type("edited");
    const result = await saveProjectFileDocument({ client: f.client, projectId, key: f.key, sessionId: "session-1" });
    expect(result.status).toBe("saved");
    expect(f.pin).toHaveBeenCalledOnce();
    expect(f.save).toHaveBeenCalledWith(projectId, "document-1", expect.objectContaining({ expected_revision: 2, operation_id: expect.any(String) }), "session-1");
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: false, baseSha256: "saved-sha" });
    expect(filesBufferText(required(projectFilesState(projectId).byKey[f.key]))).toEqual("edited");
    expect(filesBufferBase(required(projectFilesState(projectId).byKey[f.key]))).toEqual("edited");
  });

  it("leaves edits accepted after the pinned save dirty", async () => {
    const f = await setup(); f.type("first");
    const originalPin = f.pin.getMockImplementation()!;
    f.pin.mockImplementationOnce(async () => {
      const snapshot = await originalPin();
      f.type("second"); await f.replica.flush();
      return snapshot;
    });
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: true });
    expect(filesBufferText(required(projectFilesState(projectId).byKey[f.key]))).toEqual("second");
    expect(filesBufferBase(required(projectFilesState(projectId).byKey[f.key]))).toEqual("first");
    expect(f.save).toHaveBeenCalledOnce();
  });

  it("keeps typing after the save request out of the snapshot while pinning catches up", async () => {
    const f = await setup(); f.type("first");
    const originalPin = f.pin.getMockImplementation()!;
    f.pin.mockImplementationOnce(async () => {
      f.type("second");
      await f.replica.flush();
      expect(f.host.text.toString()).toBe("first");
      return originalPin();
    });
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    await f.replica.flush();
    expect(f.host.text.toString()).toBe("second");
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: true });
    expect(filesBufferText(required(projectFilesState(projectId).byKey[f.key]))).toEqual("second");
    expect(filesBufferBase(required(projectFilesState(projectId).byKey[f.key]))).toEqual("first");
  });

  it("retains the accepted draft after a save fails", async () => {
    const f = await setup(); f.type("retained");
    f.save.mockRejectedValueOnce(new Error("filesystem unavailable"));
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("error");
    expect(f.host.text.toString()).toBe("retained");
    expect(projectFilesState(projectId).byKey[f.key]?.dirty).toBe(true);
  });

  it("applies whitespace and EOL hygiene to the live replica before pinning", async () => {
    const f = await setup("a.txt", "line  \n"); f.type("line  ");
    setFilesBufferEditorConfig(projectId, f.key, { trimTrailingWhitespace: true, insertFinalNewline: true, endOfLine: "crlf" });
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    expect(f.host.text.toString()).toBe("line\n");
    expect(f.host.wire.eol).toBe("crlf");
    expect(projectFilesState(projectId).byKey[f.key]?.eol).toBe("crlf");
  });

  it("refreshes EditorConfig for other open files after publication", async () => {
    const source = loaded("src/a.ts", "const value = 1;\n");
    setFilesBufferEditorConfig(projectId, source, { indentSize: 8 });
    const f = await setup(".editorconfig", "root = true\n[*.ts]\nindent_size = 8\n");
    f.type("root = true\n[*.ts]\nindent_size = 2\n");
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    expect(projectFilesState(projectId).byKey[source]?.editorConfig?.indentSize).toBe(2);
  });

  it("returns clean without a filesystem command when nothing changed", async () => {
    const f = await setup();
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("clean");
    expect(f.save).not.toHaveBeenCalled();
  });
});

describe("save reservations", () => {
  it("reserves again once when the host reports the reservation stale", async () => {
    const f = await setup(); f.type("edited");
    f.save.mockRejectedValueOnce(new LycaonApiError("the editor document or file changed", 409, "editor_revision_conflict"));
    const result = await saveProjectFileDocument({ client: f.client, projectId, key: f.key });
    expect(result.status).toBe("saved");
    expect(f.pin).toHaveBeenCalledTimes(2);
    expect(f.save).toHaveBeenCalledTimes(2);
    expect(f.save.mock.calls[0]?.[2].operation_id).not.toBe(f.save.mock.calls[1]?.[2].operation_id);
    expect(projectFilesState(projectId).byKey[f.key]).toMatchObject({ dirty: false, baseSha256: "saved-sha" });
  });

  it("reports a conflict when the second reservation is stale too", async () => {
    const f = await setup(); f.type("edited");
    f.save.mockRejectedValue(new LycaonApiError("the editor document or file changed", 409, "editor_revision_conflict"));
    const result = await saveProjectFileDocument({ client: f.client, projectId, key: f.key });
    expect(result.status).toBe("conflict");
    expect(f.save).toHaveBeenCalledTimes(2);
  });

  it("applies save hygiene as exact edits rather than a whole-document replacement", async () => {
    const f = await setup();
    setFilesBufferEditorConfig(projectId, f.key, { trimTrailingWhitespace: true, insertFinalNewline: true });
    f.type("base  \nkeep");
    const changes: string[] = [];
    f.replica.doc.on("update", (update: Uint8Array) => {
      const decoded = Y.decodeUpdate(update);
      for (const struct of decoded.structs) if ("content" in struct && struct.content instanceof Y.ContentString) changes.push(struct.content.str);
    });
    expect((await saveProjectFileDocument({ client: f.client, projectId, key: f.key })).status).toBe("saved");
    expect(f.host.text.toString()).toBe("base\nkeep\n");
    // Only the final newline was inserted; nothing that stayed was rewritten.
    expect(changes).toEqual(["\n"]);
  });
});
