// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorSelection, EditorState, Transaction } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { indentMore } from "@codemirror/commands";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentReplica } from "../documents/document-replica.ts";
import { documentCommand } from "../documents/document-command.ts";
import { filesCollaborationExtension, rebindFilesCollaboration } from "../editor/files-editor-collaboration.ts";
import { encodeUpdate } from "../documents/document-outbox.ts";
import * as Y from "yjs";

const identity = vi.hoisted(() => ({ value: "window" }));
vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => identity.value }));
const replicas: DocumentReplica[] = [];
const views: EditorView[] = [];
afterEach(async () => {
  for (const view of views.splice(0)) view.destroy();
  for (const replica of replicas.splice(0)) await replica.close();
  identity.value = "window";
});
async function open(host: DocumentFixture, outbox = memoryDocumentOutbox(), overrides = {}) {
  const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit, ...overrides });
  const replica = new DocumentReplica(host.snapshot(), () => client, () => undefined, outbox);
  replicas.push(replica);
  await replica.initialize();
  const view = new EditorView({ state: EditorState.create({ doc: replica.text.toString(),
    extensions: [EditorState.allowMultipleSelections.of(true), replica.extension] }) });
  views.push(view);
  return { replica, view };
}
function type(view: EditorView, text: string, event = "input.type") {
  view.dispatch(view.state.replaceSelection(text), { userEvent: event });
}
function remote(host: DocumentFixture, replica: DocumentReplica, from: number, insert: string, remove = 0) {
  host.doc.transact(() => { if (remove) host.text.delete(from, remove); if (insert) host.text.insert(from, insert); });
  host.wire.revision++;
  replica.receive(host.snapshot());
}

describe("code editor document history", () => {
  it.each([false, true])("keeps unchanged identities across full-document paste and outside edits (undo/redo=%s)", async (cycleHistory) => {
    const base = "# Concurrent editing\n\nEditor line: untouched\nExternal line: untouched\n";
    const local = base.replace("Editor line: untouched", "Editor line: changed in app");
    const host = new DocumentFixture(base);
    const disk = new Y.Doc({ gc: false });
    disk.clientID = 42;
    Y.applyUpdate(disk, Y.encodeStateAsUpdate(host.doc));
    const { replica, view } = await open(host);
    view.dispatch({ changes: { from: 0, to: base.length, insert: local }, userEvent: "input.paste" });
    if (cycleHistory) {
      replica.stepHistory("undo"); expect(replica.text.toString()).toBe(base);
      replica.stepHistory("redo"); expect(replica.text.toString()).toBe(local);
    }
    await replica.flush();
    const index = base.lastIndexOf("untouched");
    disk.transact(() => { disk.getText("text").delete(index, "untouched".length); disk.getText("text").insert(index, "changed externally"); });
    Y.applyUpdate(host.doc, Y.encodeStateAsUpdate(disk));
    host.wire.revision++;
    replica.receive(host.snapshot());
    expect(replica.text.toString()).toBe(local.replace("External line: untouched", "External line: changed externally"));
    replica.stepHistory("undo");
    expect(replica.text.toString()).toBe(base.replace("External line: untouched", "External line: changed externally"));
    disk.destroy();
  });

  it("groups adjacent typing but separates navigation, paste, indentation, and deletion", async () => {
    const { replica, view } = await open(new DocumentFixture(""));
    type(view, "a"); type(view, "b");
    type(view, " pasted", "input.paste");
    type(view, "!");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("ab pasted");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("ab");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("");
    replica.stepHistory("redo");
    view.dispatch({ selection: { anchor: 0 }, userEvent: "select.pointer" });
    type(view, "x");
    indentMore(view);
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("xab");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("ab");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("");
  });

  it("preserves a peer insertion inside your text through undo and redo", async () => {
    const host = new DocumentFixture("AB");
    const { replica, view } = await open(host);
    view.dispatch({ selection: { anchor: 1 } }); type(view, "mine");
    await replica.flush();
    remote(host, replica, 3, "peer");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("ApeerB");
    replica.stepHistory("redo"); expect(replica.text.toString()).toBe("AmipeerneB");
    await replica.flush(); expect(host.text.toString()).toBe("AmipeerneB");
  });

  it("maps both stacks through remote replacements and drops edits a peer already removed", async () => {
    const host = new DocumentFixture("AB");
    const { replica, view } = await open(host);
    view.dispatch({ selection: { anchor: 1 } }); type(view, "mine"); await replica.flush();
    remote(host, replica, 1, "peer", 4);
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("ApeerB");
    replica.stepHistory("redo"); expect(replica.text.toString()).toBe("ApeerB");
  });

  it("restores every cursor and the primary range across remote edits", async () => {
    const host = new DocumentFixture("ab\ncd");
    const { replica, view } = await open(host);
    view.dispatch({ selection: EditorSelection.create([EditorSelection.cursor(1), EditorSelection.range(3, 5)], 1) });
    type(view, "X"); await replica.flush();
    remote(host, replica, 0, "peer\n");
    replica.stepHistory("undo");
    expect(view.state.doc.toString()).toBe("peer\nab\ncd");
    expect(view.state.selection.toJSON()).toEqual({ ranges: [{ anchor: 6, head: 6 }, { anchor: 8, head: 10 }], main: 1 });
    replica.stepHistory("redo"); expect(view.state.selection.ranges).toHaveLength(2);
  });

  it("preserves both stacks through close, reload, and changes received while closed", async () => {
    const host = new DocumentFixture("AB");
    const outbox = memoryDocumentOutbox();
    const { replica, view } = await open(host, outbox);
    view.dispatch({ selection: { anchor: 1 } }); type(view, "mine"); await replica.flush();
    replica.stepHistory("undo"); await replica.flush(); await replica.close();
    host.text.insert(1, "peer"); host.wire.revision++;
    const { replica: reopened } = await open(host, outbox);
    reopened.stepHistory("redo"); expect(reopened.text.toString()).toContain("peer");
    reopened.stepHistory("undo"); expect(reopened.text.toString()).toBe("ApeerB");
    await reopened.flush(); await reopened.close();
    const { replica: again } = await open(host, outbox);
    again.stepHistory("redo"); expect(again.text.toString()).toContain("mine");
  });

  it("restores all recovered cursors when an existing view joins the document", async () => {
    const host = new DocumentFixture("ab\ncd");
    const outbox = memoryDocumentOutbox();
    const { replica, view } = await open(host, outbox);
    view.dispatch({ selection: EditorSelection.create([EditorSelection.cursor(1), EditorSelection.cursor(4)], 1) });
    type(view, "X");
    const selection = view.state.selection.toJSON();
    await replica.flush();
    await replica.close();
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit });
    const recovered = new DocumentReplica(host.snapshot(), () => client, () => undefined, outbox);
    replicas.push(recovered);
    await recovered.initialize();
    const waiting = new EditorView({ state: EditorState.create({ doc: host.text.toString(),
      extensions: [EditorState.allowMultipleSelections.of(true), filesCollaborationExtension(undefined)] }) });
    views.push(waiting);
    rebindFilesCollaboration(waiting, recovered);
    expect(waiting.state.selection.toJSON()).toEqual(selection);
    expect(recovered.history.state.selection.toJSON()).toEqual(selection);
  });

  it("does not transfer keyboard history to a different window", async () => {
    const host = new DocumentFixture("AB"); const outbox = memoryDocumentOutbox();
    const { replica, view } = await open(host, outbox);
    type(view, "mine"); await replica.flush(); await replica.close();
    identity.value = "peer";
    const { replica: peer } = await open(host, outbox);
    peer.stepHistory("undo"); expect(peer.text.toString()).toBe("mineAB");
  });

  it("records an exact human command without capturing peer context or later edits", async () => {
    const host = new DocumentFixture("AagentB");
    const { replica, view } = await open(host, memoryDocumentOutbox(), {
      revertEditorDocumentChange: async (_p: string, _d: string, _c: string, request: { operation_id: string; history_vector: string }) => {
        host.text.insert(0, "peer");
        const before = encodeUpdate(Y.encodeStateAsUpdate(host.doc, Uint8Array.from(atob(request.history_vector), c => c.charCodeAt(0))));
        const vector = Y.encodeStateVector(host.doc);
        host.text.delete(9, 5);
        const update = encodeUpdate(Y.encodeStateAsUpdate(host.doc, vector));
        host.text.insert(host.text.length, "later"); host.wire.revision += 3;
        return { ...host.snapshot(), command_history: { operation_id: request.operation_id, epoch: 1, before_update: before, update } };
      },
    });
    view.dispatch({ selection: { anchor: 0 } }); type(view, "mine"); await replica.flush();
    // Agent text follows the newly inserted human prefix and the peer prefix.
    await replica.executeCommand(documentCommand(host.snapshot(), { action: "revert", changeId: crypto.randomUUID(), request: {
      client_id: "window", operation_id: crypto.randomUUID(), epoch: 1,
    } }));
    const changed = replica.text.toString();
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("peermineAagentBlater");
    replica.stepHistory("redo"); expect(replica.text.toString()).toBe(changed);
  });
  it("recovers a lost command acknowledgement into keyboard history exactly once", async () => {
    const host = new DocumentFixture("agent"); const outbox = memoryDocumentOutbox();
    let receipt: ReturnType<typeof host.snapshot> | undefined;
    let attempts = 0;
    const invoke = vi.fn(async (_p: string, _d: string, _c: string, request: { operation_id: string }) => {
      if (!receipt) {
        const before = encodeUpdate(Y.encodeStateAsUpdate(host.doc, Y.encodeStateVector(host.doc)));
        const vector = Y.encodeStateVector(host.doc);
        host.text.delete(0, 5); host.wire.revision++;
        receipt = { ...host.snapshot(), command_history: { operation_id: request.operation_id, epoch: 1,
          before_update: before, update: encodeUpdate(Y.encodeStateAsUpdate(host.doc, vector)) } };
      }
      if (attempts++ === 0) throw new Error("lost acknowledgement");
      return receipt;
    });
    const { replica } = await open(host, outbox, { revertEditorDocumentChange: invoke });
    await expect(replica.executeCommand(documentCommand(host.snapshot(), { action: "revert", changeId: crypto.randomUUID(), request: {
      client_id: "window", operation_id: crypto.randomUUID(), epoch: 1,
    } }))).rejects.toThrow("lost acknowledgement");
    const allocatedId = replica.doc.clientID;
    await replica.close();
    const preserved = await outbox.read(host.wire.id);
    const pendingCommand = preserved.find(record => record.kind === "command");
    const beforeRecovery = preserved.find(record => record.kind === "checkpoint") ?? (pendingCommand?.kind === "command" ? pendingCommand.historyBase : undefined);
    expect(beforeRecovery?.kind).toBe("checkpoint");
    if (beforeRecovery?.kind !== "checkpoint") throw new Error("Expected a recovery checkpoint");
    const offline = new DocumentReplica(beforeRecovery.confirmed, () => null, () => {}, outbox);
    replicas.push(offline);
    await offline.initialize(undefined, true);
    expect(offline.currentText).toBe("agent");
    expect(offline.awaitingCommand).toBe(true);
    expect(() => offline.replaceLocal("unsafe edit")).toThrow("cannot accept changes");
    expect(invoke).toHaveBeenCalledTimes(1);
    await offline.close();
    host.text.insert(0, "peer"); host.wire.revision++;
    const { replica: reopened } = await open(host, outbox, { revertEditorDocumentChange: invoke });
    expect(invoke.mock.calls[1]).toEqual(invoke.mock.calls[0]);
    expect(reopened.text.toString()).toBe("peer");
    const adopted = (await outbox.read(host.wire.id)).find(record => record.kind === "checkpoint");
    expect(adopted?.kind === "checkpoint" && adopted.replicaId).toBe(allocatedId);
    reopened.stepHistory("undo"); expect(reopened.text.toString()).toContain("agent");
    expect(reopened.text.toString()).toContain("peer");
    reopened.stepHistory("redo"); expect(reopened.text.toString()).toBe("peer");
    await reopened.flush(); await reopened.close();
    const { replica: again } = await open(host, outbox, { revertEditorDocumentChange: invoke });
    expect(invoke).toHaveBeenCalledTimes(2);
    again.stepHistory("undo"); expect(again.text.toString()).toContain("agent");
  });

  it("handles keyboard and native undo and refuses undo while input is blocked", async () => {
    const { replica, view } = await open(new DocumentFixture(""));
    type(view, "typed");
    view.contentDOM.dispatchEvent(new InputEvent("beforeinput", { inputType: "historyUndo", bubbles: true, cancelable: true }));
    expect(replica.text.toString()).toBe("");
    view.contentDOM.dispatchEvent(new InputEvent("beforeinput", { inputType: "historyRedo", bubbles: true, cancelable: true }));
    expect(replica.text.toString()).toBe("typed");
    replica.preservationBlocked = true;
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("typed");
    replica.preservationBlocked = false;
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("");
  });

  it("keeps a composition together across pauses and separates a following deletion", async () => {
    const { replica, view } = await open(new DocumentFixture(""));
    view.dispatch({ changes: { from: 0, insert: "n" }, selection: { anchor: 1 }, userEvent: "input.type.compose.start", annotations: Transaction.time.of(1000) });
    view.dispatch({ changes: { from: 0, to: 1, insert: "你" }, selection: { anchor: 1 }, userEvent: "input.type.compose", annotations: Transaction.time.of(4000) });
    view.dispatch({ changes: { from: 0, to: 1 }, selection: { anchor: 0 }, userEvent: "delete.backward", annotations: Transaction.time.of(4010) });
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("你");
    replica.stepHistory("undo"); expect(replica.text.toString()).toBe("");
  });

  it("leaves corrupt history untouched when initialization and cleanup fail", async () => {
    const host = new DocumentFixture("base"); const outbox = memoryDocumentOutbox();
    const { replica } = await open(host, outbox);
    replica.replaceLocal("base typed");
    await replica.flush();
    await replica.close();
    const checkpoint = (await outbox.read(host.wire.id)).find((record) => record.kind === "checkpoint")!;
    await outbox.put({ ...checkpoint, history: { history: { done: [{ changes: "corrupt", startSelection: null, mapped: [] }], undone: [] }, selection: { ranges: [{ anchor: 0, head: 0 }], main: 0 } } });
    const before = await outbox.read(host.wire.id);
    const broken = new DocumentReplica(host.snapshot(), () => stubClient({ syncEditorDocument: host.sync }), () => {}, outbox);
    await expect(broken.initialize()).rejects.toThrow();
    await broken.close();
    expect(await outbox.read(host.wire.id)).toEqual(before);
  });

  it("replaces an imported epoch once and starts fresh history in the existing view", async () => {
    const host = new DocumentFixture("old");
    const { replica, view } = await open(host);
    type(view, "mine"); await replica.flush();
    host.import("new epoch");
    await replica.synchronize();
    expect(view.state.doc.toString()).toBe("new epoch");
    replica.stepHistory("undo"); expect(view.state.doc.toString()).toBe("new epoch");
    type(view, "typed ");
    replica.stepHistory("undo"); expect(view.state.doc.toString()).toBe("new epoch");
  });

});
