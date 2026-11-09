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
import {
  PrettyReplicaEditorAdapter,
  resetPrettyDocumentRecoveryStoreForTests,
} from "./index.ts";

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
  vi.useRealTimers();
});

async function mountPrettyEditor(initialRawText: string, path = "config.json") {
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
  const replica = new DocumentReplica(snap, () => client, () => undefined, outbox);
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

  return { fixture, replica, adapter, view, client, publishPresence };
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

  it("Case 3a & 3b: collapsed Backspace/Delete in generated layout moves to raw boundary with affinity that survives remote refresh, then deletes adjacent raw token", async () => {
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case3.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');
    const revBefore = replica.history.revision;

    // Step 1 Backspace at disp = 4 (before `"a"` inside `\n  ` [1, 4)): moves caret to disp = 1 (src = 1, assoc = "before").
    view.dispatch({
      changes: { from: 3, to: 4, insert: "" },
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe(raw);
    expect(replica.history.revision).toBe(revBefore);
    expect(view.state.selection.main.head).toBe(1);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "before" });

    // Remote refresh modifies `"b":2` -> `"b":20` while caret sits at rawOffset = 1 with assoc = "before".
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf('"b":2') + '"b":'.length;
      ytext.delete(idx, 1);
      ytext.insert(idx, "20");
    });
    expect(replica.currentText).toBe('{"a":1,"b":20}');
    // Caret affinity ("before") survived remote regeneration: display selection is still at disp = 1 (after `{`), not disp = 4!
    expect(view.state.selection.main.head).toBe(1);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "before" });

    // Step 2 Backspace at disp = 1: deletes `{` at raw [0, 1) and transitions to raw-backed presentation.
    view.dispatch({
      changes: { from: 0, to: 1, insert: "" },
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe('"a":1,"b":20}');
    expect(view.state.doc.toString()).toBe('"a":1,"b":20}');

    // Undo restores `{"a":1,"b":20}` in Pretty mode; now test forward Delete (Case 3b).
    adapter.stepHistory("undo");
    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 20\n}');

    // Step 1 Delete at disp = 1 (start of `\n  ` [1, 4)): moves caret to disp = 4 (src = 1, assoc = "after").
    view.dispatch({
      changes: { from: 1, to: 2, insert: "" },
      userEvent: "delete.forward",
    });
    expect(replica.currentText).toBe('{"a":1,"b":20}');
    expect(view.state.selection.main.head).toBe(4);
    expect(adapter.caretAffinity).toEqual({ rawOffset: 1, assoc: "after" });

    // Step 2 Delete at disp = 4 (immediately before `"a"`): deletes `"` at raw [1, 2).
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

    // Paste `"   "` at disp = 4 (before `"a"`, insertOffset = 1) -> raw becomes `{"   "a":"hello"}` and stays in raw-backed presentation.
    const keyDisp = view.state.doc.toString().indexOf('"a"');
    expect(keyDisp).toBe(4);
    view.dispatch({
      changes: { from: keyDisp, to: keyDisp, insert: "   " },
      userEvent: "input.paste",
    });
    expect(replica.currentText).toBe('{   "a":"hello"}');
    expect(view.state.doc.toString()).toBe('{   "a":"hello"}');

    // Undo back to formatted `{"a":"hello"}`, then type `" world"` inside `"hello"`.
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

    // First formatted record `{\n  "a": 1\n}` is 12 UTF-16 units; record delimiter `\n` is at disp [12, 13).
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

  it("Case 6a & 6b: coalesces identical multi-cursor virtual insertions and retains conflicting multi-cursor intentions without mutating raw source", async () => {
    const raw = '{"a":1}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case6.json");

    // Case 6a: Identical insertions at disp = 2 and disp = 3 coalesce into a single insertion at raw offset 1.
    view.dispatch({
      changes: [
        { from: 2, to: 2, insert: '"x":0,' },
        { from: 3, to: 3, insert: '"x":0,' },
      ],
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"x":0,"a":1}');
    expect(view.state.doc.toString()).toBe('{\n  "x": 0,\n  "a": 1\n}');

    adapter.stepHistory("undo");
    expect(replica.currentText).toBe('{"a":1}');
    const revBeforeConflict = replica.history.revision;

    // Case 6b: Conflicting insertions at disp = 2 ("x") and disp = 3 ("y") perform ZERO raw mutations,
    // transition to raw-backed presentation at raw offset 1, and retain both ordered intentions.
    view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "x" },
        { from: 3, to: 3, insert: "y" },
      ],
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a":1}');
    expect(replica.history.revision).toBe(revBeforeConflict);
    expect(view.state.doc.toString()).toBe('{"a":1}');
    expect(view.state.selection.main.head).toBe(1);
    expect(adapter.pendingConflict).toEqual({
      sourceRevision: revBeforeConflict,
      reason: "same-raw-offset-distinct-insert",
      intentions: [
        { order: 0, dispFrom: 2, dispTo: 2, rawFrom: 1, rawTo: 1, insert: "x" },
        { order: 1, dispFrom: 3, dispTo: 3, rawFrom: 1, rawTo: 1, insert: "y" },
      ],
    });

    // Pending conflict survives view detach and reattach.
    adapter.detach();
    adapter.attach(view);
    expect(adapter.pendingConflict?.intentions).toHaveLength(2);

    // Explicit resolution applies chosen intention (order 1 -> "y") at raw offset 1.
    expect(adapter.resolvePendingConflict(1)).toBe(true);
    expect(adapter.pendingConflict).toBeNull();
    expect(replica.currentText).toBe('{y"a":1}');
  });

  it("Case 7: IME composition establishes raw-backed presentation before composition DOM ownership and avoids replacing DOM on intermediate compose frames", async () => {
    const raw = '{"a":"ni"}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case7.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": "ni"\n}');

    // Pre-IME keydown (keyCode 229 / Process) establishes raw-backed presentation before browser composition owns DOM.
    view.contentDOM.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Process", keyCode: 229, bubbles: true }),
    );
    expect(view.state.doc.toString()).toBe('{"a":"ni"}');

    // Intermediate composition frame updates in-place without full projection replacement.
    const niEnd = view.state.doc.toString().indexOf('ni"') + 2;
    view.dispatch({
      changes: { from: niEnd, to: niEnd, insert: "hao" },
      userEvent: "input.type.compose",
    });
    expect(replica.currentText).toBe('{"a":"nihao"}');
    expect(view.state.doc.toString()).toBe('{"a":"nihao"}');

    // Finishing composition re-evaluates projection and formats valid JSON cleanly.
    view.contentDOM.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    expect(view.state.doc.toString()).toBe('{\n  "a": "nihao"\n}');
    void adapter;
  });

  it("Case 8: malformed intermediate typing stays in raw-backed mode, recovers to Pretty when valid, and coalesces typing into one undo step", async () => {
    const raw = '{"a":1}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case8.json");

    const valDisp = view.state.doc.toString().indexOf("1");
    expect(valDisp).toBe(9);
    const t0 = 1_000_000;

    // Delete `1` -> `{"a":}` (malformed -> raw-backed presentation).
    view.dispatch({
      changes: { from: valDisp, to: valDisp + 1, insert: "" },
      annotations: Transaction.time.of(t0),
      userEvent: "delete.backward",
    });
    expect(replica.currentText).toBe('{"a":}');
    expect(view.state.doc.toString()).toBe('{"a":}');

    // Type `[`, `1`, `]` at raw offset 5 with consecutive timestamps so DocumentHistory coalesces typing.
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
    // Now valid JSON: automatically recovers to formatted Pretty projection!
    expect(replica.currentText).toBe('{"a":[1]}');
    expect(view.state.doc.toString()).toBe('{\n  "a": [\n    1\n  ]\n}');

    // Single undo step undoes the entire coalesced typing group `[1]` back to `{"a":}`!
    adapter.stepHistory("undo");
    expect(replica.currentText).toBe('{"a":}');
    // Second undo restores `{"a":1}`!
    adapter.stepHistory("undo");
    expect(replica.currentText).toBe('{"a":1}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 1\n}');
  });

  it("Case 9a, 9b, 9c & 9d: rebases strictly disjoint remote edits and rejects interior-overlap, same-offset insertion, and expired-chain edits while retaining PendingStaleEdit across detach", async () => {
    const raw = '{"a": 1,   "b": 2}';
    const { replica, adapter, view } = await mountPrettyEditor(raw, "case9.json");

    expect(view.state.doc.toString()).toBe('{\n  "a": 1,\n  "b": 2\n}');

    // 9a: Hold view projection at base revision while remote peer edits `"b": 2` -> `"b": 20`.
    adapter.holdViewProjection(true);
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf("2");
      ytext.delete(idx, 1);
      ytext.insert(idx, "20");
    });
    expect(replica.currentText).toBe('{"a": 1,   "b": 20}');

    // Local user edits `1` (disp [9, 10)) -> `10` on the held projection: disjoint from remote edit!
    const oneDisp = view.state.doc.toString().indexOf("1,");
    view.dispatch({
      changes: { from: oneDisp, to: oneDisp + 1, insert: "10" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"a": 10,   "b": 20}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 10,\n  "b": 20\n}');
    expect(adapter.pendingStaleEdit).toBeNull();

    // 9b: Overlap with surviving endpoints!
    // Remote peer modifies interior whitespace `   ` (raw [9, 12) in `{"a": 10,   "b": 20}`) to single space ` `,
    // while local user replaces enclosing range `10,\n  "b"` (raw [6, 15)) whose endpoints 6 and 15 both survive TrackDel!
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

    // Zero raw mutation: remote text is preserved intact, view transitions to raw-backed fallback, and PendingStaleEdit is retained!
    expect(replica.currentText).toBe('{"a": 10, "b": 20}');
    expect(replica.history.revision).toBe(remoteRev9b);
    expect(view.state.doc.toString()).toBe('{"a": 10, "b": 20}');
    expect(adapter.pendingStaleEdit).toMatchObject({
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

    // Survives EditorView detach and reattach!
    adapter.detach();
    adapter.attach(view);
    expect(adapter.pendingStaleEdit?.reason).toBe("overlap-conflict");

    // 9c: Same-position zero-width insertion conflict!
    adapter.setPrettyEnabled(false);
    adapter.setPrettyEnabled(true);
    adapter.holdViewProjection(true);
    applyRemotePeerEdit(replica, (ytext) => {
      ytext.insert(1, '"r":0,');
    });
    const revAfter9cRemote = replica.history.revision;
    view.dispatch({
      changes: { from: 2, to: 2, insert: '"l":0,' },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"r":0,"a": 10, "b": 20}');
    expect(replica.history.revision).toBe(revAfter9cRemote);
    expect(adapter.pendingStaleEdit?.reason).toBe("overlap-conflict");

    // 9d: Expired transform chain / ring-buffer eviction!
    adapter.holdViewProjection(true);
    const baseRev9d = replica.history.revision;
    applyRemotePeerEdit(replica, (ytext) => {
      const idx = ytext.toString().indexOf("20");
      ytext.delete(idx, 2);
      ytext.insert(idx, "30");
    });
    // Evict intermediate revision from ring buffer so complete transform chain is unavailable.
    adapter.ringBuffer.evictBefore(replica.history.revision);
    view.dispatch({
      changes: { from: 0, to: 1, insert: "{" },
      userEvent: "input.type",
    });
    expect(replica.currentText).toBe('{"r":0,"a": 10, "b": 30}');
    expect(adapter.pendingStaleEdit).toMatchObject({
      baseHistoryRevision: baseRev9d,
      currentHistoryRevision: replica.history.revision,
      reason: "chain-unavailable",
    });
  });

  it("publishes raw presence offsets and surfaces 4 MiB admission refusal without mutating view or replica", async () => {
    vi.useFakeTimers();
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view, publishPresence } = await mountPrettyEditor(
      raw,
      "presence.json",
    );

    // Focus view and move selection to `"b"` in display coordinates (disp = 14 -> raw = 7).
    vi.spyOn(view, "hasFocus", "get").mockReturnValue(true);
    const bDisp = view.state.doc.toString().indexOf('"b"');
    expect(bDisp).toBe(14);
    view.dispatch({
      selection: EditorSelection.cursor(bDisp),
    });
    await vi.advanceTimersByTimeAsync(100);
    expect(publishPresence).toHaveBeenCalled();
    expect(replica.history.state.selection.main.head).toBe(raw.indexOf('"b"'));

    // Attempting an edit exceeding the 4 MiB DocumentByteBudget is refused cleanly.
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
