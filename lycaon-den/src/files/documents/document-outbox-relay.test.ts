// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { documentCommand } from "./document-command.ts";
import { DocumentReplica } from "./document-replica.ts";
import { LycaonApiError } from "../../api/http.ts";
import { relayDocumentOutbox, wakeDocumentOutbox } from "./document-outbox-relay.ts";

describe("closed document delivery", () => {
  it("skips synchronized inventory entries but reads unknown and pending entries", async () => {
    const outbox = memoryDocumentOutbox();
    const entry = { documentId: "clean", projectId: "project", rootId: "root", fileId: "file", path: "a.txt" };
    vi.spyOn(outbox, "list").mockResolvedValue([
      { ...entry, synchronized: true },
      { ...entry, documentId: "unknown" },
      { ...entry, documentId: "pending", synchronized: false },
    ]);
    const read = vi.spyOn(outbox, "read");
    await relayDocumentOutbox("project", stubClient(), () => false, outbox);
    expect(read.mock.calls).toEqual([["unknown"], ["pending"]]);
  });

  it("delivers pending work across projects without a mounted project view", async () => {
    const outbox = memoryDocumentOutbox();
    for (const projectId of ["first-project", "second-project"]) {
      const host = new DocumentFixture("base", { id: `document-${projectId}`, project_id: projectId });
      const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
        createEditorDocumentSnapshot: async () => host.snapshot() });
      const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
      await replica.initialize();
      replica.replaceLocal(`pending ${projectId}`);
      await replica.close();
      await relayDocumentOutbox(undefined, client, () => false, outbox);
      expect(host.text.toString()).toBe(`pending ${projectId}`);
    }
    expect((await outbox.list()).every(entry => entry.synchronized)).toBe(true);
  });

  it("recovers pending work committed atomically with a closed tab's history", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const commit = vi.spyOn(outbox, "commit");
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
      createEditorDocumentSnapshot: vi.fn(async () => host.snapshot()) });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize();
    replica.replaceLocal("checkpoint recovery");
    await replica.close();
    expect(commit.mock.calls.some(([records]) => records.some((record) => record.kind === "checkpoint" && record.history)
      && records.some((record) => record.kind === "update"))).toBe(true);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(host.text.toString()).toBe("checkpoint recovery");
  });

  it("delivers preserved edits by document identity after the file moves", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
      createEditorDocumentSnapshot: vi.fn(async () => host.snapshot()) });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize();
    replica.replaceLocal("closed tab's pending text");
    await replica.close();
    host.wire.path = "moved.txt";
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(host.text.toString()).toBe("closed tab's pending text");
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledWith(host.wire.project_id, host.wire.id, expect.anything());
    expect((await outbox.read(host.wire.id)).filter((record) => record.kind === "update")).toEqual([]);
  });

  it("rejoins after identity rejection and retries a suspended draft without losing pending work", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const sync = vi.fn(host.sync);
    const submit = vi.fn(host.submit).mockRejectedValueOnce(new LycaonApiError("Expired replica", 409, "editor_replica_identity"));
    const client = stubClient({ syncEditorDocument: sync, submitEditorDocumentUpdate: submit,
      createEditorDocumentSnapshot: async () => host.snapshot() });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize();
    replica.replaceLocal("suspended pending text");
    await replica.close();
    sync.mockClear();
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(sync.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect((await outbox.list())[0]?.synchronized).toBe(false);
    expect((await outbox.read(host.wire.id)).some(record => record.kind === "update" && !record.acknowledged)).toBe(true);
    wakeDocumentOutbox(host.wire.id, host.wire.revision + 1, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(host.text.toString()).toBe("suspended pending text");
    expect((await outbox.list())[0]?.synchronized).toBe(true);
  });

  it("keeps disposal pending until an outstanding send releases its document", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    let began!: () => void;
    const started = new Promise<void>((resolve) => { began = resolve; });
    const client = stubClient({ syncEditorDocument: host.sync,
      submitEditorDocumentUpdate: async (...args: Parameters<typeof host.submit>) => { began(); await pending; return host.submit(...args); } });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize();
    replica.replaceLocal("pending text");
    const delivery = replica.flush();
    await started;
    const closed = vi.fn();
    const closing = replica.close().then(closed);
    try {
      await vi.waitFor(async () => expect((await outbox.read(host.wire.id)).some((record) => record.kind === "update" && !record.acknowledged)).toBe(true));
      expect(closed).not.toHaveBeenCalled();
    } finally {
      finish();
      await delivery;
      await closing;
    }
    expect(closed).toHaveBeenCalledOnce();
    expect(host.text.toString()).toBe("pending text");
  });
  it("backs off unavailable documents and wakes early on a new source revision", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit,
      createEditorDocumentSnapshot: vi.fn(async () => { throw new LycaonApiError("Document unavailable", 404, "not_found"); }) });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize();
    replica.replaceLocal("pending closed text");
    await replica.close();
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledTimes(1);
    expect((await outbox.read(host.wire.id)).some((record) => record.kind === "update" && !record.acknowledged)).toBe(true);
    wakeDocumentOutbox(host.wire.id, 2, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledTimes(2);
    wakeDocumentOutbox(host.wire.id, 2, outbox);
    wakeDocumentOutbox(host.wire.id, 1, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledTimes(2);
  });

  it("automatically retries a closed document without a source event or human action", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const pin = vi.fn().mockRejectedValueOnce(new LycaonApiError("Unavailable", 404, "not_found"))
      .mockImplementation(async () => host.snapshot());
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit, createEditorDocumentSnapshot: pin });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize(); replica.replaceLocal("automatically recovered"); await replica.close();
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    const clock = vi.spyOn(Date, "now").mockReturnValue(Date.now() + 61_000);
    try {
      await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
      expect(host.text.toString()).toBe("automatically recovered");
      expect((await outbox.read(host.wire.id)).some((r) => r.kind === "update" && !r.acknowledged)).toBe(false);
    } finally { clock.mockRestore(); }
  });

  it("automatically resumes a pending save while its editor remains open", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const command = documentCommand(host.snapshot(), { action: "save", request: {
      client_id: "window", operation_id: crypto.randomUUID(), expected_revision: 0,
    } });
    await outbox.put(command);
    const save = vi.fn(async () => host.snapshot());
    const client = stubClient({ createEditorDocumentSnapshot: async () => host.snapshot(), saveEditorDocument: save });
    await relayDocumentOutbox(host.wire.project_id, client, () => true, outbox);
    expect(save).toHaveBeenCalledTimes(1);
    expect(await outbox.read(host.wire.id)).toEqual([]);
  });

  it("backs off a recovery read failure before reserving or reading again", async () => {
    const outbox = memoryDocumentOutbox();
    vi.spyOn(outbox, "list").mockResolvedValue([{ documentId: "unreadable", projectId: "project", rootId: "root", fileId: "file", path: "a.txt", synchronized: false }]);
    const read = vi.spyOn(outbox, "read").mockRejectedValue(new Error("Recovery storage unavailable"));
    const client = stubClient();
    await relayDocumentOutbox("project", client, () => false, outbox);
    await relayDocumentOutbox("project", client, () => false, outbox);
    expect(read).toHaveBeenCalledOnce();
  });

  it("backs off transient delivery and reuses the registered incarnation", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    const sync = vi.fn(host.sync);
    const client = stubClient({ syncEditorDocument: sync,
      submitEditorDocumentUpdate: vi.fn(async () => { throw new LycaonApiError("Unavailable", 500, "internal_error"); }),
      createEditorDocumentSnapshot: vi.fn(async () => host.snapshot()) });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    await replica.initialize(); replica.replaceLocal("retry text"); await replica.close();
    sync.mockClear();
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    const first = sync.mock.calls[0]![2].incarnation;
    const read = vi.spyOn(outbox, "read");
    wakeDocumentOutbox(host.wire.id, host.wire.revision, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledTimes(1);
    expect(read).not.toHaveBeenCalled();
    wakeDocumentOutbox(host.wire.id, 3, outbox);
    await relayDocumentOutbox(host.wire.project_id, client, () => false, outbox);
    expect(client.createEditorDocumentSnapshot).toHaveBeenCalledTimes(2);
    expect(sync.mock.calls.every((call) => call[2].incarnation === first)).toBe(true);
  });

});
