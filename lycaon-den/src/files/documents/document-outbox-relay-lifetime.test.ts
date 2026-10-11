// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { DocumentReplica } from "./document-replica.ts";
import { relayDocumentOutbox } from "./document-outbox-relay.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { EditorDocument } from "../../api/types.ts";

it.each(["success", "gone"] as const)("does not continue retired relay work after a delayed %s snapshot", async (outcome) => {
  const host = new DocumentFixture("base"), outbox = memoryDocumentOutbox();
  const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit });
  const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
  await replica.initialize();
  replica.replaceLocal("pending");
  await replica.close();
  let resolve!: (document: EditorDocument) => void;
  let reject!: (error: Error) => void;
  const snapshot = vi.fn(() => new Promise<EditorDocument>((yes, no) => { resolve = yes; reject = no; }));
  const publish = vi.fn();
  let stopped = false;
  const delivery = relayDocumentOutbox(undefined, stubClient({ createEditorDocumentSnapshot: snapshot }), () => false, outbox, () => stopped, publish);
  await vi.waitFor(() => expect(snapshot).toHaveBeenCalledOnce());
  const retained = await outbox.read(host.wire.id);
  stopped = true;
  if (outcome === "success") resolve(host.snapshot());
  else reject(new LycaonApiError("missing", 404, "editor_document_not_found"));
  await delivery;
  expect(await outbox.read(host.wire.id)).toEqual(retained);
  expect(publish).not.toHaveBeenCalled();
});
