// @vitest-environment jsdom
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { LycaonApiError } from "../../api/http.ts";
import { required } from "../../test/at.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { runEditorDocumentOperation, flushEditorDocumentDraft } from "./editor-document.ts";
import { resumeDocumentCommands } from "./document-command.ts";
import { CHECKPOINT_IDLE_MS, decodeDocumentState, DocumentReplica } from "./document-replica.ts";
import * as Y from "yjs";
import { decodeUpdate, type DocumentOutbox, type OutboxRecord } from "./document-outbox.ts";

/** The text a checkpoint's CRDT state carries; its history snapshot holds no document text. */
function checkpointText(record: OutboxRecord | undefined): string | undefined {
  if (record?.kind !== "checkpoint") return undefined;
  const doc = new Y.Doc();
  Y.applyUpdate(doc, decodeUpdate(record.state));
  const text = doc.getText("text").toString();
  doc.destroy();
  return text;
}

const identity = vi.hoisted(() => ({ value: "window" }));
vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => identity.value, prepareClientIdentity: async () => identity.value }));
const replicas: DocumentReplica[] = [];
const views: EditorView[] = [];
afterEach(async () => {
  for (const view of views.splice(0)) { const parent = view.dom.parentElement; view.destroy(); parent?.remove(); }
  for (const replica of replicas.splice(0)) await replica.close();
  identity.value = "window";
  vi.useRealTimers();
});

async function open(fixture: DocumentFixture, outbox = memoryDocumentOutbox(), overrides = {}) {
  const client = stubClient({ syncEditorDocument: fixture.sync, submitEditorDocumentUpdate: fixture.submit,
    publishEditorDocumentPresence: vi.fn(async () => undefined), ...overrides });
  const replica = new DocumentReplica(fixture.snapshot(), () => client, () => undefined, outbox);
  replicas.push(replica);
  await replica.initialize();
  return { replica, client };
}

function viewFor(replica: DocumentReplica): EditorView {
  const parent = document.createElement("div");
  document.body.append(parent);
  const view = new EditorView({ parent, state: EditorState.create({ doc: replica.text.toString(), extensions: replica.extension }) });
  views.push(view);
  return view;
}

describe("collaborative editor documents", () => {
  it("reopens preserved history without rewriting an unchanged checkpoint", async () => {
    const fixture = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const first = await open(fixture, outbox);
    first.replica.replaceLocal("preserved edit");
    await first.replica.flush();
    const history = first.replica.history.snapshot();
    await first.replica.close();
    const commit = vi.spyOn(outbox, "commit");
    const put = vi.spyOn(outbox, "put");
    const { replica } = await open(fixture, outbox);
    expect(replica.history.snapshot()).toEqual(history);
    const view = viewFor(replica);
    replica.detachEditor(view);
    expect(replica.history.snapshot()).toEqual(history);
    await replica.preserve();
    await replica.flush();
    await replica.close();
    expect(commit).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
  });

  it("writes nothing for a synchronized document that was only opened and closed", async () => {
    const outbox = memoryDocumentOutbox();
    const commit = vi.spyOn(outbox, "commit");
    const put = vi.spyOn(outbox, "put");
    const { replica } = await open(new DocumentFixture(), outbox);
    await replica.preserve({ checkpoint: true });
    await replica.close();
    expect(commit).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
    expect(await outbox.read(replica.accepted.id)).toEqual([]);
  });

  it("hydrates offline with its host-allocated identity and synchronizes later edits", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const { replica: first, client } = await open(host, outbox);
    first.replaceLocal("saved local edit");
    await first.flush();
    const replicaId = first.doc.clientID;
    const incarnation = first.incarnation;
    await first.close();
    const checkpoint = required((await outbox.read(host.wire.id)).find(record => record.kind === "checkpoint"));
    let online = false;
    const resumed = new DocumentReplica(checkpoint.confirmed, () => online ? client : null, () => {}, outbox);
    replicas.push(resumed);
    await resumed.initialize(undefined, true);
    expect(resumed.doc.clientID).toBe(replicaId);
    expect(resumed.incarnation).toBe(incarnation);
    expect(resumed.currentText).toBe("saved local edit");
    expect(resumed.history.hasHistory).toBe(true);
    resumed.replaceLocal("typed while offline");
    await resumed.preserve();
    const pending = (await outbox.read(host.wire.id)).filter(record => record.kind === "update" && !record.acknowledged);
    expect(pending).toEqual([expect.objectContaining({ replicaId })]);
    const submit = vi.spyOn(client, "submitEditorDocumentUpdate").mockImplementation(async (project, document, request) => {
      expect(request.replica_id).toBe(replicaId);
      const update = Y.decodeUpdate(decodeUpdate(request.update));
      expect(update.structs.every(struct => struct.id.client === replicaId)).toBe(true);
      return host.submit(project, document, request);
    });
    online = true;
    await resumed.synchronize();
    await resumed.flush();
    expect(host.text.toString()).toBe("typed while offline");
    expect(submit).toHaveBeenCalled();
  });

  it.each(["unreachable", "rejected"])("handles a %s sync after the host open without losing recovery", async kind => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const { replica: first } = await open(host, outbox);
    first.replaceLocal("preserved pending edit");
    const replicaId = first.doc.clientID;
    await first.close();
    const error = kind === "unreachable" ? new BackendTransportError(new Error("Disconnected"), "unreachable")
      : new LycaonApiError("Rejected", 403, "forbidden");
    const client = stubClient({ syncEditorDocument: async () => { throw error; } });
    const resumed = new DocumentReplica(host.snapshot(), () => client, () => {}, outbox);
    replicas.push(resumed);
    if (kind === "rejected") {
      await expect(resumed.initialize()).rejects.toBe(error);
      return;
    }
    await resumed.initialize();
    expect(resumed.initialized).toBe(true);
    expect(resumed.currentText).toBe("preserved pending edit");
    expect(resumed.doc.clientID).toBe(replicaId);
    expect(resumed.pendingCount).toBeGreaterThan(0);
    resumed.replaceLocal("edited after connection loss");
    await resumed.preserve();
    expect(checkpointText((await outbox.read(host.wire.id)).find(record => record.kind === "checkpoint"))).toBe("preserved pending edit");
  });

  it("does not publish stale clean metadata when delivery finishes during suspension", async () => {
    const host = new DocumentFixture(), outbox = memoryDocumentOutbox();
    let deliver!: () => void;
    const response = new Promise<void>(resolve => { deliver = resolve; });
    const submit = vi.fn(async (...args: Parameters<DocumentFixture["submit"]>) => {
      await response;
      return host.submit(...args);
    });
    const dirty: boolean[] = [];
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: submit,
      leaveEditorDocument: async () => undefined });
    const replica = new DocumentReplica(host.snapshot(), () => client, value => dirty.push(value.dirty), outbox);
    replicas.push(replica);
    await replica.initialize();
    dirty.length = 0;
    replica.replaceLocal("unsaved work");
    const flushing = replica.flush();
    await vi.waitFor(() => expect(submit).toHaveBeenCalledOnce());
    const closing = replica.close();
    await new Promise(resolve => setTimeout(resolve, 0));
    deliver();
    await Promise.all([flushing, closing]);
    expect(dirty.length).toBeGreaterThan(0);
    expect(dirty.every(value => value)).toBe(true);
    expect(host.snapshot().dirty).toBe(true);
  });

  it("hydrates pending checkpoint text without claiming it in the confirmed vector", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const { replica: first } = await open(host, outbox);
    first.replaceLocal("pending local text");
    await first.close();
    const checkpoint = required((await outbox.read(host.wire.id)).find(record => record.kind === "checkpoint"));
    const decoded = decodeDocumentState(checkpoint.confirmed, checkpoint);
    expect(decoded.text).toBe("base");
    const replica = new DocumentReplica(checkpoint.confirmed, () => null, () => {}, outbox, undefined, undefined, decoded.confirmed);
    replicas.push(replica);
    await replica.initialize(undefined, true);
    expect(replica.currentText).toBe("pending local text");
    expect(replica.acceptedText).toBe("base");
    expect(replica.pendingCount).toBeGreaterThan(0);
  });

  it("commits undo retention with its covering checkpoint and releases it on tab close", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const { replica } = await open(host, outbox);
    replica.replaceLocal("local history");
    await replica.flush();
    await replica.preserve({ checkpoint: true });
    expect(await outbox.list(host.wire.project_id)).toEqual([expect.objectContaining({ retainedClients: ["window"] })]);
    await replica.releaseRetention();
    expect(await outbox.list(host.wire.project_id)).toEqual([expect.objectContaining({ retainedClients: [] })]);
  });

  it.each(["local", "remote", "epoch", "path"] as const)("preserves a %s change after checkpoint restoration", async (change) => {
    const fixture = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const first = await open(fixture, outbox);
    first.replica.replaceLocal("checkpointed edit");
    await first.replica.flush();
    await first.replica.close();
    if (change === "remote") fixture.replace("remote edit");
    if (change === "epoch") fixture.import("replacement epoch");
    if (change === "path") fixture.wire.path = "renamed.txt";
    const { replica } = await open(fixture, outbox);
    if (change === "local") replica.replaceLocal("local edit");
    await replica.preserve({ checkpoint: true });
    const checkpoint = (await outbox.read(fixture.wire.id)).find(record => record.kind === "checkpoint");
    if (change === "epoch") {
      // A stale epoch with nothing pending leaves the host state as the only recovery base.
      expect(checkpoint).toBeUndefined();
      return;
    }
    expect(checkpoint).toMatchObject({ path: fixture.wire.path, epoch: fixture.wire.epoch, synchronized: change !== "local" });
    expect(checkpointText(checkpoint)).toBe(replica.text.toString());
    expect(checkpoint).not.toHaveProperty("history.doc");
    if (change === "local") expect(checkpoint?.pendingOperations).toHaveLength(1);
  });

  it("recovers undo history for edits preserved after the last checkpoint", async () => {
    const fixture = new DocumentFixture("base");
    const outbox = memoryDocumentOutbox();
    const first = await open(fixture, outbox);
    first.replica.replaceLocal("first");
    await first.replica.flush();
    first.replica.history.boundary();
    first.replica.replaceLocal("second");
    await first.replica.preserve();
    expect(checkpointText((await outbox.read(fixture.wire.id)).find((record) => record.kind === "checkpoint"))).toBe("first");
    // An interruption before close reopens from the durable records alone.
    const { replica } = await open(fixture, outbox);
    expect(replica.text.toString()).toBe("second");
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("first");
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("base");
  });

  it("acknowledges only the history a completed checkpoint covered", async () => {
    vi.useFakeTimers();
    const outbox = memoryDocumentOutbox();
    const { replica } = await open(new DocumentFixture(), outbox);
    let release = () => {};
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const commit = outbox.commit.bind(outbox);
    vi.spyOn(outbox, "commit").mockImplementationOnce(async (records, remove) => { await gate; await commit(records, remove); });
    replica.replaceLocal("first");
    const pending = replica.preserve();
    replica.history.boundary();
    replica.replaceLocal("first second");
    release();
    await pending;
    await replica.preserve({ checkpoint: true });
    const checkpoint = (await outbox.read(replica.accepted.id)).find((record) => record.kind === "checkpoint");
    expect(checkpointText(checkpoint)).toBe("first second");
  });

  it("keeps acknowledged updates until a checkpoint covers their history", async () => {
    vi.useFakeTimers();
    const fixture = new DocumentFixture("base");
    const outbox = memoryDocumentOutbox();
    const first = await open(fixture, outbox);
    first.replica.replaceLocal("first");
    await first.replica.flush();
    await vi.advanceTimersByTimeAsync(CHECKPOINT_IDLE_MS);
    first.replica.history.boundary();
    first.replica.replaceLocal("second");
    await first.replica.flush();
    // Delivered and acknowledged, but the idle checkpoint has not run: the update stays.
    expect((await outbox.read(fixture.wire.id)).map((record) => record.kind).sort()).toEqual(["checkpoint", "update"]);
    expect(checkpointText((await outbox.read(fixture.wire.id)).find((record) => record.kind === "checkpoint"))).toBe("first");
    await vi.advanceTimersByTimeAsync(CHECKPOINT_IDLE_MS);
    expect((await outbox.read(fixture.wire.id)).map((record) => record.kind)).toEqual(["checkpoint"]);
    expect(checkpointText((await outbox.read(fixture.wire.id))[0])).toBe("second");
    vi.useRealTimers();
    // An interruption between the acknowledgement and that checkpoint still recovers the undo step.
    first.replica.history.boundary();
    first.replica.replaceLocal("third");
    await first.replica.flush();
    const { replica } = await open(fixture, outbox);
    expect(replica.text.toString()).toBe("third");
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("second");
  });

  it("captures undo history once typing pauses and appends only updates in between", async () => {
    vi.useFakeTimers();
    const outbox = memoryDocumentOutbox();
    const { replica } = await open(new DocumentFixture(), outbox);
    const commit = vi.spyOn(outbox, "commit");
    const checkpoints = () => commit.mock.calls.filter(([records]) => records.some((record) => record.kind === "checkpoint"));
    replica.replaceLocal("first");
    await replica.preserve();
    expect(commit.mock.calls[0]?.[0].map((record) => record.kind)).toEqual(["checkpoint", "update"]);
    replica.replaceLocal("first second");
    await replica.preserve();
    expect(commit.mock.calls[1]?.[0].map((record) => record.kind)).toEqual(["update"]);
    expect(checkpoints()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(2000);
    expect(checkpoints()).toHaveLength(2);
    const latest = checkpoints()[1]?.[0].find((record) => record.kind === "checkpoint");
    expect(checkpointText(latest)).toBe("first second");
  });

  it("does no storage work when an unchanged document is preserved or flushed", async () => {
    const outbox = memoryDocumentOutbox();
    const { replica } = await open(new DocumentFixture(), outbox);
    const commit = vi.spyOn(outbox, "commit");
    const put = vi.spyOn(outbox, "put");
    viewFor(replica);
    await replica.preserve();
    await replica.preserve();
    await replica.flush();
    expect(commit).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
    replica.replaceLocal("a real edit");
    await replica.preserve();
    expect(commit).toHaveBeenCalledOnce();
    expect((await outbox.read(replica.accepted.id)).some(record => record.kind === "update")).toBe(true);
  });

  it("uses the recovered outbox snapshot without reading it twice", async () => {
    const fixture = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const read = vi.spyOn(outbox, "read");
    const client = stubClient({ syncEditorDocument: fixture.sync, submitEditorDocumentUpdate: fixture.submit });
    const recovery = await resumeDocumentCommands(client, fixture.snapshot(), outbox);
    const replica = new DocumentReplica(recovery.document, () => client, () => undefined, outbox);
    replicas.push(replica);
    await replica.initialize(recovery.records);
    expect(read).toHaveBeenCalledTimes(1);
    expect(replica.initialized).toBe(true);
    expect(replica.text.toString()).toBe(fixture.text.toString());
  });

  it("keeps the chat from authoring when delivery happens after switching chats", async () => {
    const host = new DocumentFixture();
    const submit = vi.fn(host.submit);
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: submit });
    let author = { sessionId: "first chat" };
    const replica = new DocumentReplica(host.snapshot(), () => client, () => undefined, memoryDocumentOutbox(), () => author);
    replicas.push(replica);
    await replica.initialize();
    replica.replaceLocal("base one");
    author = { sessionId: "second chat" };
    replica.replaceLocal("base one two");
    author = { sessionId: "" };
    await replica.flush();
    // The host records each chat's turn when it accepts the text; the window names only the chat.
    expect(submit.mock.calls.map((call) => call[2].session_id)).toEqual(["first chat", "second chat"]);
    expect(submit.mock.calls.every((call) => !("turn" in call[2]))).toBe(true);
    expect(host.text.toString()).toBe("base one two");
  });

  it("automatically resumes another window's unsent edits and their later dependents", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    identity.value = "first window";
    const { replica: first } = await open(host, outbox);
    first.replaceLocal("base one");
    await first.preserve();
    await first.close(); replicas.splice(replicas.indexOf(first), 1);
    identity.value = "second window";
    const { replica: second } = await open(host, outbox);
    expect(second.text.toString()).toBe("base one");
    second.replaceLocal("base one two");
    await second.preserve();
    await second.close(); replicas.splice(replicas.indexOf(second), 1);
    identity.value = "reopened window";
    const submit = vi.fn(host.submit);
    const { replica: reopened } = await open(host, outbox, { submitEditorDocumentUpdate: submit });
    expect(reopened.text.toString()).toBe("base one two");
    await reopened.flush();
    expect(host.text.toString()).toBe("base one two");
    expect(submit.mock.calls.map((call) => call[2].client_id)).toEqual(["first window", "second window"]);
    expect((await outbox.read(host.wire.id)).filter((record) => record.kind === "update")).toEqual([]);
  });

  it("coalesces offline typing without a per-keystroke record limit", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const { replica } = await open(host, outbox);
    for (let count = 1; count <= 5000; count++) {
      replica.replaceLocal("base" + "x".repeat(count));
      if (count % 100 === 0) await replica.preserve();
    }
    expect(replica.capacityBlocked).toBe(false);
    expect((await outbox.read(host.wire.id)).filter((record) => record.kind === "update")).toHaveLength(1);
    await replica.close(); replicas.splice(replicas.indexOf(replica), 1);
    const submit = vi.fn(host.submit);
    const { replica: reopened } = await open(host, outbox, { submitEditorDocumentUpdate: submit });
    await reopened.flush();
    expect(host.text.toString()).toBe("base" + "x".repeat(5000));
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("uses presence and screening events locally but synchronizes a metadata event after a missed edit", async () => {
    const host = new DocumentFixture();
    const sync = vi.fn(host.sync);
    const { replica } = await open(host, memoryDocumentOutbox(), { syncEditorDocument: sync });
    const event = { ...host.snapshot(), content_changed: false, secret_screen_status: "complete" as const,
      participants: [{ client_id: "peer", person_id: "person", ranges: [], main: 0 }] };
    for (let index = 0; index < 20; index++) await replica.receiveEvent(event);
    expect(sync).toHaveBeenCalledTimes(1);
    expect(replica.accepted.participants).toEqual(event.participants);
    expect(replica.accepted.secret_screen_status).toBe("complete");
    host.replace("missed update");
    await replica.receiveEvent({ ...host.snapshot(), content_changed: false });
    expect(sync).toHaveBeenCalledTimes(2);
    expect(replica.text.toString()).toBe("missed update");
  });

  it("refuses an oversized paste before changing the replica or its outbox", async () => {
    const { replica } = await open(new DocumentFixture());
    const view = viewFor(replica);
    view.dispatch({ changes: { from: 0, insert: "x".repeat(8 * 1024 * 1024) } });
    expect(view.state.doc.toString()).toBe("base");
    expect(replica.text.toString()).toBe("base");
    expect(replica.pendingCount).toBe(0);
  });

  it("shows concurrent window edits immediately and preserves the other writer during undo", async () => {
    const host = new DocumentFixture("a🐺z");
    identity.value = "left";
    const { replica: left } = await open(host);
    identity.value = "right";
    const { replica: right } = await open(host);
    const first = viewFor(left);
    const second = viewFor(right);
    first.dispatch({ changes: { from: 1, insert: "left" } });
    second.dispatch({ changes: { from: 3, insert: "right" } });
    expect(left.text.toString()).toBe("aleft🐺z");
    await Promise.all([left.flush(), right.flush()]);
    await Promise.all([left.synchronize(), right.synchronize()]);
    expect(first.state.doc.toString()).toBe("aleft🐺rightz");
    expect(second.state.doc.toString()).toBe(first.state.doc.toString());
    left.stepHistory("undo");
    await left.flush();
    await right.synchronize();
    expect(first.state.doc.toString()).toBe("a🐺rightz");
    expect(second.state.doc.toString()).toBe(first.state.doc.toString());
  });

  it("keeps typing while an agent change arrives without a snapshot replacement", async () => {
    const host = new DocumentFixture("first\nlast\n");
    const { replica } = await open(host);
    const view = viewFor(replica);
    view.dispatch({ changes: { from: 0, insert: "typed " }, selection: { anchor: 6 } });
    host.text.insert(host.text.length, "agent\n");
    host.wire.revision++;
    replica.receive(host.snapshot());
    expect(view.state.doc.toString()).toBe("typed first\nlast\nagent\n");
    expect(view.state.selection.main.head).toBe(6);
    await replica.flush();
    expect(host.text.toString()).toBe(view.state.doc.toString());
  });

  it("retries a lost acknowledgement with the same operation after a reload", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    const submit = vi.fn(async (...args: Parameters<typeof host.submit>) => {
      await host.submit(...args);
      throw new Error("connection ended before acknowledgement");
    });
    const { replica } = await open(host, outbox, { submitEditorDocumentUpdate: submit });
    replica.replaceLocal("base edited");
    await expect(replica.flush()).rejects.toThrow("acknowledgement");
    await replica.close(); replicas.splice(replicas.indexOf(replica), 1);
    const { replica: restored } = await open(host, outbox);
    await restored.flush();
    expect(host.wire.revision).toBe(2);
    expect(host.text.toString()).toBe("base edited");
    expect(restored.pendingCount).toBe(0);
  });

  it("retains updates in causal order when storage enumeration is reversed", async () => {
    const host = new DocumentFixture();
    const storage = memoryDocumentOutbox();
    const reversed: DocumentOutbox = { ...storage, read: async (id) => (await storage.read(id)).reverse() };
    const { replica } = await open(host, reversed, { submitEditorDocumentUpdate: vi.fn(async () => {
      throw new Error("offline");
    }) });
    replica.replaceLocal("base one"); replica.replaceLocal("base one two");
    await replica.preserve();
    await replica.close(); replicas.splice(replicas.indexOf(replica), 1);
    const { replica: restored } = await open(host, reversed);
    await restored.flush();
    expect(host.text.toString()).toBe("base one two");
    expect(restored.pendingCount).toBe(0);
  });

  it("automatically resumes preservation and delivery after local storage recovers", async () => {
    vi.useFakeTimers();
    const host = new DocumentFixture();
    const storage = memoryDocumentOutbox();
    let failed = true;
    const outbox = { ...storage, commit: async (records: OutboxRecord[], remove: OutboxRecord[] = []) => {
      if (records.some((record) => record.kind === "update") && failed) throw new Error("disk full");
      await storage.commit(records, remove);
    } };
    const submit = vi.fn(host.submit);
    const { replica } = await open(host, outbox, { submitEditorDocumentUpdate: submit });
    replica.replaceLocal("base preserved");
    await expect(replica.flush()).rejects.toThrow("disk full");
    expect(submit).not.toHaveBeenCalled();
    expect(replica.text.toString()).toBe("base preserved");
    expect(replica.preservationBlocked).toBe(true);
    failed = false;
    await vi.advanceTimersByTimeAsync(1500);
    expect(host.text.toString()).toBe("base preserved");
    expect(replica.preservationBlocked).toBe(false);
  });

  it("automatically resynchronizes and retries after interrupted delivery", async () => {
    vi.useFakeTimers();
    const host = new DocumentFixture();
    let online = false;
    const submit = vi.fn(async (...args: Parameters<typeof host.submit>) => {
      if (!online) throw new Error("connection interrupted");
      return host.submit(...args);
    });
    const { replica } = await open(host, memoryDocumentOutbox(), { submitEditorDocumentUpdate: submit });
    replica.replaceLocal("base automatically delivered");
    await vi.advanceTimersByTimeAsync(100);
    expect(replica.status).toBe("error");
    expect(replica.pendingCount).toBeGreaterThan(0);
    online = true;
    await vi.advanceTimersByTimeAsync(1500);
    expect(host.text.toString()).toBe("base automatically delivered");
    expect(replica.pendingCount).toBe(0);
    expect(replica.status).toBe("accepted");
  });

  it("replaces a clean imported epoch without combining retired CRDT identities", async () => {
    const host = new DocumentFixture("retired text with a longer clock");
    const { replica } = await open(host);
    const old = replica.doc;
    host.import("new");
    await replica.synchronize();
    expect(replica.doc).not.toBe(old);
    expect(replica.text.toString()).toBe("new");
    replica.replaceLocal("new typed");
    await replica.flush();
    expect(host.text.toString()).toBe("new typed");
  });

  it("sets pending edits aside one undo away when the host returns an incompatible history", async () => {
    const host = new DocumentFixture();
    const { replica } = await open(host);
    replica.replaceLocal("base pending");
    host.wire.epoch++;
    replica.receive(host.replace("outside"));
    expect(replica.text.toString()).toBe("outside");
    expect(replica.pendingCount).toBe(0);
    expect(replica.refusal).toMatch(/new history/u);
    await replica.flush();
    expect(host.text.toString()).toBe("outside");
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("base pending");
  });

  it("serializes simultaneous flushes without duplicate requests", async () => {
    const host = new DocumentFixture();
    const submit = vi.fn(host.submit);
    const { replica } = await open(host, memoryDocumentOutbox(), { submitEditorDocumentUpdate: submit });
    replica.replaceLocal("base edited");
    await Promise.all([replica.flush(), replica.flush(), replica.flush()]);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("ignores an older snapshot after accepting a newer revision", async () => {
    const host = new DocumentFixture();
    const older = host.snapshot();
    const { replica } = await open(host);
    replica.receive(host.replace("newer")); replica.receive(older);
    expect(replica.text.toString()).toBe("newer");
    expect(replica.accepted.revision).toBe(2);
  });
  it("propagates an in-flight command failure through the close barrier", async () => {
    let reject!: (error: Error) => void;
    const held = new Promise<void>((_resolve, fail) => { reject = fail; });
    const operation = runEditorDocumentOperation("command-document", () => held);
    const barrier = flushEditorDocumentDraft("command-document");
    const operationFailure = expect(operation).rejects.toThrow("metadata failed");
    const barrierFailure = expect(barrier).rejects.toThrow("metadata failed");
    reject(new Error("metadata failed"));
    await Promise.all([operationFailure, barrierFailure]);
  });

});


it.each(["unchanged", "changed", "metadata"])("preserves opening bytes when %s sync arrives before outbox initialization", async (arrival) => {
  const host = new DocumentFixture("func main() { /* café orchard */ }\n");
  const outbox = memoryDocumentOutbox();
  let release!: (records: OutboxRecord[]) => void;
  const read = new Promise<OutboxRecord[]>((resolve) => { release = resolve; });
  const changes: string[] = [];
  const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit });
  const replica = new DocumentReplica(host.snapshot(), () => client, (current) => changes.push(current.text.toString()),
    { ...outbox, read: () => read });
  replicas.push(replica);
  const opening = replica.initialize();
  expect(replica.initialized).toBe(false);
  expect(() => replica.replaceLocal("premature edit")).toThrow();
  try {
    if (arrival === "changed") host.replace("func main() { /* refreshed orchard */ }\n");
    if (arrival === "metadata") {
      await replica.receiveEvent({ ...host.snapshot(), content_changed: false, secret_screen_status: "complete" });
    } else {
      await replica.synchronize();
    }
    expect(changes).toEqual([]);
  } finally {
    release([]);
  }
  await opening;
  expect(replica.initialized).toBe(true);
  expect(replica.text.toString()).toBe(host.text.toString());
  expect(replica.history.state.doc.toString()).toBe(host.text.toString());
  expect(changes.length).toBeGreaterThan(0);
  expect(changes.every((text) => text === host.text.toString())).toBe(true);
  const view = viewFor(replica);
  expect(view.state.doc.toString()).toBe(host.text.toString());
  replica.replaceLocal(host.text.toString() + "// local edit\n");
  await replica.flush();
  expect(host.text.toString()).toBe(view.state.doc.toString());
});

describe("refused edits", () => {
  it("sets refused edits aside one undo away and returns to the host text", async () => {
    const host = new DocumentFixture();
    const outbox = memoryDocumentOutbox();
    let refuse = true;
    const submit = vi.fn(async (project: string, id: string, request: Parameters<typeof host.submit>[2]) => {
      if (refuse) throw new LycaonApiError("content exceeds the 4 MiB editor cap", 413, "source_content_too_large");
      return host.submit(project, id, request);
    });
    const { replica } = await open(host, outbox, { submitEditorDocumentUpdate: submit });
    const view = viewFor(replica);
    replica.replaceLocal("base refused");
    await replica.flush();
    expect(submit).toHaveBeenCalledTimes(1);
    expect(replica.text.toString()).toBe("base");
    expect(view.state.doc.toString()).toBe("base");
    expect(replica.pendingCount).toBe(0);
    expect(replica.status).toBe("accepted");
    expect(replica.refusal).toMatch(/4 MiB editor limit.*Undo restores/u);
    expect((await outbox.read(host.wire.id)).filter((record) => record.kind === "update" && !record.acknowledged)).toEqual([]);
    // The refused text is the next undo step, and once the host accepts it the refusal clears.
    refuse = false;
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("base refused");
    await replica.flush();
    expect(host.text.toString()).toBe("base refused");
    expect(replica.refusal).toBeNull();
    expect(replica.error).toBeNull();
  });

  it("refuses an edit that would exceed the host's cap before sending it", async () => {
    const host = new DocumentFixture();
    const { replica } = await open(host);
    const submit = vi.spyOn(host, "submit");
    expect(() => replica.replaceLocal("a".repeat(4 * 1024 * 1024 + 1))).toThrow(/4 MiB editor limit/u);
    expect(replica.text.toString()).toBe("base");
    await replica.flush();
    expect(submit).not.toHaveBeenCalled();
  });

  it("sets pending edits aside when the host starts a new epoch", async () => {
    const host = new DocumentFixture();
    const { replica } = await open(host, memoryDocumentOutbox(), { submitEditorDocumentUpdate: vi.fn(async () => { throw new BackendTransportError(new Error("offline"), "unreachable"); }) });
    replica.replaceLocal("base typed offline");
    await replica.preserve();
    expect(replica.pendingCount).toBe(1);
    replica.receive(host.import("rebased"));
    expect(replica.text.toString()).toBe("rebased");
    expect(replica.pendingCount).toBe(0);
    expect(replica.refusal).toMatch(/new history.*Undo restores/u);
    expect(replica.doc.clientID).toBe(replica.accepted.replica_id ?? replica.doc.clientID);
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe("base typed offline");
  });
});
