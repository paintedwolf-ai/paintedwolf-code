import { acceptWindowNumber } from "../../platform/windows/window-identity-paint.ts";
import { deliverDocumentCommand, type PendingDocumentCommand } from "./document-command.ts";
import { dependenciesAccepted, updateDependencies } from "./document-causality.ts";
import { documentChangeHighlights } from "./document-change-highlights.ts";
import * as Y from "yjs";
import { documentPresence } from "./document-presence.ts";
import { documentAgentPresence } from "./document-agent-presence.ts";
import { DocumentFormatQueue, type DocumentFormat } from "./document-format.ts";
import { DocumentHistory } from "../history/document-history.ts";
import { documentReplacementChanges } from "./document-replacement-changes.ts";
import { documentEditorBinding, documentDeltaChanges, documentSynchronization, synchronizeEditor } from "./document-editor-binding.ts";
import { isolateHistory } from "@codemirror/commands";
import type { EditorView } from "@codemirror/view";
import { EditorState, Transaction, type ChangeSpec, type EditorSelection, type Extension } from "@codemirror/state";
import { DocumentByteBudget } from "./document-size.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import { BackendTransportError, isBackendUnreachableError } from "../../platform/connection/request-connectivity.ts";
import type { EditorDocument, EditorDocumentEvent, EditorReplicaFrame, EditorPresenceRange, RetainedReplica } from "../../api/types.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import {
  decodeUpdate, documentOutbox, encodeUpdate, recoveryBytes,
  outboxDocument, type OutboxRecord, type DocumentOutbox, type PendingDocumentUpdate, type ReplicaCheckpoint,
} from "./document-outbox.ts";

/** Refusals no later delivery of the same update can overcome. */
const REFUSAL_CODES = [
  "invalid_request",
  "invalid_json",
  "body_too_large",
  "source_content_too_large",
  "source_text_too_large",
  "editor_document_not_found",
] as const;

const REMOTE = Symbol("accepted document update");
const HUMAN_COMMAND = Symbol("accepted human document command");
/** This window's own preserved edits, replayed into undo history after an interruption. */
const RECOVERED_LOCAL = Symbol("recovered local document update");
const LOCAL = Symbol("local document command");
const SEND_DELAY_MS = 40;
/** Undo history is captured once typing pauses; text durability never waits for it. */
export const CHECKPOINT_IDLE_MS = 2000;
/** Un-checkpointed update bytes that force a checkpoint at the next preservation. */
const CHECKPOINT_BYTES = 4 * 1024 * 1024;
const MAX_PENDING_ENCODED_BYTES = 16 * 1024 * 1024;
const EPOCH_REFUSAL = "The document started a new history on the host, so your unsynchronized edits were set aside. Undo restores them.";

function refusalMessage(error: LycaonApiError, count: number): string {
  const edits = count === 1 ? "edit" : `${count} edits`;
  if (error.code === "source_content_too_large" || error.code === "source_text_too_large" || error.code === "body_too_large") {
    return `The host refused your last ${edits}: the file would exceed the 4 MiB editor limit. Undo restores the refused text.`;
  }
  if (error.code === "editor_document_not_found") {
    return `The host no longer has this document, so your last ${edits} could not be delivered. Undo restores the text.`;
  }
  return `The host refused your last ${edits} (${error.message}). Undo restores the refused text.`;
}

export type ReplicaStatus = "preserving" | "pending" | "accepted" | "error";
/** The chat focused while typing; the host records that chat's turn when it accepts the text. */
export type DocumentAuthorContext = { sessionId: string };

/** Completes a projected frame from its checkpoint and shares the decoded state with the replica. */
export function decodeDocumentState(document: EditorDocument, base?: ReplicaCheckpoint): { confirmed: Y.Doc; text: string } {
  const confirmed = new Y.Doc();
  if (base && base.documentId === document.id && base.epoch === document.epoch) Y.applyUpdate(confirmed, decodeUpdate(base.synchronized ? base.state : base.confirmed.crdt_update), REMOTE);
  Y.applyUpdate(confirmed, decodeUpdate(document.crdt_update), REMOTE);
  return { confirmed, text: confirmed.getText("text").toString() };
}

/** A synchronized checkpoint lets the host return only the missing state. */
export function retainedCheckpointIdentity(checkpoint: ReplicaCheckpoint): RetainedReplica | undefined {
  if (!checkpoint.synchronized) return undefined;
  return { document_id: checkpoint.documentId, epoch: checkpoint.epoch,
    state_vector: encodeUpdate(Y.encodeStateVectorFromUpdate(decodeUpdate(checkpoint.state))), base_sha256: "" };
}

export class DocumentReplica {
  doc = new Y.Doc();
  get text(): Y.Text { return this.doc.getText("text"); }
  readonly history = new DocumentHistory();
  private editor?: EditorView;
  private commandPending = false;
  private commandDelivery?: Promise<EditorDocument>;
  private lastHistoryCommand?: string;
  private synchronizeAfterCommand = false;
  readonly clientId = clientIdentity();
  status: ReplicaStatus = "accepted";
  error: string | null = null;
  /** What the host last refused and set aside, until the next accepted delivery. */
  refusal: string | null = null;
  accepted: EditorDocument;
  private readonly budget = new DocumentByteBudget();
  private confirmed: Y.Doc;
  private confirmedSnapshot: EditorDocument;
  private savedBase?: string;
  private checkpointFootprint?: { value: ReplicaCheckpoint; bytes: number };
  private readonly outbox: DocumentOutbox;
  private readonly formats: DocumentFormatQueue;
  private readonly records = new Map<string, PendingDocumentUpdate>();
  private checkpoint: ReplicaCheckpoint | undefined;
  private replicaId?: number;
  private persistence: Promise<void> = Promise.resolve();
  private delivery: Promise<void> | undefined;
  private synchronization: Promise<void> | undefined;
  private synchronizeAgain = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private recoveryTimer: ReturnType<typeof setTimeout> | undefined;
  private recoveryFailures = 0;
  private recovering = false;
  private recovery: Promise<void> | undefined;
  private preservationTimer: ReturnType<typeof setTimeout> | undefined;
  private checkpointTimer: ReturnType<typeof setTimeout> | undefined;
  private checkpointBytes = 0;
  private ready = false;
  private retentionEstablished = false;
  private historyInitialized = false;
  private preservedHistoryRevision = 0;
  private sequence = 0;
  /** Highest update sequence whose text and undo history a committed checkpoint carries. */
  private coveredSequence = 0;
  private readonly deliveryBoundaries = new Map<symbol, number>();
  private readonly observers = new Set<() => void>();
  private heartbeat: ReturnType<typeof setInterval> | undefined;
  private presenceTimer: ReturnType<typeof setTimeout> | undefined;
  private presenceDelivery: Promise<void> | undefined;
  private presenceAgain = false;
  private selection: { ranges: EditorPresenceRange[]; main: number } = { ranges: [], main: 0 };
  private readonly retiredRecords = new Map<string, PendingDocumentUpdate>();
  private readonly unpreserved = new Map<string, PendingDocumentUpdate>();
  private stopped = false;
  resolving = false;
  preservationBlocked = false;

  constructor(
    document: EditorDocument,
    private readonly client: () => LycaonClient | null,
    private readonly changed: (replica: DocumentReplica) => void,
    outbox = documentOutbox(),
    private readonly authorContext: () => DocumentAuthorContext = () => ({ sessionId: "" }),
    public incarnation: string = crypto.randomUUID(),
    confirmed?: Y.Doc,
    private retainHistory = true,
  ) {
    this.confirmed = confirmed ?? decodeDocumentState(document).confirmed;
    this.savedBase = document.dirty ? document.base_content : undefined;
    const { base_content: _base, command_history: _command, ...metadata } = document;
    this.accepted = { ...metadata, crdt_update: "", state_vector: "" };
    this.confirmedSnapshot = this.accepted;
    this.outbox = outbox;
    this.formats = new DocumentFormatQueue(outbox);
  }

  async initialize(recovered?: OutboxRecord[], offline = false): Promise<void> {
    const stored = recovered ?? await this.outbox.read(this.accepted.id);
    this.formats.restore(stored);
    const records = stored.filter((record) => record.clientId === this.clientId || ((record.kind === "update" || record.kind === "checkpoint") && record.epoch === this.accepted.epoch));
    this.checkpoint = records.find((record): record is ReplicaCheckpoint => record.kind === "checkpoint" && record.clientId === this.clientId);
    for (const record of records.filter((r): r is PendingDocumentUpdate => r.kind === "update").sort((a,b) => a.sequence-b.sequence)) {
      this.records.set(record.operationId, record);
      this.sequence = Math.max(this.sequence, record.sequence);
    }
    const historyCommands = stored.filter((record): record is PendingDocumentCommand => record.kind === "command"
      && record.clientId === this.clientId && !!record.historyBase);
    const unadopted = historyCommands.find((command) => command.operationId !== this.checkpoint?.lastHistoryCommand);
    if (unadopted) this.checkpoint = unadopted.historyBase;
    if (this.checkpoint) {
      this.incarnation = this.checkpoint.incarnation;
      this.replicaId = this.checkpoint.replicaId;
      this.retentionEstablished = (await this.outbox.inspect(this.accepted.id))?.retainedClients?.includes(this.clientId) ?? false;
    }
    this.lastHistoryCommand = this.checkpoint?.lastHistoryCommand;
    const staleCheckpoint = this.checkpoint != null && this.checkpoint.epoch !== this.accepted.epoch;
    // A stale checkpoint with pending edits is rebuilt so it can be set aside whole.
    if (this.checkpoint && (!staleCheckpoint || this.pendingCount > 0)) {
      Y.applyUpdate(this.doc, decodeUpdate(this.checkpoint.state), REMOTE);
    }
    if (this.replicaId) this.doc.clientID = this.replicaId;
    this.history.reset(this.text.toString(), this.checkpoint?.epoch === this.accepted.epoch ? this.checkpoint.history : undefined);
    this.preservedHistoryRevision = this.history.revision;
    this.historyInitialized = true;
    this.text.observe(this.onText);
    for (const command of historyCommands) {
      if (offline && !command.result && this.lastHistoryCommand !== command.operationId) this.commandPending = true;
      else await this.adoptHistoryCommand(command);
    }
    if (!staleCheckpoint || this.pendingCount > 0) {
      for (const checkpoint of records.filter((record): record is ReplicaCheckpoint => record.kind === "checkpoint")) {
        if (checkpoint.epoch === this.accepted.epoch || checkpoint.clientId === this.clientId) Y.applyUpdate(this.doc, decodeUpdate(checkpoint.state), REMOTE);
      }
      // Edits this window preserved after its last checkpoint re-enter its undo history in sequence.
      for (const record of this.records.values()) {
        Y.applyUpdate(this.doc, decodeUpdate(record.update), record.clientId === this.clientId ? RECOVERED_LOCAL : REMOTE);
      }
    }
    if (staleCheckpoint && this.pendingCount > 0) {
      this.retireUnacknowledged(EPOCH_REFUSAL);
    } else {
      // Confirmed state includes sync deltas received during outbox recovery.
      Y.applyUpdate(this.doc, Y.encodeStateAsUpdate(this.confirmed), REMOTE);
    }
    if (!offline) {
      try { await this.synchronize(); }
      catch (error) {
        if (!isBackendUnreachableError(error) || !this.replicaId || this.checkpoint?.epoch !== this.accepted.epoch) throw error;
        offline = true;
      }
    }
    if (this.checkpoint && staleCheckpoint && this.pendingCount === 0) {
      // Nothing local survives the epoch change; the host state is the recovery base again.
      await this.outbox.commit([], [this.checkpoint], { document: this.outboxAddress(), clientId: this.clientId, retained: false });
      this.retentionEstablished = false;
      this.checkpoint = undefined;
    }
    if (offline) {
      if (!this.replicaId || !this.checkpoint?.incarnation) throw new Error("The preserved editor replica identity is unavailable.");
      this.doc.clientID = this.replicaId;
    }
    this.doc.on("update", this.onUpdate);
    this.ready = true;
    this.heartbeat = setInterval(() => this.sendPresence(), 25_000);
    this.notify();
    if (offline) this.scheduleRecovery();
    else if (this.pendingCount > 0) this.scheduleDelivery();
  }

  subscribe(observer: () => void): () => void {
    this.observers.add(observer);
    return () => this.observers.delete(observer);
  }

  get initialized(): boolean { return this.ready; }
  get awaitingCommand(): boolean { return this.commandPending; }

  /** What a reopen sends so the host projects only the state this replica lacks. */
  retainedIdentity(): RetainedReplica {
    return { document_id: this.accepted.id, epoch: this.confirmedSnapshot.epoch,
      state_vector: encodeUpdate(Y.encodeStateVector(this.confirmed)), base_sha256: this.confirmedSnapshot.base_sha256 };
  }

  private notify(): void {
    if (!this.ready || this.stopped) return;
    this.changed(this);
    for (const observer of this.observers) observer();
  }

  publishPresence(selection: EditorSelection): void {
    if (this.stopped) return;
    const encode = (position: number) => encodeUpdate(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(this.text, position)));
    const ranges = selection.ranges.length <= 256 ? selection.ranges : [selection.main];
    this.selection = { ranges: ranges.map(range => ({ anchor: encode(range.anchor), head: encode(range.head) })),
      main: ranges.length === selection.ranges.length ? selection.mainIndex : 0 };
    if (this.presenceTimer) return;
    this.presenceTimer = setTimeout(() => {
      this.presenceTimer = undefined;
      this.sendPresence();
    }, 75);
  }

  clearPresence(): void {
    clearTimeout(this.presenceTimer);
    this.presenceTimer = undefined;
    this.selection = { ranges: [], main: 0 };
    this.sendPresence();
  }

  private sendPresence(): void {
    if (this.closure || this.stopped) return;
    this.presenceAgain = true;
    if (this.presenceDelivery) return;
    this.presenceDelivery = this.deliverPresence().finally(() => {
      this.presenceDelivery = undefined;
      if (this.presenceAgain) this.sendPresence();
    });
  }

  private async deliverPresence(): Promise<void> {
    while (this.presenceAgain && !this.closure && !this.stopped) {
      this.presenceAgain = false;
      const client = this.client();
      if (!client) return;
      const publish = () => client.publishEditorDocumentPresence(this.accepted.project_id, this.accepted.id,
        { client_id: this.clientId, incarnation: this.incarnation, ...this.selection });
      try { await publish(); }
      catch (error) {
        if (!(error instanceof LycaonApiError) || error.code !== "editor_replica_identity") continue;
        try {
          await this.synchronize();
          if (!this.closure && !this.stopped) await publish();
        } catch { /* The heartbeat retries after connectivity recovers. */ }
      }
    }
  }

  private persist(run: () => Promise<void>): Promise<void> {
    this.persistence = this.persistence.catch(() => undefined).then(run);
    return this.persistence;
  }

  /** Commits pending updates, retirements, and format intents in one transaction; a checkpoint joins when due. */
  async preserve({ checkpoint: requested = false } = {}): Promise<void> {
    if (!this.historyInitialized) return;
    clearTimeout(this.preservationTimer);
    this.preservationTimer = undefined;
    try {
      await this.persist(async () => {
        this.preserveBatch();
        const pending = [...this.unpreserved.values()];
        const retired = [...this.retiredRecords.values()];
        const formats = this.formats.unpreserved();
        // Typing continues while the commit is in flight; only the captured boundary is acknowledged.
        const retain = this.retainHistory && this.history.hasHistory && !this.retentionEstablished;
        const captured = (this.checkpointDue(requested, pending.length + formats.length > 0) || (retain && !this.checkpoint))
          ? { checkpoint: this.captureCheckpoint(), revision: this.history.revision, bytes: this.checkpointBytes, sequence: this.sequence } : undefined;
        // An acknowledged update leaves storage only once a checkpoint carries its undo history.
        const covered = [...this.records.values()].filter((record) => record.acknowledged && record.sequence <= (captured?.sequence ?? this.coveredSequence));
        // Retention and its covering checkpoint become durable in the same transaction.
        const checkpoint = captured?.checkpoint;
        if (retain || checkpoint || pending.length || retired.length || formats.length || covered.length) {
          await this.outbox.commit([...(checkpoint ? [checkpoint] : []), ...pending, ...formats], [...retired, ...covered],
            retain ? { document: this.outboxAddress(), clientId: this.clientId, retained: true } : undefined);
          if (retain) this.retentionEstablished = true;
        }
        if (captured) {
          this.checkpoint = captured.checkpoint;
          this.preservedHistoryRevision = captured.revision;
          this.checkpointBytes -= captured.bytes;
          this.coveredSequence = captured.sequence;
          clearTimeout(this.checkpointTimer);
          this.checkpointTimer = undefined;
          if (this.history.revision !== captured.revision) this.scheduleCheckpoint();
        }
        for (const record of pending) this.unpreserved.delete(record.operationId);
        for (const record of retired) this.retiredRecords.delete(record.operationId);
        for (const record of covered) this.records.delete(record.operationId);
        this.formats.preserved(formats);
      });
      if (this.preservationBlocked) { this.preservationBlocked = false; this.notify(); }
    } catch (error) {
      this.preservationBlocked = true;
      this.fail(error);
      throw error;
    }
  }

  /** First local work establishes a recovery base; later checkpoints follow idle, size, or base changes. */
  private checkpointDue(requested: boolean, newLocalWork: boolean): boolean {
    if (!this.checkpoint) return newLocalWork || this.pendingCount > 0;
    if (this.checkpointBytes >= CHECKPOINT_BYTES) return true;
    if (this.checkpoint.path !== this.accepted.path || this.checkpoint.epoch !== this.accepted.epoch
      || this.checkpoint.lastHistoryCommand !== this.lastHistoryCommand || this.checkpoint.replicaId !== this.replicaId || this.checkpoint.incarnation !== this.incarnation) return true;
    return requested && (this.preservedHistoryRevision !== this.history.revision
      || this.checkpoint.confirmed.revision !== this.confirmedSnapshot.revision
      || this.checkpoint.confirmed.base_sha256 !== this.confirmedSnapshot.base_sha256);
  }

  private scheduleCheckpoint(): void {
    if (this.checkpointTimer || this.stopped) return;
    this.checkpointTimer = setTimeout(() => {
      this.checkpointTimer = undefined;
      void this.preserve({ checkpoint: true }).catch(() => undefined);
    }, CHECKPOINT_IDLE_MS);
  }

  private preserveBatch(): void {
    if (this.deliveryBoundaries.size) return;
    const context = this.authorContext();
    const records = [...this.records.values()].filter((record) => !record.acknowledged && !record.attempted
      && record.sessionId === context.sessionId
      && record.clientId === this.clientId && record.replicaId === this.doc.clientID && record.epoch === this.accepted.epoch);
    if (records.length < 2) return;
    const latest = records[records.length - 1]!;
    const merged: PendingDocumentUpdate = { ...latest, operationId: crypto.randomUUID(),
      update: encodeUpdate(Y.mergeUpdates(records.map((record) => decodeUpdate(record.update)))) };
    this.records.set(merged.operationId, merged);
    this.unpreserved.set(merged.operationId, merged);
    for (const record of records) {
      this.records.delete(record.operationId);
      this.unpreserved.delete(record.operationId);
      this.retiredRecords.set(record.operationId, record);
    }
  }

  private scheduleRecovery(): void {
    if (!this.ready || this.stopped || this.recoveryTimer || this.recovering) return;
    const delay = Math.min(60_000, 1500 * 2 ** Math.min(this.recoveryFailures++, 6));
    this.recoveryTimer = setTimeout(() => {
      this.recoveryTimer = undefined;
      this.recovery = this.recover().finally(() => { this.recovery = undefined; });
    }, delay);
  }

  private async recover(): Promise<void> {
    if (this.stopped) return;
    this.recovering = true;
    try {
      await this.preserve();
      if (this.stopped) return;
      for (const record of await this.outbox.read(this.accepted.id)) {
        if (this.stopped) return;
        if (record.kind === "command" && record.historyBase && record.clientId === this.clientId) await this.adoptHistoryCommand(record);
      }
      if (this.stopped) return;
      await this.synchronize();
      if (!this.stopped) await this.flush();
    } catch (error) { this.fail(error); }
    finally {
      this.recovering = false;
      if (this.status === "error") this.scheduleRecovery();
      else if (this.pendingCount) this.scheduleDelivery();
    }
  }

  get pendingCount(): number {
    return this.formats.count + [...this.records.values()].filter((record) => !record.acknowledged).length;
  }

  get format(): DocumentFormat { return this.formats.current(this.accepted); }

  /** Later typing stays local until the save pins its accepted revision. */
  holdLaterDelivery(): () => void {
    const boundary = Symbol("save revision");
    this.deliveryBoundaries.set(boundary, this.sequence);
    return () => {
      if (this.deliveryBoundaries.delete(boundary)) this.scheduleDelivery();
    };
  }

  setFormat(format: DocumentFormat): boolean {
    if (!this.formats.stage(this.accepted, this.clientId, format, this.authorContext())) return false;
    this.scheduleDelivery();
    return true;
  }

  get authorship(): { session_id: string } {
    return { session_id: this.authorContext().sessionId };
  }

  get capacityBlocked(): boolean {
    return [...this.records.values()].reduce((bytes, record) => bytes + record.update.length, 0) >= MAX_PENDING_ENCODED_BYTES;
  }

  /** The host's 4 MiB cap on saved bytes, applied before the edit enters the document. */
  private checkLocalAdmission(transaction: Transaction): void {
    if (!this.ready || this.stopped || this.resolving || this.commandPending || this.preservationBlocked) throw new Error("The editor cannot accept changes until synchronization or local storage is available.");
    if (this.capacityBlocked) throw new Error("The pending draft reached its preservation limit. Editing will resume when pending changes synchronize.");
    const format = this.format;
    if (!this.budget.admits(transaction.startState.doc, transaction.newDoc, transaction.changes, { encoding: this.accepted.encoding, eol: format.eol })) {
      throw new Error("This edit would take the file past the 4 MiB editor limit.");
    }
  }

  get extension(): Extension {
    return [EditorState.transactionFilter.of((transaction) => {
      if (!transaction.docChanged || transaction.annotation(documentSynchronization)) return transaction;
      try { this.checkLocalAdmission(transaction); return transaction; }
      catch (cause) { queueMicrotask(() => { if (!this.stopped) this.fail(cause); }); return []; }
    }), documentChangeHighlights, documentPresence(this), documentAgentPresence(this), documentEditorBinding(this)];
  }

  replaceLocal(text: string): void {
    const before = this.text.toString();
    if (before === text) return;
    const changes = documentReplacementChanges(this.history.state.doc, this.history.state.changes({ from: 0, to: before.length, insert: text }));
    this.applyLocalChanges(changes, "input.replace");
  }

  /** Applies exact local edits as one undo group, under the same admission as typing. */
  applyLocalChanges(changes: ChangeSpec, userEvent: string): void {
    const transaction = this.history.state.update({ changes, userEvent, annotations: isolateHistory.of("full") });
    if (!transaction.docChanged) return;
    this.checkLocalAdmission(transaction);
    this.applyHistoryTransaction(transaction);
  }

  attachEditor(view: EditorView): void {
    this.editor = view;
    if (!this.history.state.selection.eq(view.state.selection, true)) {
      this.history.apply({ selection: view.state.selection, annotations: Transaction.addToHistory.of(false) });
    }
  }

  detachEditor(view: EditorView): void {
    if (this.editor === view) this.editor = undefined;
    this.history.boundary();
  }

  acceptEditorTransaction(transaction: Transaction): void {
    if (!transaction.docChanged && !transaction.selection && !transaction.annotation(isolateHistory)) return;
    if (transaction.docChanged) this.checkLocalAdmission(transaction);
    const timestamp = transaction.annotation(Transaction.time);
    const isolation = transaction.annotation(isolateHistory);
    const historyTransaction = this.history.state.update({ changes: documentReplacementChanges(transaction.startState.doc, transaction.changes), selection: transaction.selection,
      annotations: [...(timestamp === undefined ? [] : [Transaction.time.of(timestamp)]),
        Transaction.addToHistory.of(transaction.annotation(Transaction.addToHistory) !== false),
        ...(isolation === undefined ? [] : [isolateHistory.of(isolation)])],
      userEvent: transaction.annotation(Transaction.userEvent) });
    this.applyHistoryTransaction(historyTransaction, false);
  }

  stepHistory(direction: "undo" | "redo"): void {
    if (this.editor?.state.readOnly) return;
    try {
      this.history.step(direction, (transaction) => {
        if (transaction.docChanged) this.checkLocalAdmission(transaction);
        this.applyHistoryTransaction(transaction);
      });
    } catch (error) { this.fail(error); }
  }

  private applyHistoryTransaction(transaction: Transaction, render = true): void {
    this.history.state = transaction.state;
    if (transaction.docChanged) this.doc.transact(() => {
      let offset = 0;
      transaction.changes.iterChanges((from, to, _newFrom, _newTo, insert) => {
        this.text.delete(from + offset, to - from);
        if (insert.length) this.text.insert(from + offset, insert.toString());
        offset += insert.length - (to - from);
      });
    }, LOCAL);
    if (render) synchronizeEditor(this.editor, transaction);
  }

  private onText = (event: Y.YTextEvent, transaction: Y.Transaction): void => {
    if (transaction.origin === LOCAL) return;
    const undoable = transaction.origin === HUMAN_COMMAND || transaction.origin === RECOVERED_LOCAL;
    const change = this.history.apply({ changes: documentDeltaChanges(event.delta),
      userEvent: transaction.origin === HUMAN_COMMAND ? "input.command" : undefined,
      annotations: [Transaction.addToHistory.of(undoable), ...(undoable ? [isolateHistory.of("full")] : [])] });
    synchronizeEditor(this.editor, change, !undoable);
  };

  executeCommand(command: PendingDocumentCommand): Promise<EditorDocument> {
    if (this.commandDelivery) return Promise.reject(new Error("An editor action is already in progress."));
    const delivery = this.executeHistoryCommand(command);
    this.commandDelivery = delivery;
    void delivery.finally(() => { if (this.commandDelivery === delivery) this.commandDelivery = undefined; }).catch(() => undefined);
    return delivery;
  }

  private async executeHistoryCommand(command: PendingDocumentCommand): Promise<EditorDocument> {
    if (this.stopped) throw new Error("Reopen the document before applying this action.");
    if (this.commandPending) throw new Error("The previous editor action is still being synchronized.");
    this.resolving = true;
    this.notify();
    try {
      await this.flush();
      if (command.action === "save") throw new Error("Save does not change document history.");
      const recorded: PendingDocumentCommand = { ...command, historyBase: this.captureCheckpoint() };
      recorded.request = { ...command.request, history_vector: encodeUpdate(Y.encodeStateVector(this.doc)) };
      this.commandPending = true;
      return await this.adoptHistoryCommand(recorded);
    } finally { this.resolving = false; this.notify(); }
  }

  private async adoptHistoryCommand(command: PendingDocumentCommand): Promise<EditorDocument> {
    const client = this.client();
    if (this.lastHistoryCommand === command.operationId) {
      await this.preserve();
      await this.outbox.remove(command);
      this.commandPending = false;
      this.resumeCommandSynchronization();
      return this.accepted;
    }
    this.commandPending = true;
    try {
      return await deliverDocumentCommand(client, command, this.outbox, async (document) => {
        const effect = document.command_history;
        if (!effect || effect.operation_id !== command.operationId || effect.epoch !== this.accepted.epoch) {
          throw new Error("The editor action could not be recovered into this document's history. Its preserved work remains on this device.");
        }
        Y.applyUpdate(this.doc, decodeUpdate(effect.before_update), REMOTE);
        Y.applyUpdate(this.doc, decodeUpdate(effect.update), HUMAN_COMMAND);
        this.lastHistoryCommand = command.operationId;
        this.commandPending = false;
        this.receive(document);
        this.resumeCommandSynchronization();
        await this.preserve();
      });
    } catch (error) {
      const retained = await this.outbox.read(this.accepted.id);
      if (!retained.some((record) => record.kind === "command" && record.operationId === command.operationId)) {
        this.commandPending = false;
        this.resumeCommandSynchronization();
      }
      this.fail(error);
      throw error;
    }
  }

  private resumeCommandSynchronization(): void {
    if (!this.synchronizeAfterCommand) return;
    this.synchronizeAfterCommand = false;
    void this.synchronize().catch((error: unknown) => this.fail(error));
  }

  private onUpdate = (update: Uint8Array, origin: unknown): void => {
    if (origin === REMOTE || origin === HUMAN_COMMAND || !this.ready || this.stopped) return;
    const record: PendingDocumentUpdate = {
      kind: "update", ...outboxDocument(this.accepted),
      clientId: this.clientId, replicaId: this.doc.clientID, epoch: this.accepted.epoch,
      operationId: crypto.randomUUID(), update: encodeUpdate(update), acknowledged: false, sequence: ++this.sequence,
      sessionId: this.authorContext().sessionId,
    };
    this.records.set(record.operationId, record);
    this.status = "preserving";
    queueMicrotask(() => { if (!this.stopped) this.notify(); });
    this.unpreserved.set(record.operationId, record);
    this.checkpointBytes += record.update.length;
    this.scheduleCheckpoint();
    if (!this.preservationTimer) this.preservationTimer = setTimeout(() => {
      this.preservationTimer = undefined;
      void this.preserve().then(() => {
        this.status = "pending";
        this.notify();
        this.scheduleDelivery();
      }).catch((error: unknown) => this.fail(error));
    }, SEND_DELAY_MS);
  };

  private captureCheckpoint(): ReplicaCheckpoint {
    if (!this.replicaId) throw new Error("The host has not allocated this editor replica.");
    const { base_content: _base, command_history: _command, ...metadata } = this.confirmedSnapshot;
    return { kind: "checkpoint", documentId: this.accepted.id, projectId: this.accepted.project_id,
      replicaId: this.replicaId, incarnation: this.incarnation,
      confirmed: { ...metadata, participants: [], crdt_update: encodeUpdate(Y.encodeStateAsUpdate(this.confirmed)) },
      rootId: this.accepted.root_id, fileId: this.accepted.file_id, path: this.accepted.path, clientId: this.clientId, epoch: this.accepted.epoch, state: encodeUpdate(Y.encodeStateAsUpdate(this.doc)), synchronized: this.pendingCount === 0, pendingOperations: [...this.records.values()].filter((record) => !record.acknowledged).map((record) => record.operationId), history: this.history.snapshot(), lastHistoryCommand: this.lastHistoryCommand };
  }

  private scheduleDelivery(): void {
    if (this.timer || this.recoveryTimer || this.recovering || this.stopped || this.resolving || this.deliveryBoundaries.size) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.flush().catch((error: unknown) => this.fail(error));
    }, SEND_DELAY_MS);
  }

  async flush(): Promise<void> {
    clearTimeout(this.timer);
    this.timer = undefined;
    const previous = this.delivery ?? Promise.resolve();
    const delivery = previous.catch(() => undefined).then(async () => {
      await this.preserve();
      await this.deliver(await this.sealBatch());
      const client = this.client();
      if (this.formats.count && client) {
        await this.formats.deliver(client, () => ({ document: this.accepted, text: this.acceptedText }), (document) => this.receive(document), () => this.synchronize());
        this.status = this.pendingCount ? "pending" : "accepted";
        this.notify();
      }
    });
    this.delivery = delivery;
    let succeeded = false;
    try {
      await delivery;
      succeeded = true;
      this.recoveryFailures = 0;
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = undefined;
    } catch (error) { this.fail(error); throw error; }
    finally {
      if (this.delivery === delivery) {
        this.delivery = undefined;
        if (succeeded && this.pendingCount) this.scheduleDelivery();
      }
    }
  }

  private async sealBatch(): Promise<PendingDocumentUpdate[]> {
    let through: PendingDocumentUpdate[] = [];
    await this.persist(async () => {
      const limit = Math.min(...this.deliveryBoundaries.values());
      through = [...this.records.values()].filter((record) => !record.acknowledged && record.sequence <= limit);
      for (const record of through) {
        if (record.attempted) continue;
        await this.outbox.put({ ...record, attempted: true });
        record.attempted = true;
      }
    });
    return through;
  }

  private async deliver(through: PendingDocumentUpdate[]): Promise<void> {
    const client = this.client();
    if (!client) throw new Error("The editor is offline. Your preserved edits will synchronize after reconnecting.");
    const remaining = through.filter((record) => !record.acknowledged);
    const dependencies = new Map(remaining.map((record) => [record.operationId, updateDependencies(decodeUpdate(record.update))]));
    while (remaining.length) {
      const accepted = Y.decodeStateVector(Y.encodeStateVector(this.confirmed));
      const ready = remaining.findIndex((record) => {
        const dependency = dependencies.get(record.operationId);
        return dependency !== undefined && dependenciesAccepted(dependency, accepted);
      });
      if (ready < 0) throw new Error("Waiting for earlier editor changes to synchronize.");
      const record = remaining.splice(ready, 1)[0]!;
      let response: EditorReplicaFrame;
      try {
        response = await client.submitEditorDocumentUpdate(this.accepted.project_id, this.accepted.id, {
          client_id: record.clientId, replica_id: record.replicaId, epoch: record.epoch,
          operation_id: record.operationId, update: record.update,
          ...(record.sessionId ? { session_id: record.sessionId } : {}),
          state_vector: encodeUpdate(Y.encodeStateVector(this.confirmed)),
          base_sha256: this.confirmedSnapshot.base_sha256,
        });
      } catch (error) {
        if (error instanceof LycaonApiError) {
          if (error.code === "editor_replica_epoch" || error.code === "editor_replica_identity") await this.synchronize();
          else if (isApiErrorCode(error, REFUSAL_CODES)) {
            // Every later record depends on this one through its clocks.
            this.retireUnacknowledged(refusalMessage(error, remaining.length + 1));
            if (error.code === "editor_document_not_found") throw error;
            return;
          }
        }
        throw error;
      }
      if (response.accepted_operation_id !== record.operationId || !response.accepted_revision) {
        throw new Error("The host did not acknowledge this editor operation.");
      }
      await this.persist(() => this.outbox.put({ ...record, acknowledged: true }));
      record.acknowledged = true;
      this.receive(response);
    }
    await this.compact();
    this.status = this.pendingCount ? "pending" : "accepted";
    this.error = null;
    this.refusal = null;
    this.notify();
  }

  receive(frame: EditorDocument | EditorReplicaFrame): void {
    if (this.commandPending) { this.synchronizeAfterCommand = true; return; }
    if (this.stopped || frame.revision < this.confirmedSnapshot.revision) return;
    if (frame.epoch !== this.confirmedSnapshot.epoch) {
      this.confirmed.destroy();
      this.confirmed = new Y.Doc();
    }
    if (frame.dirty && this.savedBase === undefined && frame.base_sha256 === this.confirmedSnapshot.base_sha256) this.savedBase = this.confirmed.getText("text").toString();
    if (frame.crdt_update) Y.applyUpdate(this.confirmed, decodeUpdate(frame.crdt_update), REMOTE);
    if (!frame.dirty) this.savedBase = undefined;
    else if (frame.base_content !== undefined) this.savedBase = frame.base_content;
    const acceptedFrame: EditorDocument = { ...this.confirmedSnapshot, ...frame };
    const { base_content: _base, command_history: _command, ...metadata } = acceptedFrame;
    const document: EditorDocument = { ...this.confirmedSnapshot, ...metadata, crdt_update: "", state_vector: "" };
    this.confirmedSnapshot = document;
    if (document.epoch !== this.accepted.epoch) {
      // Nothing pending can join a new history; it is set aside one undo away.
      this.accepted = document;
      if (this.pendingCount > 0) this.retireUnacknowledged(EPOCH_REFUSAL);
      else this.resetToConfirmed(null);
    } else if (frame.crdt_update) Y.applyUpdate(this.doc, decodeUpdate(frame.crdt_update), REMOTE);
    this.accepted = document;
    acceptWindowNumber(document.participants.find(participant => participant.client_id === this.clientId)?.window_number);
    this.notify();
  }

  /** Sets aside every unacknowledged update; its text stays one undo away. */
  private retireUnacknowledged(reason: string): void {
    const refused = [...this.records.values()].filter((record) => !record.acknowledged);
    for (const record of refused) {
      this.records.delete(record.operationId);
      this.unpreserved.delete(record.operationId);
      this.retiredRecords.delete(record.operationId);
    }
    if (refused.length) {
      void this.persist(() => this.outbox.commit([], refused)).catch((error: unknown) => this.fail(error));
    }
    this.resetToConfirmed(reason);
  }

  /** Rebuilds the local document from the host's accepted state under the same replica identity. */
  private resetToConfirmed(reason: string | null): void {
    this.doc.off("update", this.onUpdate);
    if (this.historyInitialized) this.text.unobserve(this.onText);
    const before = this.text.toString();
    this.doc.destroy();
    this.doc = new Y.Doc();
    if (this.replicaId) this.doc.clientID = this.replicaId;
    Y.applyUpdate(this.doc, Y.encodeStateAsUpdate(this.confirmed), REMOTE);
    const accepted = this.text.toString();
    this.lastHistoryCommand = undefined;
    let transaction: Transaction | undefined;
    if (reason !== null && before !== accepted && this.historyInitialized) {
      transaction = this.history.apply({ changes: { from: 0, to: this.history.state.doc.length, insert: accepted },
        userEvent: "input.refused", annotations: isolateHistory.of("full") });
    } else {
      this.history.reset(accepted);
    }
    if (this.historyInitialized) this.text.observe(this.onText);
    if (this.ready) this.doc.on("update", this.onUpdate);
    if (transaction) synchronizeEditor(this.editor, transaction);
    else this.editor?.dispatch({ changes: { from: 0, to: this.editor.state.doc.length, insert: accepted },
      selection: this.history.state.selection, annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)] });
    this.refusal = reason;
    this.status = this.pendingCount ? "pending" : "accepted";
    this.notify();
  }

  async receiveEvent(event: EditorDocumentEvent): Promise<void> {
    if (this.stopped || event.revision < this.confirmedSnapshot.revision) return;
    if (event.epoch === this.confirmedSnapshot.epoch && event.revision === this.confirmedSnapshot.revision) {
      const metadata = { ...event, epoch: event.epoch, published_revision: event.published_revision ?? this.confirmedSnapshot.published_revision,
        participants: event.participants ?? this.confirmedSnapshot.participants };
      this.accepted = { ...this.accepted, ...metadata };
      this.confirmedSnapshot = { ...this.confirmedSnapshot, ...metadata };
      this.notify();
      return;
    }
    // Even a metadata event can follow missed text or change the saved base.
    await this.synchronize();
  }

  async synchronize(): Promise<void> {
    if (this.synchronization) { this.synchronizeAgain = true; return this.synchronization; }
    this.synchronization = (async () => {
      const client = this.client();
      if (!client) throw new BackendTransportError(new Error("The editor is offline."), "unreachable");
      do {
      this.synchronizeAgain = false;
      const incoming = await client.syncEditorDocument(this.accepted.project_id, this.accepted.id, {
        client_id: this.clientId, incarnation: this.incarnation, epoch: this.confirmedSnapshot.epoch,
        state_vector: encodeUpdate(Y.encodeStateVector(this.confirmed)),
          base_sha256: this.confirmedSnapshot.base_sha256,
      });
      if (!incoming.replica_id) throw new Error("The host did not allocate an editor replica.");
      this.receive(incoming);
      this.replicaId = incoming.replica_id;
      this.doc.clientID = incoming.replica_id;
      } while (this.synchronizeAgain && !this.stopped);
    })();
    try { await this.synchronization; } finally { this.synchronization = undefined; }
  }

  /** Retires covered acknowledgements; a long acknowledged run asks for the checkpoint that covers it. */
  private async compact(): Promise<void> {
    if (this.records.size < 64 && this.pendingCount > 0) return;
    await this.preserve({ checkpoint: this.records.size >= 64 });
  }

  private fail(error: unknown): void {
    this.error = error instanceof Error ? error.message : String(error);
    this.status = "error";
    clearTimeout(this.timer);
    this.timer = undefined;
    this.scheduleRecovery();
    this.notify();
  }

  private outboxAddress() {
    const d = this.accepted;
    return { documentId: d.id, projectId: d.project_id, rootId: d.root_id, fileId: d.file_id, path: d.path };
  }

  async releaseRetention(): Promise<void> {
    await this.outbox.commit([], [], { document: this.outboxAddress(), clientId: this.clientId, retained: false });
    this.retentionEstablished = false;
    this.retainHistory = false;
  }

  get acceptedText(): string { return this.confirmed.getText("text").toString(); }
  get baseText(): string { return this.savedBase ?? (this.accepted.dirty ? "" : this.acceptedText); }

  /** Includes both CRDT representations, editor text and retained serialized history. */
  get estimatedBytes(): number {
    if (this.checkpoint && this.checkpointFootprint?.value !== this.checkpoint) {
      this.checkpointFootprint = { value: this.checkpoint, bytes: recoveryBytes(this.checkpoint) };
    }
    return 65536 + (this.savedBase?.length ?? 0) * 2 + this.text.length * 12 + this.history.estimatedBytes + this.history.state.doc.lines * 16
      + (this.checkpoint ? this.checkpointFootprint?.bytes ?? 0 : 0)
      + [...this.records.values()].reduce((sum, record) => sum + record.update.length * 2, 0);
  }

  private materialized?: { revision: number; text: string };
  get currentText(): string {
    if (this.materialized?.revision !== this.history.revision) {
      this.materialized = { revision: this.history.revision, text: this.text.toString() };
    }
    return this.materialized.text;
  }

  get localGeneration(): number { return this.history.revision; }
  get dirty(): boolean {
    return this.accepted.dirty || this.pendingCount > 0 || this.formats.count > 0;
  }

  private closure?: Promise<boolean>;

  async close(): Promise<void> {
    if (!(await this.suspend(() => true))) await this.close();
  }

  suspend(current: () => boolean): Promise<boolean> {
    if (this.closure) return this.closure;
    if (this.stopped) return Promise.resolve(true);
    const transition = this.closePreserved(current).then(closed => {
      if (!closed) this.closure = undefined;
      return closed;
    }, (error: unknown) => {
      this.closure = undefined;
      throw error;
    });
    this.closure = transition;
    return transition;
  }

  private async closePreserved(current = () => true): Promise<boolean> {
    await this.commandDelivery?.catch(() => undefined);
    for (;;) {
      const revision = this.history.revision;
      await this.preserve({ checkpoint: true });
      if (!current()) return false;
      if (revision === this.history.revision) break;
    }
    this.stopped = true;
    clearTimeout(this.timer);
    clearTimeout(this.checkpointTimer);
    clearTimeout(this.presenceTimer);
    clearTimeout(this.recoveryTimer);
    clearInterval(this.heartbeat);
    this.observers.clear();
    this.doc.off("update", this.onUpdate);
    const client = this.client();
    const release = () => {
      // Pending joins settle before departure.
      if (client) void Promise.resolve().then(() => client.leaveEditorDocument(this.accepted.project_id, this.accepted.id, {
        client_id: this.clientId, incarnation: this.incarnation,
      })).catch(() => undefined);
      if (this.historyInitialized) this.text.unobserve(this.onText);
      this.doc.destroy();
      this.confirmed.destroy();
    };
    if (this.delivery || this.synchronization || this.presenceDelivery || this.recovery) {
      await Promise.allSettled([this.delivery, this.synchronization, this.presenceDelivery, this.recovery]);
      release();
    } else release();
    return true;
  }
}
