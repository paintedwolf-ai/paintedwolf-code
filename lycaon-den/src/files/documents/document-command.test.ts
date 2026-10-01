// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { LycaonClient } from "../../api/client.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { outboxDocument, type ReplicaCheckpoint } from "./document-outbox.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { deliverDocumentCommand, documentCommand, resumeDocumentCommands } from "./document-command.ts";

describe("durable editor commands", () => {
  it("replays the identical save after an accepted response is lost", async () => {
    const outbox = memoryDocumentOutbox();
    const fixture = new DocumentFixture();
    const document = fixture.snapshot();
    const saved = { ...document, revision: 3, base_content: "saved" };
    const save = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(saved);
    const client = stubClient({ saveEditorDocument: save });
    const command = documentCommand(document, { action: "save", request: {
      client_id: clientIdentity(), operation_id: crypto.randomUUID(), expected_revision: 2,
    } }, "session-1");
    await expect(deliverDocumentCommand(client, command, outbox)).rejects.toThrow("connection lost");
    expect(await outbox.read(document.id)).toEqual([command]);
    expect((await resumeDocumentCommands(client, document, outbox)).document).toEqual(saved);
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
    expect(await outbox.read(document.id)).toEqual([]);
  });

  it("retires a definitively rejected command without replay or manual cleanup", async () => {
    const outbox = memoryDocumentOutbox();
    const fixture = new DocumentFixture();
    const document = fixture.snapshot();
    const checkpoint: ReplicaCheckpoint = { replicaId: 11, incarnation: "incarnation", confirmed: document, kind: "checkpoint", ...outboxDocument(document),
      clientId: clientIdentity(), epoch: document.epoch, state: document.crdt_update,
      history: { doc: fixture.text.toString(), retained: "undo" }, synchronized: true, pendingOperations: [] };
    await outbox.put(checkpoint);
    const undo = vi.fn().mockRejectedValue(new LycaonApiError("The reviewed epoch changed.", 409, "editor_replica_epoch"));
    const client = stubClient({ revertEditorDocumentChange: undo });
    const command = documentCommand(document, { action: "revert", changeId: "change", request: {
      client_id: clientIdentity(), operation_id: crypto.randomUUID(), epoch: 1,
    } });
    await expect(deliverDocumentCommand(client, command, outbox)).rejects.toThrow("reviewed epoch");
    expect((await resumeDocumentCommands(client, document, outbox)).document).toEqual(document);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(await outbox.read(document.id)).toEqual([checkpoint]);
  });
  it("preserves save intent before pinning and resumes the same operation after a lost pin response", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const operationId = crypto.randomUUID();
    const command = documentCommand(host.snapshot(), { action: "save", request: {
      client_id: "window", operation_id: operationId, expected_revision: 0,
    } });
    let attempts = 0;
    const pin = vi.fn(async () => {
      expect((await outbox.read(host.wire.id)).some(r => r.kind === "command" && r.operationId === operationId)).toBe(true);
      if (++attempts === 1) throw new Error("pin response lost");
      return host.snapshot();
    });
    const save = vi.fn(async (...[_project, _document, request]: Parameters<LycaonClient["saveEditorDocument"]>) => {
      expect(request.operation_id).toBe(operationId);
      expect(request.expected_revision).toBe(host.wire.revision);
      const retained = (await outbox.read(host.wire.id)).find(r => r.kind === "command");
      expect(retained?.kind === "command" && retained.action === "save" && retained.request.expected_revision).toBe(host.wire.revision);
      return host.snapshot();
    });
    const client = stubClient({ createEditorDocumentSnapshot: pin, saveEditorDocument: save });
    await expect(deliverDocumentCommand(client, command, outbox)).rejects.toThrow("pin response lost");
    await resumeDocumentCommands(client, host.snapshot(), outbox);
    expect(pin).toHaveBeenCalledTimes(2);
    expect(save).toHaveBeenCalledTimes(1);
    expect(await outbox.read(host.wire.id)).toEqual([]);
  });

});
