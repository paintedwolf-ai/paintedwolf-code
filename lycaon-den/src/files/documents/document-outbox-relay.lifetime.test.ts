// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { DocumentReplica } from "./document-replica.ts";
import { relayDocumentOutbox } from "./document-outbox-relay.ts";
import { documentResidency } from "./document-residency.ts";

describe("outbox relay retirement boundaries", () => {
  it("does not inventory recovery storage after retirement", async () => {
    const outbox = memoryDocumentOutbox(); const list = vi.spyOn(outbox, "list");
    await relayDocumentOutbox(undefined, stubClient(), () => false, outbox, () => true);
    expect(list).not.toHaveBeenCalled();
  });

  it.each(["read", "capacity", "snapshot"] as const)("preserves pending edits when retired during recovery %s", async (boundary) => {
    const host = new DocumentFixture("base"); const outbox = memoryDocumentOutbox();
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
      createEditorDocumentSnapshot: vi.fn(async () => host.snapshot()) });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => undefined, outbox);
    await replica.initialize(); replica.replaceLocal("Pending draft"); await replica.close();
    const before = await outbox.read(host.wire.id); let stopped = false;
    if (boundary === "read") {
      const read = outbox.read.bind(outbox);
      vi.spyOn(outbox, "read").mockImplementation(async (id) => { const records = await read(id); stopped = true; return records; });
    } else if (boundary === "snapshot") {
      vi.mocked(client.createEditorDocumentSnapshot).mockImplementation(async () => { stopped = true; return host.snapshot(); });
    } else {
      const acquire = documentResidency.acquire.bind(documentResidency);
      vi.spyOn(documentResidency, "acquire").mockImplementation(async (...args) => {
        const reservation = await acquire(...args);
        if (reservation) {
          const expand = reservation.expand.bind(reservation);
          vi.spyOn(reservation, "expand").mockImplementation(async (bytes) => { await expand(bytes); stopped = true; });
        }
        return reservation;
      });
    }
    try {
      await relayDocumentOutbox(undefined, client, () => false, outbox, () => stopped);
      if (boundary === "snapshot") expect(client.createEditorDocumentSnapshot).toHaveBeenCalledOnce();
      else expect(client.createEditorDocumentSnapshot).not.toHaveBeenCalled();
      expect(await outbox.read(host.wire.id)).toEqual(before);
      expect(host.text.toString()).toBe("base");
    } finally { vi.restoreAllMocks(); }
    vi.mocked(client.createEditorDocumentSnapshot).mockImplementation(async () => host.snapshot());
    const publish = vi.fn();
    await relayDocumentOutbox(undefined, client, () => false, outbox, () => false, publish);
    expect(host.text.toString()).toBe("Pending draft"); expect(publish).toHaveBeenCalledOnce();
  });
});
