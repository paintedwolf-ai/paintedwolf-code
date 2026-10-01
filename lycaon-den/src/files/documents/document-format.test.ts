// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { LycaonClient } from "../../api/client.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { DocumentFormatQueue } from "./document-format.ts";

describe("durable document format", () => {
  it("preserves offline format choices and applies them to current text", async () => {
    const outbox = memoryDocumentOutbox(), host = new DocumentFixture();
    const queue = new DocumentFormatQueue(outbox);
    queue.stage(host.snapshot(), "window", { eol: "crlf", mixedEol: false }, { sessionId: "chat" });
    const staged = queue.unpreserved();
    expect(staged).toHaveLength(1);
    await outbox.commit(staged);
    queue.preserved(staged);
    expect(queue.unpreserved()).toEqual([]);
    const restored = new DocumentFormatQueue(outbox);
    restored.restore(await outbox.read(host.wire.id));
    host.replace("another chat's edit");
    let latest = host.accepted();
    const replace = vi.fn<LycaonClient["replaceEditorDocument"]>(async (_project, _id, request) => {
      expect(request.content).toBe("another chat's edit");
      return { ...latest, eol: request.eol, revision: latest.revision + 1 };
    });
    await restored.deliver(stubClient({ replaceEditorDocument: replace }), () => ({ document: latest, text: host.text.toString() }), (next) => { latest = { ...latest, ...next }; }, async () => {});
    expect(latest.eol).toBe("crlf");
    expect(replace.mock.calls[0]?.[2]).toMatchObject({ session_id: "chat" });
    expect("turn" in (replace.mock.calls[0]?.[2] ?? {})).toBe(false);
    expect(await outbox.read(host.wire.id)).toEqual([]);
  });

  it("replays an uncertain command exactly, then rebases a definite conflict", async () => {
    const outbox = memoryDocumentOutbox(), host = new DocumentFixture();
    const queue = new DocumentFormatQueue(outbox);
    queue.stage(host.snapshot(), "window", { eol: "crlf", mixedEol: false }, { sessionId: "chat" });
    let latest = host.accepted();
    const replace = vi.fn<LycaonClient["replaceEditorDocument"]>()
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockRejectedValueOnce(new LycaonApiError("changed", 409, "editor_revision_conflict"))
      .mockImplementation(async (_project, _id, request) => ({ ...latest, eol: request.eol, revision: latest.revision + 1 }));
    const client = stubClient({ replaceEditorDocument: replace });
    await expect(queue.deliver(client, () => ({ document: latest, text: host.text.toString() }), () => {}, async () => {})).rejects.toThrow("connection lost");
    const restored = new DocumentFormatQueue(outbox);
    restored.restore(await outbox.read(host.wire.id));
    await restored.deliver(client, () => ({ document: latest, text: host.text.toString() }), (next) => { latest = { ...latest, ...next }; }, async () => { host.replace("newer text"); latest = host.accepted(); });
    expect(replace.mock.calls[1]).toEqual(replace.mock.calls[0]);
    expect(replace.mock.calls[2]?.[2]).toMatchObject({ content: "newer text", eol: "crlf" });
    expect(replace.mock.calls[2]?.[2].operation_id).not.toBe(replace.mock.calls[1]?.[2].operation_id);
    expect(await outbox.read(host.wire.id)).toEqual([]);
  });
});
