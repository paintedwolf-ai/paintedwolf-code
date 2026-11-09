// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import * as Y from "yjs";
import {
  DocumentStorageError,
  encodeUpdate,
} from "../../../files/documents/document-outbox.ts";
import { DocumentReplica } from "../../../files/documents/document-replica.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { DocumentFixture } from "../../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../../test/memory-document-outbox.ts";
import { PrettyReplicaEditorAdapter } from "./pretty-replica-adapter.ts";
import {
  configurePrettyRecoveryLimitsForTests,
  configurePrettyRecoveryStorageForTests,
  getHistoryRuntimeId,
  prettyRecoveryStorageKey,
  PrettyRecoveryCapacityError,
  type PrettyRecoveryStorage,
  resetPrettyDocumentRecoveryStoreForTests,
  simulatePrettyProcessRestartForTests,
} from "./pretty-replica-recovery.ts";

const identity = vi.hoisted(() => ({ value: "window-pretty-recovery-proof" }));
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
  Y.applyUpdate(peer, Y.encodeStateAsUpdate(replica.doc));
  const before = Y.encodeStateVector(peer);
  peer.transact(() => {
    mutate(peer.getText("text"));
  });
  const diff = Y.encodeStateAsUpdate(peer, before);
  replica.receive({
    type: "editor_document_updated",
    document: replica.accepted,
    crdt_update: encodeUpdate(diff),
    origin_client_id: "remote-peer-client",
  });
}

describe("PrettyReplicaEditorAdapter durable recovery & storage transactions", () => {
  it("Case 6c: rejects stale conflict resolution on remote overlap, proves real recreated DocumentReplica/outbox recovery across process restart, and refuses guessed offsets when source changed across restart", async () => {
    const raw = '{"a":1,"b":2}';
    const { replica, adapter, view } = await mountPrettyEditor(
      raw,
      "conflict-durable.json",
      "incarnation-alpha",
    );

    view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "A" },
        { from: 3, to: 3, insert: "B" },
      ],
      userEvent: "input.type",
    });
    expect(adapter.pendingConflicts).toHaveLength(1);

    adapter.setPrettyEnabled(false);
    adapter.setPrettyEnabled(true);
    expect(adapter.pendingConflicts).toHaveLength(1);

    applyRemotePeerEdit(replica, (ytext) => {
      ytext.delete(1, 3);
      ytext.insert(1, '"aa"');
    });
    expect(replica.currentText).toBe('{"aa":1,"b":2}');

    const overlapRes = adapter.resolvePendingConflict({ action: "apply", order: 0 });
    expect(overlapRes).toEqual({
      ok: false,
      status: "overlap-conflict",
    });
    expect(replica.currentText).toBe('{"aa":1,"b":2}');
    expect(adapter.pendingConflicts).toHaveLength(1);
    expect(adapter.pendingConflict?.intentions).toHaveLength(2);

    const firstConflictId = adapter.pendingConflicts[0]!.conflictId;
    adapter.resolvePendingConflict({
      action: "discard-conflict",
      conflictId: firstConflictId,
    });
    expect(adapter.pendingConflicts).toHaveLength(0);

    const jsonlMount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}',
      "multi-conflict.jsonl",
      "incarnation-persist-1",
    );
    jsonlMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "C1a" },
        { from: 3, to: 3, insert: "C1b" },
      ],
      userEvent: "input.type",
    });
    expect(jsonlMount.adapter.pendingConflicts).toHaveLength(1);

    const rec2Start = jsonlMount.view.state.doc.toString().indexOf('{\n  "b"');
    expect(rec2Start).toBeGreaterThan(0);
    jsonlMount.view.dispatch({
      changes: [
        { from: rec2Start + 2, to: rec2Start + 2, insert: "C2a" },
        { from: rec2Start + 3, to: rec2Start + 3, insert: "C2b" },
      ],
      userEvent: "input.type",
    });
    expect(jsonlMount.adapter.pendingConflicts).toHaveLength(2);
    expect(jsonlMount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("C1a");
    expect(jsonlMount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("C2a");

    const storageKey = prettyRecoveryStorageKey(
      jsonlMount.replica.accepted.id,
      "incarnation-persist-1",
    );
    expect(localStorage.getItem(storageKey)).toBeTruthy();

    jsonlMount.adapter.destroy();
    await jsonlMount.replica.flush();
    await jsonlMount.replica.close();
    const origIdx = replicas.indexOf(jsonlMount.replica);
    if (origIdx >= 0) replicas.splice(origIdx, 1);

    simulatePrettyProcessRestartForTests();

    const otherIncReplica = new DocumentReplica(
      jsonlMount.fixture.snapshot(),
      () => jsonlMount.client,
      () => undefined,
      memoryDocumentOutbox(),
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

    const snapForRestart = jsonlMount.fixture.snapshot();
    snapForRestart.path = "multi-conflict.jsonl";
    const recreatedReplica = new DocumentReplica(
      snapForRestart,
      () => jsonlMount.client,
      () => undefined,
      jsonlMount.outbox,
    );
    replicas.push(recreatedReplica);
    await recreatedReplica.initialize();
    expect(recreatedReplica.incarnation).toBe("incarnation-persist-1");
    expect(getHistoryRuntimeId(recreatedReplica.history)).not.toBe(
      getHistoryRuntimeId(jsonlMount.replica.history),
    );
    expect(recreatedReplica.currentText).toBe('{"a":1}\n{"b":2}');

    const restoredAdapter = new PrettyReplicaEditorAdapter(recreatedReplica, {
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

    const divergedFixture = new DocumentFixture();
    const divergedOutbox = memoryDocumentOutbox();
    const divergedClient = stubClient({
      syncEditorDocument: divergedFixture.sync,
      submitEditorDocumentUpdate: divergedFixture.submit,
      publishEditorDocumentPresence: async () => undefined,
    });
    const divergedSnap = divergedFixture.snapshot();
    divergedSnap.id = jsonlMount.replica.accepted.id;
    divergedSnap.path = "multi-conflict.jsonl";
    const divergedReplica = new DocumentReplica(
      divergedSnap,
      () => divergedClient,
      () => undefined,
      divergedOutbox,
      undefined,
      "incarnation-persist-1",
    );
    replicas.push(divergedReplica);
    await divergedReplica.initialize();
    divergedReplica.replaceLocal('{"z":999,"a":1}\n{"b":2}');
    await divergedReplica.flush();
    expect(divergedReplica.history.revision).toBe(
      restoredAdapter.pendingConflicts[0]!.baseHistoryRevision,
    );
    const divergedAdapter = new PrettyReplicaEditorAdapter(divergedReplica, {
      path: "multi-conflict.jsonl",
    });
    adapters.push(divergedAdapter);
    const refusedCrossProcess = divergedAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: divergedAdapter.pendingConflicts[0]!.conflictId,
      order: 0,
    });
    expect(refusedCrossProcess).toEqual({
      ok: false,
      status: "chain-unavailable",
    });
    expect(divergedReplica.currentText).toBe('{"z":999,"a":1}\n{"b":2}');
    expect(divergedAdapter.pendingConflicts).toHaveLength(2);

    const resC1 = restoredAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: restoredAdapter.pendingConflicts[0]!.conflictId,
      order: 0,
      discardRemainingIntentions: true,
    });
    expect(resC1.ok).toBe(true);
    expect(restoredAdapter.pendingConflicts).toHaveLength(1);
    expect(recreatedReplica.currentText).toBe('{C1a"a":1}\n{"b":2}');

    const resC2Refused = restoredAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: restoredAdapter.pendingConflicts[0]!.conflictId,
      order: 1,
    });
    expect(resC2Refused).toEqual({
      ok: false,
      status: "chain-unavailable",
    });
    expect(recreatedReplica.currentText).toBe('{C1a"a":1}\n{"b":2}');
    expect(restoredAdapter.pendingConflicts).toHaveLength(1);

    const resC2Discard = restoredAdapter.resolvePendingConflict({
      action: "discard-conflict",
      conflictId: restoredAdapter.pendingConflicts[0]!.conflictId,
    });
    expect(resC2Discard.ok).toBe(true);
    expect(restoredAdapter.pendingConflicts).toHaveLength(0);
    expect(localStorage.getItem(storageKey)).toBeNull();
  });

  it("Case 6d: handles unavailable, read-denied, unknown-format, and quota/write-denied storage transactionally without overwriting unread data or replaying duplicate intentions after restart", async () => {
    configurePrettyRecoveryStorageForTests(null);
    const unavailMount = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "unavail.json",
      "inc-unavail",
    );
    unavailMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "U1" },
        { from: 3, to: 3, insert: "U2" },
      ],
      userEvent: "input.type",
    });
    expect(unavailMount.adapter.pendingConflicts).toHaveLength(1);
    expect(unavailMount.adapter.preservationBlocked).toBe(true);
    expect(unavailMount.replica.preservationBlocked).toBe(false);
    expect(unavailMount.adapter.recoveryStorageError).toBeInstanceOf(DocumentStorageError);

    const setItemSpy = vi.fn();
    const removeItemSpy = vi.fn();
    const readDeniedStorage: PrettyRecoveryStorage = {
      getItem: () => {
        throw new Error("SecurityError: storage read denied");
      },
      setItem: setItemSpy,
      removeItem: removeItemSpy,
    };
    configurePrettyRecoveryStorageForTests(readDeniedStorage);
    const readDeniedMount = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "read-denied.json",
      "inc-read-denied",
    );
    expect(readDeniedMount.adapter.preservationBlocked).toBe(true);
    expect(readDeniedMount.adapter.recoveryStorageError).toBeInstanceOf(DocumentStorageError);
    readDeniedMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "R1" },
        { from: 3, to: 3, insert: "R2" },
      ],
      userEvent: "input.type",
    });
    expect(readDeniedMount.adapter.pendingConflicts).toHaveLength(1);
    expect(setItemSpy).not.toHaveBeenCalled();
    expect(removeItemSpy).not.toHaveBeenCalled();

    configurePrettyRecoveryStorageForTests(undefined);
    const unknownMountInit = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "unknown-format.json",
      "inc-unknown-fmt",
    );
    const unknownKey = prettyRecoveryStorageKey(
      unknownMountInit.replica.accepted.id,
      "inc-unknown-fmt",
    );
    const unknownPayload = JSON.stringify({ version: 99, futureField: "keep-me-untouched" });
    localStorage.setItem(unknownKey, unknownPayload);
    unknownMountInit.adapter.destroy();
    simulatePrettyProcessRestartForTests();

    const unknownAdapter = new PrettyReplicaEditorAdapter(unknownMountInit.replica, {
      path: "unknown-format.json",
    });
    adapters.push(unknownAdapter);
    expect(unknownAdapter.preservationBlocked).toBe(true);
    expect(unknownAdapter.recoveryStorageError).toBeInstanceOf(DocumentStorageError);
    expect(unknownAdapter.recoveryStorageError?.retryable).toBe(false);
    expect(unknownAdapter.unreadableRecoveryPayload).toBe(unknownPayload);
    unknownAdapter.attach(unknownMountInit.view);
    unknownMountInit.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "F1" },
        { from: 3, to: 3, insert: "F2" },
      ],
      userEvent: "input.type",
    });
    expect(unknownAdapter.pendingConflicts).toHaveLength(1);
    expect(localStorage.getItem(unknownKey)).toBe(unknownPayload);

    const quotaMount = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "quota.json",
      "inc-quota",
    );
    quotaMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "Q1" },
        { from: 3, to: 3, insert: "Q2" },
      ],
      userEvent: "input.type",
    });
    expect(quotaMount.adapter.pendingConflicts).toHaveLength(1);

    const FailingQuotaStorage: PrettyRecoveryStorage = {
      getItem: (k) => localStorage.getItem(k),
      setItem: () => {
        throw new Error("QuotaExceededError: storage quota exceeded");
      },
      removeItem: () => {
        throw new Error("QuotaExceededError: remove blocked");
      },
    };
    configurePrettyRecoveryStorageForTests(FailingQuotaStorage);

    quotaMount.replica.preservationBlocked = false;
    const quotaApplyRes = quotaMount.adapter.resolvePendingConflict({
      action: "apply",
      order: 0,
    });
    expect(quotaApplyRes).toEqual({
      ok: true,
      status: "applied",
      conflictId: expect.any(String),
      remainingIntentions: 1,
      remainingConflicts: 1,
      preservationBlocked: true,
      storageError: expect.any(DocumentStorageError),
    });
    expect(quotaMount.replica.currentText).toBe('{Q1"a":1,"b":2}');
    expect(quotaMount.adapter.pendingConflict?.intentions).toHaveLength(1);
    expect(quotaMount.adapter.pendingConflict?.intentions[0]!.order).toBe(1);

    configurePrettyRecoveryStorageForTests(undefined);
    quotaMount.adapter.destroy();
    await quotaMount.replica.flush();
    await quotaMount.replica.close();
    const qIdx = replicas.indexOf(quotaMount.replica);
    if (qIdx >= 0) replicas.splice(qIdx, 1);
    simulatePrettyProcessRestartForTests();

    const quotaSnap = quotaMount.fixture.snapshot();
    quotaSnap.path = "quota.json";
    const recreatedQuotaReplica = new DocumentReplica(
      quotaSnap,
      () => quotaMount.client,
      () => undefined,
      quotaMount.outbox,
    );
    replicas.push(recreatedQuotaReplica);
    await recreatedQuotaReplica.initialize();
    expect(recreatedQuotaReplica.incarnation).toBe("inc-quota");
    expect(recreatedQuotaReplica.currentText).toBe('{Q1"a":1,"b":2}');

    const recreatedQuotaAdapter = new PrettyReplicaEditorAdapter(recreatedQuotaReplica, {
      path: "quota.json",
    });
    adapters.push(recreatedQuotaAdapter);
    const duplicateReplayAttempt = recreatedQuotaAdapter.resolvePendingConflict({
      action: "apply",
      order: 0,
    });
    expect(duplicateReplayAttempt).toEqual({
      ok: false,
      status: "chain-unavailable",
    });
    expect(recreatedQuotaReplica.currentText).toBe('{Q1"a":1,"b":2}');
  });

  it("Case 6e: bounds recovery queue count and retained bytes without dropping unresolved input, holds overflow, promotes on resolution, and refuses conflicting edits when overflow is occupied", async () => {
    configurePrettyRecoveryLimitsForTests({ maxRecords: 2, maxRetainedBytes: 256 * 1024 });
    const mount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}\n{"c":3}\n{"d":4}',
      "capacity.jsonl",
      "inc-capacity",
    );

    const triggerConflictInRecord = (recKey: string, a: string, b: string) => {
      const start = mount.view.state.doc.toString().indexOf(`{\n  "${recKey}"`);
      expect(start).toBeGreaterThanOrEqual(0);
      mount.view.dispatch({
        changes: [
          { from: start + 2, to: start + 2, insert: a },
          { from: start + 3, to: start + 3, insert: b },
        ],
        userEvent: "input.type",
      });
    };

    triggerConflictInRecord("a", "K1a", "K1b");
    triggerConflictInRecord("b", "K2a", "K2b");
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.capacityOverflowRecord).toBeNull();
    expect(mount.adapter.retainedRecoveryBytes).toBeGreaterThan(0);

    triggerConflictInRecord("c", "K3a", "K3b");
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.capacityOverflowRecord?.kind).toBe("conflict");
    expect(
      (mount.adapter.capacityOverflowRecord?.record as { intentions: Array<{ insert: string }> })
        .intentions[0]!.insert,
    ).toBe("K3a");
    expect(mount.adapter.preservationBlocked).toBe(true);

    triggerConflictInRecord("d", "K4a", "K4b");
    expect(mount.adapter.lastAdmissionError).toBeInstanceOf(PrettyRecoveryCapacityError);
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(
      (mount.adapter.capacityOverflowRecord?.record as { intentions: Array<{ insert: string }> })
        .intentions[0]!.insert,
    ).toBe("K3a");

    const rawKeyA = mount.view.state.doc.toString().indexOf('"a":1');
    expect(rawKeyA).toBeGreaterThanOrEqual(0);
    mount.view.dispatch({
      changes: { from: rawKeyA + 4, to: rawKeyA + 5, insert: "10" },
      userEvent: "input.type",
    });
    expect(mount.replica.currentText).toContain('{"a":10}');

    mount.adapter.resolvePendingConflict({
      action: "discard-conflict",
      conflictId: mount.adapter.pendingConflicts[0]!.conflictId,
    });
    expect(mount.adapter.capacityOverflowRecord).toBeNull();
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("K3a");

    const singleRecordBytes = mount.adapter.pendingConflicts[0]!.byteEstimate ?? 400;
    configurePrettyRecoveryLimitsForTests({
      maxRecords: 64,
      maxRetainedBytes: singleRecordBytes + 50,
    });
    const byteMount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}',
      "byte-cap.jsonl",
      "inc-byte-cap",
    );
    byteMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "B1a" },
        { from: 3, to: 3, insert: "B1b" },
      ],
      userEvent: "input.type",
    });
    expect(byteMount.adapter.pendingConflicts).toHaveLength(1);
    expect(byteMount.adapter.capacityOverflowRecord).toBeNull();

    const rec2Idx = byteMount.view.state.doc.toString().indexOf('{\n  "b"');
    byteMount.view.dispatch({
      changes: [
        { from: rec2Idx + 2, to: rec2Idx + 2, insert: "B2a" },
        { from: rec2Idx + 3, to: rec2Idx + 3, insert: "B2b" },
      ],
      userEvent: "input.type",
    });
    expect(byteMount.adapter.pendingConflicts).toHaveLength(1);
    expect(byteMount.adapter.capacityOverflowRecord?.kind).toBe("conflict");
  });
});
