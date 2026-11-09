// @vitest-environment jsdom
import { EditorSelection, EditorState, Transaction } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import * as Y from "yjs";
import { DocumentReplica } from "../../../files/documents/document-replica.ts";
import { encodeUpdate } from "../../../files/documents/document-outbox.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { DocumentFixture } from "../../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../../test/memory-document-outbox.ts";
import { PrettyReplicaEditorAdapter } from "./pretty-replica-adapter.ts";
import {
  prettyRecoveryStorageKey,
  resetPrettyDocumentRecoveryStoreForTests,
  simulatePrettyProcessRestartForTests,
} from "./pretty-replica-rebase.ts";

const identity = vi.hoisted(() => ({ value: "window-pretty-proof" }));
vi.mock("../../../platform/connection/client-identity.ts", () => ({
  clientIdentity: () => identity.value,
  prepareClientIdentity: async () => identity.value,
}));

const replicas: DocumentReplica[] = [];
const adapters: PrettyReplicaEditorAdapter[] = [];
const views: EditorView[] = [];

afterEach(async () => {
  for (const adapter of adapters.splice(0)) adapter.destroy();
  for (const view of views.splice(0)) {
    const parent = view.dom.parentElement;
    view.destroy();
    parent?.remove();
  }
  for (const replica of replicas.splice(0)) await replica.close();
  resetPrettyDocumentRecoveryStoreForTests();
  localStorage.clear();
  vi.useRealTimers();
});

async function mountPrettyEditor(
  initialRawText: string,
  path = "config.json",
  incarnation?: string,
) {
  const fixture = new DocumentFixture();
  const outbox = memoryDocumentOutbox();
  const publishPresence = vi.fn(async () => undefined);
  const client = stubClient({
    syncEditorDocument: fixture.sync,
    submitEditorDocumentUpdate: fixture.submit,
    publishEditorDocumentPresence: publishPresence,
  });
  const snap = fixture.snapshot();
  snap.path = path;
  const replica = new DocumentReplica(
    snap,
    () => client,
    () => undefined,
    outbox,
    undefined,
    incarnation,
  );
  replicas.push(replica);
  await replica.initialize();
  if (initialRawText.length > 0) {
    replica.replaceLocal(initialRawText);
    await replica.flush();
  }

  const adapter = new PrettyReplicaEditorAdapter(replica, { path });
  adapters.push(adapter);

  const parent = document.createElement("div");
  document.body.append(parent);
  const view = new EditorView({
    parent,
    state: EditorState.create({
      doc: adapter.currentProjection.displayText,
      extensions: [adapter.extension],
    }),
  });
  views.push(view);
  adapter.attach(view);

  return { fixture, outbox, replica, adapter, view, client, publishPresence };
}

function applyRemotePeerEdit(
  replica: DocumentReplica,
  mutate: (text: Y.Text) => void,
): void {
  const peer = new Y.Doc();
  peer.clientID = 9999;
  Y.applyUpdate(peer, Y.encodeStateAsUpdate(replica.doc));
  mutate(peer.getText("text"));
  const deltaUpdate = Y.encodeStateAsUpdate(peer, Y.encodeStateVector(replica.doc));
  peer.destroy();
  replica.receive({
    ...replica.accepted,
    revision: replica.accepted.revision + 1,
    crdt_update: encodeUpdate(deltaUpdate),
  });
}

describe("PrettyReplicaEditorAdapter real EditorView + DocumentReplica proof (FMT-AC-07, FMT-AC-10, FMT-AC-16)", () => {
  it("Case 1: edits a formatted token while preserving valid 1.20e+3 lexeme, toggles Pretty/Raw without mutating bytes/dirty/history, and falls back to raw on leading-zero 01.20e+3", async () => {
    const raw = '{"a":1,"b":1.20e+3}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case1.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 1.20e+3\n}');
    const revBeforeToggle = replica.history.revision;

    // Mode toggle Pretty -> Raw -> Pretty never mutates bytes, dirty state, or history revision.
    adapter.setPrettyEnabled(false);
    expect(view.state.doc.toString()).toBe(raw);
    expect(replica.currentText).toBe(raw);
    expect(replica.history.revision).toBe(revBeforeToggle);

    adapter.setPrettyEnabled(true);
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 1.20e+3\n}');
    expect(replica.history.revision).toBe(revBeforeToggle);

    // Replace `1` at disp [9, 10) with `42`.
    const oneDisp = view.state.doc.toString().indexOf("1,");
    expect(oneDisp).toBe(9);
    view.dispatch({
      changes: { from: oneDisp, to: oneDisp + 1, insert: "42" },
      userEvent: "input.type",
    });

    expect(replica.currentText).toBe('{"a":42,"b":1.20e+3}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 42,\n  "b": 1.20e+3\n}');

    // Undo restores `{"a":1,"b":1.20e+3}` and re-projects.
    adapter.stepHistory("undo");
    expect(replica.currentText).toBe(raw);
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 1.20e+3\n}');

    // Typing a leading zero before `1.20e+3` (`01.20e+3`) is invalid strict JSON and transitions to raw-backed presentation.
    const numDisp = view.state.doc.toString().indexOf("1.20e+3");
    view.dispatch({
      changes: { from: numDisp, to: numDisp, insert: "0" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":1,"b":01.20e+3}');
    expect(view.state.doc.toString()).toBe('{"a":1,"b":01.20e+3}');
  });

  it("Case 2a & 2b: replaces enclosing source interval across tokens and treats virtual-only delete/cut as zero-token no-op", async () => {
    const raw = '{"a": 1,   "b": 2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case2.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');
    const revInitial = replica.history.revision;

    // Case 2b: Select virtual-only `\n  ` between `,` and `"b"` (disp [11, 14)) and cut/delete.
    const virtFrom = view.state.doc.toString().indexOf(",\n  ") + 1;
    const virtTo = view.state.doc.toString().indexOf('"b"');
    expect(virtFrom).toBe(11);
    expect(virtTo).toBe(14);

    view.dispatch({
      selection: EditorSelection.range(virtFrom, virtTo),
    });
    expect(adapter.copySelection()).toEqual({ displayText: "\n  ", rawText: "" });

    view.dispatch({
      changes: { from: virtFrom, to: virtTo, insert: "" },
      userEvent: "delete.cut",
    });
    expect(replica.currentText).toBe(raw);
    expect(replica.history.revision).toBe(revInitial);
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');
    expect(adapter.lastCopiedSelection).toEqual({ displayText: "\n  ", rawText: "" });

    // Case 2a: Select from `1` through `"b"` (disp [9, 17)) and replace with `10, "c"`.
    const selFrom = view.state.doc.toString().indexOf("1,");
    const selTo = view.state.doc.toString().indexOf('"b"') + '"b"'.length;
    expect(selFrom).toBe(9);
    expect(selTo).toBe(17);

    view.dispatch({
      selection: EditorSelection.range(selFrom, selTo),
    });
    expect(adapter.copySelection()).toEqual({
      displayText: '1,\n  "b"',
      rawText: '1,   "b"',
    });

    view.dispatch({
      changes: { from: selFrom, to: selTo, insert: '10, "c"' },
      userEvent: "input.paste",
    });
    expect(replica.currentText).toBe('{"a": 10, "c": 2}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 10,\n  "c": 2\n}');
  });

  it("Case 3a & 3b: collapsed Backspace/Delete in generated layout moves to raw boundary with view-local affinity that survives remote refresh, without overwriting separate views", async () => {
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case3.json");

    // Mount a second EditorView + adapter on the same DocumentReplica to verify view-local affinity isolation.
    const adapter2 = new PrettyReplicaEditorAdapter(replica, { path: "case3.json" });
    adapters.push(adapter2);
    const parent2 = document.createElement("div");
    document.body.append(parent2);
    const view2 = new EditorView({
      parent: parent2,
      state: EditorState.create({
        doc: adapter2.currentProjection.displayText,
        extensions: [adapter2.extension],
      }),
    });
    views.push(view2);
    adapter2.attach(view2);

    // Move view2 to the `"after"` edge of `\n  ` (disp = 4, raw = 1, assoc = "after").
    view2.dispatch({
      changes: { from: 1, to: 2, insert: "" },
      userEvent: "delete.forward",
    });
    expect(view2.state.selection.main.head).toBe(4);
    expect(adapter2.caretAffinity).toEqual({ rawOffset: 1, assoc: "after" });

    // Step 1 Backspace on view1 at disp = 4: moves view1 caret to disp = 1 (src = 1, assoc = "before")
    // without overwriting view2's `"after"` affinity!
    const revBefore = replica.history.revision;
    view.dispatch({
      changes: { from: 3, to: 4, insert: "" },
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe(raw);
    expect(replica.history.revision).toBe(revBefore);
    expect(view.state.selection.main.head).toBe(1);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "before" });
    expect(adapter2.caretAffinity).toEqual({ rawOffset: 1, assoc: "after" });
    expect(view2.state.selection.main.head).toBe(4);

    // Remote refresh modifies `"b":2` -> `"b":20` while view1 has "before" and view2 has "after".
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf('"b":2') + '"b":'.length;
      ytext.delete(idx, 1);
      ytext.insert(idx, "20");
    });
    expect(replica.currentText).toBe('{"a":1,"b":20}');
    // Both views keep their respective display positions (view1 at 1, view2 at 4)!
    expect(view.state.selection.main.head).toBe(1);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "before" });
    expect(view2.state.selection.main.head).toBe(4);
    expect(adapter2.caretAffinity).toEqual({ rawOffset: 1, assoc: "after" });

    // Step 2 Backspace on view1 at disp = 1: deletes `{` at raw [0, 1) and transitions to raw-backed presentation.
    view.dispatch({
      changes: { from: 0, to: 1, insert: "" },
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe('"a":1,"b":20}');
    expect(view.state.doc.toString()).toBe('"a":1,"b":20}');

    // Undo restores `{"a":1,"b":20}` in Pretty mode; now test forward Delete (Case 3b).
    adapter.stepHistory("undo");
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 20\n}');

    view.dispatch({
      changes: { from: 1, to: 2, insert: "" },
      userEvent: "delete.forward",
    });
    expect(view.state.selection.main.head).toBe(4);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "after" });

    view.dispatch({
      changes: { from: 4, to: 5, insert: "" },
      userEvent: "delete.forward",
    });
    expect(replica.currentText).toBe('{a":1,"b":20}');
    expect(view.state.doc.toString()).toBe('{a":1,"b":20}');
  });

  it("Case 4: deliberate pasted whitespace at virtual boundary holds segment in raw-backed presentation, while space inside string stays formatted", async () => {
    const raw = '{"a":"hello"}';
    const { replica, view } = await mountPrettyEditor(raw, "case4.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": "hello"\n}');

    const keyDisp = view.state.doc.toString().indexOf('"a"');
    expect(keyDisp).toBe(4);
    view.dispatch({
      changes: { from: keyDisp, to: keyDisp, insert: "   " },
      userEvent: "input.paste",
    });
    expect(replica.currentText).toBe('{   "a":"hello"}');
    expect(view.state.doc.toString()).toBe('{   "a":"hello"}');

    const helloEndDisp = '{\n  "a": "hello'.length;
    view.dispatch({
      changes: { from: 1, to: 4, insert: "" },
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe('{"a":"hello"}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "hello"\n}');

    view.dispatch({
      changes: { from: helloEndDisp, to: helloEndDisp, insert: " world" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":"hello world"}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "hello world"\n}');
  });

  it("Case 5: JSONL record-delimiter deletion at disp [12, 13) deletes actual source newline", async () => {
    const raw = '{"a":1}\n{"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case5.jsonl");

    const delimRun = adapter.currentProjection.sourceMap.runs.find(
      (r) => r.kind === "record-delimiter",
    )!;
    expect(delimRun.dispFrom).toBe(12);
    expect(delimRun.dispTo).toBe(13);

    view.dispatch({
      changes: { from: delimRun.dispFrom, to: delimRun.dispTo, insert: "" },
      userEvent: "delete.forward",
    });
    expect(replica.currentText).toBe('{"a":1}{"b":2}');
    expect(view.state.doc.toString()).toBe('{"a":1}{"b":2}');
  });

  it("Case 6a & 6b: coalesces identical multi-cursor insertions, preserves unresolved intentions across invalid choice, refused admission, and single-intention application, and rebases or rejects on intervening changes", async () => {
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case6.json");

    // Case 6a: Identical insertions at disp = 2 and disp = 3 coalesce into a single insertion at raw offset 1.
    view.dispatch({
      changes: [
        { from: 2, to: 2, insert: '"x":0,' },
        { from: 3, to: 3, insert: '"x":0,' },
      ],
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"x":0,"a":1,"b":2}');
    expect(view.state.doc.toString()).toBe('{\n  "x": 0,\n  "a": 1,\n  "b": 2\n}');

    adapter.stepHistory("undo");
    expect(replica.currentText).toBe(raw);
    const revBeforeConflict = replica.history.revision;

    // Case 6b: Conflicting insertions inside virtual `\n  ` before `"b"` (disp = 12 and disp = 13 -> raw = 7).
    view.dispatch({
      changes: [
        { from: 12, to: 12, insert: "X" },
        { from: 13, to: 13, insert: "Y" },
      ],
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe(raw);
    expect(replica.history.revision).toBe(revBeforeConflict);
    expect(adapter.pendingConflicts).toHaveLength(1);
    expect(adapter.pendingConflict?.intentions).toEqual([
      {
        order: 0,
        dispFrom: 12,
        dispTo: 12,
        rawFrom: 7,
        rawTo: 7,
        insert: "X",
        baseIncarnation: replica.incarnation,
        baseEpoch: replica.accepted.epoch,
        baseHistoryRevision: revBeforeConflict,
      },
      {
        order: 1,
        dispFrom: 13,
        dispTo: 13,
        rawFrom: 7,
        rawTo: 7,
        insert: "Y",
        baseIncarnation: replica.incarnation,
        baseEpoch: replica.accepted.epoch,
        baseHistoryRevision: revBeforeConflict,
      },
    ]);

    // 1. Invalid choice does NOT clear or lose any retained intentions.
    expect(adapter.resolvePendingConflict({ action: "apply", order: 99 })).toEqual({
      ok: false,
      status: "invalid-choice",
    });
    expect(adapter.resolvePendingConflict({ action: "discard-intention", order: 99 })).toEqual({
      ok: false,
      status: "invalid-choice",
    });
    expect(adapter.pendingConflict?.intentions).toHaveLength(2);

    // 2. Refused admission (storage / preservation blocked) does NOT clear or lose any retained intentions.
    replica.preservationBlocked = true;
    const refused = adapter.resolvePendingConflict({ action: "apply", order: 0 });
    expect(refused.ok).toBe(false);
    expect(refused.status).toBe("admission-refused");
    expect(adapter.pendingConflict?.intentions).toHaveLength(2);
    expect(replica.currentText).toBe(raw);
    replica.preservationBlocked = false;

    // 3. Disjoint remote shift before conflict resolution:
    // Peer inserts `"z":0,` (6 chars) at raw offset 1, shifting raw offset 7 (`"b":2`) to 13!
    applyRemotePeerEdit(replica, (ytext) => {
      ytext.insert(1, '"z":0,');
    });
    expect(replica.currentText).toBe('{"z":0,"a":1,"b":2}');

    // Applying intention 0 ("X") without discardRemainingIntentions rebases 7 -> 13, admits "X" at 13,
    // and preserves intention 1 ("Y") rebased to 14!
    const applied0 = adapter.resolvePendingConflict({ action: "apply", order: 0 });
    expect(applied0).toEqual({
      ok: true,
      status: "applied",
      conflictId: expect.any(String),
      remainingIntentions: 1,
      remainingConflicts: 1,
    });
    expect(replica.currentText).toBe('{"z":0,"a":1,X"b":2}');
    expect(adapter.pendingConflict?.intentions).toEqual([
      {
        order: 1,
        dispFrom: 13,
        dispTo: 13,
        rawFrom: 14,
        rawTo: 14,
        insert: "Y",
        baseIncarnation: replica.incarnation,
        baseEpoch: replica.accepted.epoch,
        baseHistoryRevision: replica.history.revision,
      },
    ]);

    // Applying the remaining intention 1 ("Y") admits "Y" at 14 and clears the resolved conflict!
    const applied1 = adapter.resolvePendingConflict({ action: "apply", order: 1 });
    expect(applied1).toEqual({
      ok: true,
      status: "applied",
      conflictId: expect.any(String),
      remainingIntentions: 0,
      remainingConflicts: 0,
    });
    expect(replica.currentText).toBe('{"z":0,"a":1,XY"b":2}');
    expect(adapter.pendingConflict).toBeNull();
  });

  it("Case 6c: rejects stale conflict resolution on remote overlap, preserves repeated conflicts across process restart, and isolates document incarnations", async () => {
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(
      raw,
      "conflict-durable.json",
      "incarnation-alpha",
    );

    // Trigger Conflict #1 at disp 2 & 3 (raw offset 1, before `"a":1`).
    view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "A" },
        { from: 3, to: 3, insert: "B" },
      ],
      userEvent: "input.type",
    });
    expect(adapter.pendingConflicts).toHaveLength(1);

    // Switch Pretty off and back on (clearing transient raw segments while durable conflict #1 remains),
    // or trigger Conflict #2 on another formatted document region to verify repeated conflicts do not overwrite!
    // First, discard conflict #1's raw segment hold by re-enabling Pretty on a two-segment JSONL doc, or trigger a second conflict:
    adapter.setPrettyEnabled(false);
    adapter.setPrettyEnabled(true);
    // Notice Conflict #1 is STILL in adapter.pendingConflicts!
    expect(adapter.pendingConflicts).toHaveLength(1);

    // Remote peer edits raw [1, 4) (`"a"` -> `"aa"`), overlapping Conflict #1 at raw offset 1!
    applyRemotePeerEdit(replica, (ytext) => {
      ytext.delete(1, 3);
      ytext.insert(1, '"aa"');
    });
    expect(replica.currentText).toBe('{"aa":1,"b":2}');

    // Attempting to apply Conflict #1 order 0 is rejected with `overlap-conflict` and preserves Conflict #1!
    const overlapRes = adapter.resolvePendingConflict({ action: "apply", order: 0 });
    expect(overlapRes).toEqual({
      ok: false,
      status: "overlap-conflict",
    });
    expect(replica.currentText).toBe('{"aa":1,"b":2}');
    expect(adapter.pendingConflicts).toHaveLength(1);
    expect(adapter.pendingConflict?.intentions).toHaveLength(2);

    // Explicit discard of intention 0 leaves intention 1; explicit discard of conflict removes it only when requested.
    // Before discarding, trigger a second conflict so `pendingConflicts` holds 2 distinct conflicts simultaneously!
    const firstConflictId = adapter.pendingConflicts[0]!.conflictId;
    adapter.resolvePendingConflict({
      action: "discard-conflict",
      conflictId: firstConflictId,
    });
    expect(adapter.pendingConflicts).toHaveLength(0);

    // Now on a two-record JSONL document, trigger two consecutive conflicts and verify both persist across process restart!
    const jsonlMount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}',
      "multi-conflict.jsonl",
      "incarnation-persist-1",
    );
    // Conflict #1 in record 1 (disp 2 & 3 -> raw 1):
    jsonlMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "C1a" },
        { from: 3, to: 3, insert: "C1b" },
      ],
      userEvent: "input.type",
    });
    expect(jsonlMount.adapter.pendingConflicts).toHaveLength(1);

    // Record 2 (`{\n  "b": 2\n}`) is still formatted! Trigger Conflict #2 in record 2:
    const rec2Start = jsonlMount.view.state.doc.toString().indexOf("{\n  \"b\"");
    expect(rec2Start).toBeGreaterThan(0);
    jsonlMount.view.dispatch({
      changes: [
        { from: rec2Start + 2, to: rec2Start + 2, insert: "C2a" },
        { from: rec2Start + 3, to: rec2Start + 3, insert: "C2b" },
      ],
      userEvent: "input.type",
    });
    // Both conflicts are retained in order without overwriting!
    expect(jsonlMount.adapter.pendingConflicts).toHaveLength(2);
    expect(jsonlMount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("C1a");
    expect(jsonlMount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("C2a");

    // Verify durable storage contains the serialized recovery record for (documentId, incarnation-persist-1).
    const storageKey = prettyRecoveryStorageKey(
      jsonlMount.replica.accepted.id,
      "incarnation-persist-1",
    );
    expect(localStorage.getItem(storageKey)).toBeTruthy();

    // Simulate process loss (clears in-memory recovery Map while keeping durable storage).
    jsonlMount.adapter.destroy();
    simulatePrettyProcessRestartForTests();

    // A different incarnation of the same document ID has isolated recovery state (empty).
    const otherIncReplica = new DocumentReplica(
      jsonlMount.fixture.snapshot(),
      () => jsonlMount.client,
      () => undefined,
      jsonlMount.outbox,
      undefined,
      "incarnation-other",
    );
    replicas.push(otherIncReplica);
    await otherIncReplica.initialize();
    const otherIncAdapter = new PrettyReplicaEditorAdapter(otherIncReplica, {
      path: "multi-conflict.jsonl",
    });
    adapters.push(otherIncAdapter);
    expect(otherIncAdapter.pendingConflicts).toHaveLength(0);

    // Restoring the original incarnation (`incarnation-persist-1`) restores both unresolved conflicts from durable storage!
    const restoredAdapter = new PrettyReplicaEditorAdapter(jsonlMount.replica, {
      path: "multi-conflict.jsonl",
    });
    adapters.push(restoredAdapter);
    expect(restoredAdapter.pendingConflicts).toHaveLength(2);
    expect(restoredAdapter.pendingConflicts[0]!.intentions.map((i) => i.insert)).toEqual([
      "C1a",
      "C1b",
    ]);
    expect(restoredAdapter.pendingConflicts[1]!.intentions.map((i) => i.insert)).toEqual([
      "C2a",
      "C2b",
    ]);

    // Explicitly apply C1a and discard C1b (`discardRemainingIntentions: true`) on conflict #1:
    const resC1 = restoredAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: restoredAdapter.pendingConflicts[0]!.conflictId,
      order: 0,
      discardRemainingIntentions: true,
    });
    expect(resC1.ok).toBe(true);
    expect(restoredAdapter.pendingConflicts).toHaveLength(1);
    expect(jsonlMount.replica.currentText).toBe('{C1a"a":1}\n{"b":2}');

    // Conflict #2 was at raw offset 9 (`{"a":1}\n{` -> offset 9); applying C1a (3 chars at offset 1)
    // recorded a delta step so Conflict #2 rebases from 9 -> 12!
    const resC2 = restoredAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: restoredAdapter.pendingConflicts[0]!.conflictId,
      order: 1,
      discardRemainingIntentions: true,
    });
    expect(resC2.ok).toBe(true);
    expect(restoredAdapter.pendingConflicts).toHaveLength(0);
    expect(jsonlMount.replica.currentText).toBe('{C1a"a":1}\n{C2b"b":2}');
    expect(localStorage.getItem(storageKey)).toBeNull();
  });

  it("Case 7a & 7b: IME composition WITHOUT pre-keydown (compositionstart first) and WITH Process/229 keydown preserves the active composing DOM node across multi-step compositionupdate frames", async () => {
    const raw = '{"a":"hello","b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case7.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": "hello",\n  "b": 2\n}');

    // Grab the actual DOM `.cm-line` element for line 2 (`  "a": "hello",`) BEFORE composition starts.
    const linesBefore = view.contentDOM.querySelectorAll(".cm-line");
    expect(linesBefore.length).toBe(4);
    const composingLineBefore = linesBefore[1]!;
    expect(composingLineBefore.textContent).toBe('  "a": "hello",');

    // Case 7a: Start composition directly with `compositionstart` (NO prior Process/229 keydown!).
    view.contentDOM.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    expect(adapter.isComposing).toBe(true);
    // Composing DOM line node is NOT replaced on `compositionstart`!
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);

    // Frame 1: `compositionupdate("ね")` inserting `"ね"` after `"hello"` (disp offset 15).
    view.contentDOM.dispatchEvent(
      new CompositionEvent("compositionupdate", { data: "ね", bubbles: true }),
    );
    const helloEnd = view.state.doc.toString().indexOf('hello"') + 5;
    expect(helloEnd).toBe(15);
    view.dispatch({
      changes: { from: helloEnd, to: helloEnd, insert: "ね" },
      selection: EditorSelection.single(helloEnd + 1),
      userEvent: "input.type.compose",
    });
    expect(replica.currentText).toBe('{"a":"helloね","b":2}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "helloね",\n  "b": 2\n}');
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);

    // Frame 2: `compositionupdate("ねこ")` replacing `"ね"` (disp [15, 16)) with `"ねこ"`.
    view.contentDOM.dispatchEvent(
      new CompositionEvent("compositionupdate", { data: "ねこ", bubbles: true }),
    );
    view.dispatch({
      changes: { from: 15, to: 16, insert: "ねこ" },
      selection: EditorSelection.single(17),
      userEvent: "input.type.compose",
    });
    expect(replica.currentText).toBe('{"a":"helloねこ","b":2}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "helloねこ",\n  "b": 2\n}');
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);

    // Frame 3 (commit): replacing `"ねこ"` (disp [15, 17)) with `"猫"`, followed by `compositionend`.
    view.dispatch({
      changes: { from: 15, to: 17, insert: "猫" },
      selection: EditorSelection.single(16),
      userEvent: "input.type.compose",
    });
    view.contentDOM.dispatchEvent(
      new CompositionEvent("compositionend", { data: "猫", bubbles: true }),
    );
    expect(adapter.isComposing).toBe(false);
    expect(replica.currentText).toBe('{"a":"hello猫","b":2}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "hello猫",\n  "b": 2\n}');
    // Even after `compositionend`, because the settled formatted text matches `view.state.doc`,
    // the `.cm-line` DOM node is still the exact same element!
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);

    // Case 7b: Pre-IME keydown (`Process` / 229) followed by multi-frame composition also preserves `.cm-line` DOM node!
    view.contentDOM.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Process", keyCode: 229, bubbles: true }),
    );
    expect(adapter.isComposing).toBe(true);
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);

    view.dispatch({
      changes: { from: 16, to: 16, insert: "!" },
      selection: EditorSelection.single(17),
      userEvent: "input.type.compose",
    });
    view.contentDOM.dispatchEvent(
      new CompositionEvent("compositionend", { data: "!", bubbles: true }),
    );
    expect(replica.currentText).toBe('{"a":"hello猫!","b":2}');
    expect(view.state.doc.toString()).toBe('{\n  "a": "hello猫!",\n  "b": 2\n}');
    expect(view.contentDOM.querySelectorAll(".cm-line")[1]).toBe(composingLineBefore);
  });

  it("Case 8: malformed intermediate typing stays in raw-backed mode, recovers to Pretty when valid, and coalesces typing into one undo step", async () => {
    const raw = '{"a":1}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case8.json");

    const valDisp = view.state.doc.toString().indexOf("1");
    expect(valDisp).toBe(9);
    const t0 = 1_000_000;

    view.dispatch({
      changes: { from: valDisp, to: valDisp + 1, insert: "" },
      annotations: Transaction.time.of(t0),
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe('{"a":}');
    expect(view.state.doc.toString()).toBe('{"a":}');

    view.dispatch({
      changes: { from: 5, to: 5, insert: "[" },
      annotations: Transaction.time.of(t0 + 50),
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":[}');
    expect(view.state.doc.toString()).toBe('{"a":[}');

    view.dispatch({
      changes: { from: 6, to: 6, insert: "1" },
      annotations: Transaction.time.of(t0 + 100),
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":[1}');
    expect(view.state.doc.toString()).toBe('{"a":[1}');

    view.dispatch({
      changes: { from: 7, to: 7, insert: "]" },
      annotations: Transaction.time.of(t0 + 150),
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":[1]}');
    expect(view.state.doc.toString()).toBe('{\n  "a": [\n    1\n  ]\n}');

    adapter.stepHistory("undo");
    expect(replica.currentText).toBe('{"a":}');
    adapter.stepHistory("undo");
    expect(replica.currentText).toBe('{"a":1}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 1\n}');
  });

  it("Case 9a, 9b, 9c & 9d: rebases strictly disjoint remote edits and retains all non-disjoint PendingStaleEdits across detach and process restart without overwriting", async () => {
    const raw = '{"a": 1,   "b": 2}';
    const { replica, adapter, view } = await mountPrettyEditor(
      raw,
      "case9.json",
      "incarnation-case9",
    );

    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');

    // 9a: Hold view projection at base revision while remote peer edits `"b": 2` -> `"b": 20`.
    adapter.holdViewProjection(true);
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf("2");
      ytext.delete(idx, 1);
      ytext.insert(idx, "20");
    });
    expect(replica.currentText).toBe('{"a": 1,   "b": 20}');

    const oneDisp = view.state.doc.toString().indexOf("1,");
    view.dispatch({
      changes: { from: oneDisp, to: oneDisp + 1, insert: "10" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a": 10,   "b": 20}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 10,\n  "b": 20\n}');
    expect(adapter.pendingStaleEdits).toHaveLength(0);

    // 9b: Overlap with surviving endpoints!
    const baseRev9b = replica.history.revision;
    adapter.holdViewProjection(true);
    applyRemotePeerEdit(replica, (ytext) => {
      const wsIdx = ytext.toString().indexOf(",   ") + 1;
      ytext.delete(wsIdx, 3);
      ytext.insert(wsIdx, " ");
    });
    expect(replica.currentText).toBe('{"a": 10, "b": 20}');
    const remoteRev9b = replica.history.revision;

    const selFrom = view.state.doc.toString().indexOf("10,");
    const selTo = view.state.doc.toString().indexOf('"b"') + '"b"'.length;
    view.dispatch({
      changes: { from: selFrom, to: selTo, insert: '99, "c"' },
      userEvent: "input.paste",
    });

    expect(replica.currentText).toBe('{"a": 10, "b": 20}');
    expect(replica.history.revision).toBe(remoteRev9b);
    expect(view.state.doc.toString()).toBe('{"a": 10, "b": 20}');
    expect(adapter.pendingStaleEdits).toHaveLength(1);
    expect(adapter.pendingStaleEdits[0]).toMatchObject({
      baseHistoryRevision: baseRev9b,
      currentHistoryRevision: remoteRev9b,
      reason: "overlap-conflict",
      intentions: [
        {
          order: 0,
          rawFrom: 6,
          rawTo: 15,
          insert: '99, "c"',
        },
      ],
    });

    // Clear stale edit #1 explicitly to re-enter Pretty mode for 9c, after verifying a second stale edit appends when not cleared!
    // First trigger 9c without clearing 9b to prove non-overwriting append:
    adapter.holdViewProjection(true);
    applyRemotePeerEdit(replica, (ytext) => {
      ytext.insert(1, '"r":0,');
    });
    const revAfter9cRemote = replica.history.revision;
    view.dispatch({
      changes: { from: 1, to: 1, insert: '"l":0,' },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"r":0,"a": 10, "b": 20}');
    expect(replica.history.revision).toBe(revAfter9cRemote);
    // Both 9b and 9c stale edits are preserved!
    expect(adapter.pendingStaleEdits).toHaveLength(2);
    expect(adapter.pendingStaleEdits[0]!.intentions[0]!.insert).toBe('99, "c"');
    expect(adapter.pendingStaleEdits[1]!.intentions[0]!.insert).toBe('"l":0,');

    // 9d: Expired transform chain / ring-buffer eviction appends a 3rd stale edit record!
    adapter.holdViewProjection(true);
    const baseRev9d = replica.history.revision;
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf("20");
      ytext.delete(idx, 2);
      ytext.insert(idx, "30");
    });
    adapter.ringBuffer.evictBefore(replica.history.revision);
    view.dispatch({
      changes: { from: 0, to: 1, insert: "{" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"r":0,"a": 10, "b": 30}');
    expect(adapter.pendingStaleEdits).toHaveLength(3);
    expect(adapter.pendingStaleEdits[2]).toMatchObject({
      baseHistoryRevision: baseRev9d,
      currentHistoryRevision: replica.history.revision,
      reason: "chain-unavailable",
    });

    // Simulate process restart: all 3 pendingStaleEdits survive in durable storage for (documentId, incarnation-case9)!
    adapter.destroy();
    simulatePrettyProcessRestartForTests();
    const restored = new PrettyReplicaEditorAdapter(replica, { path: "case9.json" });
    adapters.push(restored);
    expect(restored.pendingStaleEdits).toHaveLength(3);
    expect(restored.discardPendingStaleEdit(restored.pendingStaleEdits[0]!.staleEditId)).toBe(true);
    expect(restored.pendingStaleEdits).toHaveLength(2);
  });

  it("publishes raw presence offsets and surfaces 4 MiB admission refusal without mutating view or replica", async () => {
    vi.useFakeTimers();
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view, publishPresence } = await mountPrettyEditor(
      raw,
      "presence.json",
    );

    vi.spyOn(view, "hasFocus", "get").mockReturnValue(true);
    const bDisp = view.state.doc.toString().indexOf('"b"');
    expect(bDisp).toBe(14);
    view.dispatch({
      selection: EditorSelection.cursor(bDisp),
    });
    await vi.advanceTimersByTimeAsync(100);
    expect(publishPresence).toHaveBeenCalled();
    expect(replica.history.state.selection.main.head).toBe(raw.indexOf('"b"'));

    const huge = "x".repeat(4 * 1024 * 1024 + 1024);
    const strInsertDisp = view.state.doc.toString().indexOf("1");
    view.dispatch({
      changes: { from: strInsertDisp, to: strInsertDisp + 1, insert: `"${huge}"` },
      userEvent: "input.paste",
    });
    expect(adapter.lastAdmissionError).toBeInstanceOf(Error);
    expect((adapter.lastAdmissionError as Error).message).toContain("4 MiB");
    expect(replica.currentText).toBe(raw);
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');
  });
});
