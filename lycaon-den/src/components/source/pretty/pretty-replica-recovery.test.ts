// @vitest-environment jsdom
import { diagnosticCount } from "@codemirror/lint";
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
  getHistoryRuntimeSequence,
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

    simulatePrettyProcessRestartForTests();

    const jsonlMount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}',
      "multi-conflict.jsonl",
      "incarnation-persist-1",
    );
    const originalRuntimeSeq = getHistoryRuntimeSequence(jsonlMount.replica.history);
    const originalRuntimeId = getHistoryRuntimeId(jsonlMount.replica.history);
    expect(originalRuntimeSeq).toBe(1);
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
    expect(getHistoryRuntimeSequence(recreatedReplica.history)).toBe(originalRuntimeSeq);
    expect(getHistoryRuntimeId(recreatedReplica.history)).not.toBe(originalRuntimeId);
    expect(recreatedReplica.currentText).toBe('{"a":1}\n{"b":2}');

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

    simulatePrettyProcessRestartForTests();
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
    expect(getHistoryRuntimeSequence(divergedReplica.history)).toBe(originalRuntimeSeq);
    expect(getHistoryRuntimeId(divergedReplica.history)).not.toBe(originalRuntimeId);
    const baseRev = restoredAdapter.pendingConflicts[0]!.baseHistoryRevision;
    expect(divergedReplica.history.revision).toBe(baseRev);
    const divergedAdapter = new PrettyReplicaEditorAdapter(divergedReplica, {
      path: "multi-conflict.jsonl",
    });
    adapters.push(divergedAdapter);
    divergedReplica.replaceLocal('{"z":999,"w":1,"a":1}\n{"b":2}');
    expect(divergedReplica.history.revision).toBeGreaterThan(baseRev);
    expect(
      divergedAdapter.ringBuffer.collectChain(
        divergedReplica.incarnation,
        divergedReplica.accepted.epoch,
        baseRev,
        divergedReplica.incarnation,
        divergedReplica.accepted.epoch,
        divergedReplica.history.revision,
      ),
    ).toHaveLength(1);
    const refusedCrossProcess = divergedAdapter.resolvePendingConflict({
      action: "apply",
      conflictId: divergedAdapter.pendingConflicts[0]!.conflictId,
      order: 0,
    });
    expect(refusedCrossProcess).toEqual({
      ok: false,
      status: "chain-unavailable",
    });
    expect(divergedReplica.currentText).toBe('{"z":999,"w":1,"a":1}\n{"b":2}');
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

  it("Case 6e: transitions to unambiguous raw-backed editing once overflow is occupied (retaining K4 and K5 after K3 without overwriting K1..K3 or lastCopiedSelection), rejects forced overlapping edits at capacity, and supports oversized first records", async () => {
    configurePrettyRecoveryLimitsForTests({ maxRecords: 2, maxRetainedBytes: 256 * 1024 });
    const mount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}\n{"c":3}\n{"d":4}\n{"e":5}',
      "capacity.jsonl",
      "inc-capacity",
    );

    const triggerConflictInFormattedRecord = (recKey: string, a: string, b: string) => {
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

    triggerConflictInFormattedRecord("a", "K1a", "K1b");
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("K1a\nK1b");

    triggerConflictInFormattedRecord("b", "K2a", "K2b");
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.capacityOverflowRecord).toBeNull();
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("K1a\nK1b");

    // K3 occupies capacityOverflowRecord and immediately transitions the entire document
    // to unambiguous raw-backed projection before any further edits (K4, K5).
    triggerConflictInFormattedRecord("c", "K3a", "K3b");
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.capacityOverflowRecord?.kind).toBe("conflict");
    expect(
      (mount.adapter.capacityOverflowRecord?.record as { intentions: Array<{ insert: string }> })
        .intentions[0]!.insert,
    ).toBe("K3a");
    expect(mount.adapter.preservationBlocked).toBe(true);
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("K1a\nK1b");
    expect(mount.view.state.doc.toString()).toBe('{"a":1}\n{"b":2}\n{"c":3}\n{"d":4}\n{"e":5}');

    // K4 and then K5 while K3 still occupies overflow:
    // Because the document transitioned to the unambiguous raw-backed path at capacity,
    // K4 (in record "d") and K5 (in record "e") map 1:1 to raw offsets and are retained in replica.currentText
    // without generating ambiguous virtual-layout conflicts or overwriting K1, K2, K3, or lastCopiedSelection.
    const startD = mount.view.state.doc.toString().indexOf('{"d":4}');
    expect(startD).toBeGreaterThanOrEqual(0);
    mount.view.dispatch({
      changes: [
        { from: startD + 2, to: startD + 2, insert: "K4a" },
        { from: startD + 3, to: startD + 3, insert: "K4b" },
      ],
      userEvent: "input.type",
    });

    const startE = mount.view.state.doc.toString().indexOf('{"e":5}');
    expect(startE).toBeGreaterThanOrEqual(0);
    mount.view.dispatch({
      changes: [
        { from: startE + 2, to: startE + 2, insert: "K5a" },
        { from: startE + 3, to: startE + 3, insert: "K5b" },
      ],
      userEvent: "input.type",
    });

    expect(mount.replica.currentText).toContain('{"K4adK4b":4}');
    expect(mount.replica.currentText).toContain('{"K5aeK5b":5}');
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("K1a");
    expect(mount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("K2a");
    expect(
      (mount.adapter.capacityOverflowRecord?.record as { intentions: Array<{ insert: string }> })
        .intentions[0]!.insert,
    ).toBe("K3a");
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("K1a\nK1b");

    // Forced overlapping-range edits (ForcedK4, then ForcedK5) while K3 occupies overflow
    // are explicitly rejected without overwriting K1, K2, K3, K4, K5, or lastCopiedSelection.
    mount.view.dispatch({
      changes: [
        { from: 1, to: 4, insert: "FX4a" },
        { from: 2, to: 5, insert: "FX4b" },
      ],
      userEvent: "input.type",
    });
    expect(mount.adapter.lastAdmissionError).toBeInstanceOf(PrettyRecoveryCapacityError);
    mount.view.dispatch({
      changes: [
        { from: 1, to: 4, insert: "FX5a" },
        { from: 2, to: 5, insert: "FX5b" },
      ],
      userEvent: "input.type",
    });
    expect(mount.adapter.lastAdmissionError).toBeInstanceOf(PrettyRecoveryCapacityError);
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("K1a");
    expect(mount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("K2a");
    expect(
      (mount.adapter.capacityOverflowRecord?.record as { intentions: Array<{ insert: string }> })
        .intentions[0]!.insert,
    ).toBe("K3a");
    expect(mount.replica.currentText).toContain('{"K4adK4b":4}');
    expect(mount.replica.currentText).toContain('{"K5aeK5b":5}');
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("K1a\nK1b");

    // Resolving K1 promotes K3 from capacityOverflowRecord into durable pendingConflicts
    mount.adapter.resolvePendingConflict({
      action: "discard-conflict",
      conflictId: mount.adapter.pendingConflicts[0]!.conflictId,
    });
    expect(mount.adapter.capacityOverflowRecord).toBeNull();
    expect(mount.adapter.pendingConflicts).toHaveLength(2);
    expect(mount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("K2a");
    expect(mount.adapter.pendingConflicts[1]!.intentions[0]!.insert).toBe("K3a");
    expect(mount.adapter.preservationBlocked).toBe(false);

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

    // Verify restart behavior for unapplied memory-only overflow vs durable pendingConflicts
    byteMount.adapter.destroy();
    await byteMount.replica.flush();
    await byteMount.replica.close();
    const byteIdx = replicas.indexOf(byteMount.replica);
    if (byteIdx >= 0) replicas.splice(byteIdx, 1);
    simulatePrettyProcessRestartForTests();

    const byteSnap = byteMount.fixture.snapshot();
    byteSnap.path = "byte-cap.jsonl";
    const recreatedByteReplica = new DocumentReplica(
      byteSnap,
      () => byteMount.client,
      () => undefined,
      byteMount.outbox,
    );
    replicas.push(recreatedByteReplica);
    await recreatedByteReplica.initialize();
    const recreatedByteAdapter = new PrettyReplicaEditorAdapter(recreatedByteReplica, {
      path: "byte-cap.jsonl",
    });
    adapters.push(recreatedByteAdapter);
    expect(recreatedByteAdapter.pendingConflicts).toHaveLength(1);
    expect(recreatedByteAdapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("B1a");
    expect(recreatedByteAdapter.capacityOverflowRecord).toBeNull();

    // Individual oversized first record (byteEstimate > maxRetainedBytes when queue is empty):
    configurePrettyRecoveryLimitsForTests({
      maxRecords: 64,
      maxRetainedBytes: singleRecordBytes + 80,
    });
    const oversizedMount = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "oversized-first.json",
      "inc-oversized-1",
    );
    const oversizedStorageKey = prettyRecoveryStorageKey(
      oversizedMount.replica.accepted.id,
      "inc-oversized-1",
    );
    const largeChunk = "L".repeat(450);
    oversizedMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: largeChunk },
        { from: 3, to: 3, insert: "S1" },
      ],
      userEvent: "input.type",
    });
    expect(oversizedMount.adapter.pendingConflicts).toHaveLength(0);
    expect(oversizedMount.adapter.capacityOverflowRecord?.kind).toBe("conflict");
    expect(oversizedMount.adapter.pendingConflict?.intentions).toHaveLength(2);
    expect(oversizedMount.adapter.preservationBlocked).toBe(true);
    expect(localStorage.getItem(oversizedStorageKey)).toBeNull();
    const copiedOversized = oversizedMount.adapter.copyPendingRecovery();
    expect(copiedOversized?.displayText).toBe(`${largeChunk}\nS1`);
    expect(oversizedMount.adapter.lastCopiedSelection?.displayText).toBe(`${largeChunk}\nS1`);

    const shrinkRes = oversizedMount.adapter.resolvePendingConflict({
      action: "discard-intention",
      order: 0,
    });
    expect(shrinkRes.ok).toBe(true);
    expect(oversizedMount.adapter.capacityOverflowRecord).toBeNull();
    expect(oversizedMount.adapter.pendingConflicts).toHaveLength(1);
    expect(oversizedMount.adapter.pendingConflicts[0]!.intentions[0]!.insert).toBe("S1");
    expect(oversizedMount.adapter.preservationBlocked).toBe(false);
    expect(localStorage.getItem(oversizedStorageKey)).toBeTruthy();

    // Direct apply of an oversized first record from capacityOverflowRecord persists via DocumentReplica outbox
    const directOversizedMount = await mountPrettyEditor(
      '{"a":1,"b":2}',
      "oversized-direct.json",
      "inc-oversized-direct",
    );
    directOversizedMount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: largeChunk },
        { from: 3, to: 3, insert: "S2" },
      ],
      userEvent: "input.type",
    });
    expect(directOversizedMount.adapter.capacityOverflowRecord?.kind).toBe("conflict");
    const applyOversizedRes = directOversizedMount.adapter.resolvePendingConflict({
      action: "apply",
      order: 0,
      discardRemainingIntentions: true,
    });
    expect(applyOversizedRes.ok).toBe(true);
    expect(directOversizedMount.adapter.capacityOverflowRecord).toBeNull();
    expect(directOversizedMount.replica.currentText).toBe(`{${largeChunk}"a":1,"b":2}`);
  });

  it("Case 6f: renders user-accessible CodeMirror recovery tooltip and lint diagnostics (no extra chrome bars) for inspecting, copying/exporting, resolving, and dismissing retained input and storage/capacity alerts", async () => {
    configurePrettyRecoveryLimitsForTests({ maxRecords: 1, maxRetainedBytes: 256 * 1024 });
    const mount = await mountPrettyEditor(
      '{"a":1}\n{"b":2}',
      "ui-recovery.jsonl",
      "inc-ui-recovery",
    );

    // No extra chrome bars (cm-panels)
    expect(mount.view.dom.querySelectorAll(".cm-panels")).toHaveLength(0);
    expect(mount.view.dom.querySelector('[data-testid="pretty-recovery-tooltip"]')).toBeNull();

    // Trigger conflict 1 (durable) and conflict 2 (overflow)
    mount.view.dispatch({
      changes: [
        { from: 2, to: 2, insert: "U1a" },
        { from: 3, to: 3, insert: "U1b" },
      ],
      userEvent: "input.type",
    });
    const recB = mount.view.state.doc.toString().indexOf('{\n  "b"');
    mount.view.dispatch({
      changes: [
        { from: recB + 2, to: recB + 2, insert: "U2a" },
        { from: recB + 3, to: recB + 3, insert: "U2b" },
      ],
      userEvent: "input.type",
    });
    await Promise.resolve();

    const tooltip = mount.view.dom.querySelector<HTMLElement>(
      '[data-testid="pretty-recovery-tooltip"]',
    );
    expect(tooltip).not.toBeNull();
    expect(mount.view.dom.querySelectorAll(".cm-panels")).toHaveLength(0);
    expect(diagnosticCount(mount.view.state)).toBe(2);

    const statusAlert = tooltip!.querySelector('[data-testid="pretty-recovery-status"]');
    expect(statusAlert?.textContent).toContain(
      "Capacity limit reached: switched to raw-backed editing",
    );

    const c1Id = mount.adapter.pendingConflicts[0]!.conflictId;
    const c2Id = (mount.adapter.capacityOverflowRecord!.record as { conflictId: string }).conflictId;

    // Select overflow record C2 in the DOM tooltip and click Copy retained edit
    const selectC2Btn = mount.view.dom.querySelector<HTMLButtonElement>(
      `[data-testid="pretty-recovery-select-${c2Id}"]`,
    );
    expect(selectC2Btn).not.toBeNull();
    selectC2Btn!.click();

    const copyBtn = mount.view.dom.querySelector<HTMLButtonElement>(
      '[data-testid="pretty-recovery-copy"]',
    );
    expect(copyBtn).not.toBeNull();
    copyBtn!.click();

    const exportTextarea = mount.view.dom.querySelector<HTMLTextAreaElement>(
      '[data-testid="pretty-recovery-export-text"]',
    );
    expect(exportTextarea?.value).toContain("U2a\nU2b");
    expect(mount.adapter.lastCopiedSelection?.displayText).toBe("U2a\nU2b");
    expect(
      mount.view.dom.querySelector('[data-testid="pretty-recovery-feedback"]')?.textContent,
    ).toBe("Copied retained edit");

    // Dismiss intention #0 of C2 via the DOM button, then apply intention #1 of C2 via the DOM button
    const dismissInt0Btn = mount.view.dom.querySelector<HTMLButtonElement>(
      '[data-testid="pretty-recovery-discard-intention-0"]',
    );
    expect(dismissInt0Btn).not.toBeNull();
    dismissInt0Btn!.click();

    const applyInt1Btn = mount.view.dom.querySelector<HTMLButtonElement>(
      '[data-testid="pretty-recovery-apply-only-1"]',
    );
    expect(applyInt1Btn).not.toBeNull();
    applyInt1Btn!.click();
    expect(mount.replica.currentText).toBe('{"a":1}\n{U2b"b":2}');
    expect(mount.adapter.capacityOverflowRecord).toBeNull();

    // Now select C1 and click Dismiss conflict in the DOM; tooltip and diagnostics clear
    const selectC1Btn = mount.view.dom.querySelector<HTMLButtonElement>(
      `[data-testid="pretty-recovery-select-${c1Id}"]`,
    );
    selectC1Btn?.click();
    const dismissConflictBtn = mount.view.dom.querySelector<HTMLButtonElement>(
      '[data-testid="pretty-recovery-dismiss"]',
    );
    expect(dismissConflictBtn).not.toBeNull();
    dismissConflictBtn!.click();

    expect(mount.adapter.pendingConflicts).toHaveLength(0);
    expect(diagnosticCount(mount.view.state)).toBe(0);
    expect(mount.view.dom.querySelector('[data-testid="pretty-recovery-tooltip"]')).toBeNull();
  });
});
