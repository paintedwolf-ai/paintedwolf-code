import type { EditorEditingBlock } from "./editor-draft.ts";
import { applyFilesBufferDocumentStatus, projectFilesState, suspendFilesBufferContent } from "./files-buffer-state.ts";
import { batch } from "solid-js";
import { documentResidency, documentResourceKey, documentOpeningReservation, documentAdmissionBytes, type DocumentReservation, type ResidencyPriority } from "./document-residency.ts";
import { suspendFileDocument } from "./files-residency.ts";
import { refreshFileDocumentRetention } from "./files-document-metadata.ts";
import { documentCommand, resumeDocumentCommands } from "./document-command.ts";
import { refreshFilesCollaborationEpoch } from "../editor/files-editor-collaboration.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { createStore, reconcile } from "solid-js/store";
import type { LycaonClient } from "../../api/client.ts";
import type { EditorDocument, EditorDocumentEvent, SecretScreen, SecretScreenStatus, SourceEncoding } from "../../api/types.ts";
import { clientIdentity, prepareClientIdentity } from "../../platform/connection/client-identity.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";
import type { EditorDraft, EditorDraftSource } from "./editor-draft.ts";
import { flushFilesDraftSync } from "./files-draft-sync.ts";
import { DocumentReplica, decodeDocumentState, retainedCheckpointIdentity, type ReplicaStatus } from "./document-replica.ts";
import { documentOutbox, recoveryBytes, readReservedRecovery, type ReplicaCheckpoint } from "./document-outbox.ts";
import type { RetainedReplica } from "../../api/types.ts";
import type { LoadedDocumentText } from "./project-files-buffers.ts";
import type { FileBuffer } from "./files-buffer-state.ts";
import type * as Y from "yjs";
import { wakeDocumentOutbox } from "./document-outbox-relay.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { SourceWorkspaceMismatchError } from "../source/source-workspace-identity.ts";


export type EditorDocumentState = {
  projectId: string;
  workspaceId: string;
  documentId: string;
  fileId: string;
  rootId: string;
  path: string;
  localGeneration: number;
  dirty: boolean;
  baseSha256: string | null;
  encoding: SourceEncoding;
  sizeBytes: number;
  eol: EolKind;
  baseEol: EolKind;
  mixedEol: boolean;
  baseMixedEol: boolean;
  revision: number;
  diverged: boolean;
  /** The path has no file on disk; the document holds the draft. */
  absent: boolean;
  heldAgentVersionId: string | null;
  secretScreen?: SecretScreen;
  secretScreenStatus: SecretScreenStatus;
  synchronization: ReplicaStatus;
  pending: number;
  epoch: number;
  participants: number;
  /** Client identities of every live participant, this window included. */
  participantIds: string[];
  resolving: boolean;
  editingBlock: EditorEditingBlock | null;
  /** The host's last refusal of this window's edits, until its next accepted delivery. */
  refusal: string | null;
};

type Connection = { drafts: EditorDraftSource; client: () => LycaonClient | null; sessionId: () => string | undefined; workspaceId?: () => string };
export type EditorDocumentListener = (document: EditorDocumentState) => void;
const [documents, setDocuments] = createStore<Record<string, EditorDocumentState | undefined>>({});
const replicas = new Map<string, DocumentReplica>();
const listeners = new Set<EditorDocumentListener>();
const connections = new Map<string, Connection[]>();
const retiring = new Map<string, Connection>();
const opening = new Map<string, Promise<EditorDocumentState | null>>();
const suppliedDocuments = new Map<string, string>();
const initializing = new Map<string, Promise<void>>();
const operationTails = new Map<string, Promise<void>>();
const errors = new Map<string, string>();
const acquisitions = new Map<string, number>();
const evictions = new Map<string, Promise<boolean>>();

function connectionFor(projectId: string): Connection | undefined {
  const stack = connections.get(projectId);
  return stack?.[stack.length - 1] ?? retiring.get(projectId);
}

export function configureEditorDocuments(projectId: string, client: () => LycaonClient | null,
  sessionId: () => string | undefined, drafts: EditorDraftSource, workspaceId?: () => string): () => void {
  const connection = { client, sessionId, drafts, workspaceId };
  const stack = connections.get(projectId) ?? [];
  stack.push(connection);
  connections.set(projectId, stack);
  return () => {
    const index = stack.indexOf(connection);
    if (index >= 0) stack.splice(index, 1);
    if (stack.length) return;
    connections.delete(projectId);
    retiring.set(projectId, connection);
    flushFilesDraftSync();
    const ids = [...replicas.values()].filter((r) => r.accepted.project_id === projectId).map((r) => r.accepted.id);
    void (async () => {
      for (const id of ids) {
        const replica = replicas.get(id);
        if (replica) await suspendFileDocument(projectId, replica.accepted.root_id, replica.accepted.path, id).catch(() => false);
      }
    })().then(() => {
      if (![...replicas.values()].some((r) => r.accepted.project_id === projectId)) retiring.delete(projectId);
    });
  };
}

function fromWire(wire: EditorDocument, replica?: DocumentReplica): EditorDocumentState {
  return {
    projectId: wire.project_id, workspaceId: wire.workspace_id, documentId: wire.id, fileId: wire.file_id,
    rootId: wire.root_id, path: wire.path, localGeneration: replica?.localGeneration ?? 0, dirty: replica?.dirty ?? wire.dirty,
    baseSha256: wire.base_sha256,
    encoding: wire.encoding, sizeBytes: wire.size_bytes, eol: replica?.format.eol ?? wire.eol, baseEol: wire.base_eol,
    mixedEol: replica?.format.mixedEol ?? wire.mixed_eol, baseMixedEol: wire.base_mixed_eol, revision: wire.revision,
    diverged: wire.diverged, absent: wire.absent,
    heldAgentVersionId: wire.held_agent_version_id ?? null, secretScreen: wire.secret_screen,
    secretScreenStatus: wire.secret_screen_status, synchronization: replica?.status ?? "accepted",
    pending: replica?.pendingCount ?? 0, epoch: wire.epoch, participants: wire.participants.length,
    participantIds: wire.participants.map((participant) => participant.client_id), resolving: !!replica && (replica.resolving || replica.awaitingCommand),
    editingBlock: replica?.preservationBlocked ? "storage" : replica?.capacityBlocked ? "capacity" : null,
    refusal: replica?.refusal ?? null,
  };
}

function note(document: EditorDocumentState): EditorDocumentState {
  const previous = documents[document.documentId];
  if (previous && previous.revision > document.revision) return previous;
  setDocuments(document.documentId, reconcile(document, { key: null }));
  for (const listener of listeners) listener(document);
  return documents[document.documentId]!;
}

function replicaChanged(replica: DocumentReplica): void {
  if (replicas.get(replica.accepted.id) !== replica) return;
  const buffers = connectionFor(replica.accepted.project_id)?.drafts.all() ?? [];
  for (const buffer of buffers) {
    if (buffer.documentId !== replica.accepted.id) continue;
    const view = getFilesEditorView(replica.accepted.project_id, buffer.key);
    if (view) refreshFilesCollaborationEpoch(view, replica);
  }
  if (replica.error) errors.set(replica.accepted.id, replica.error);
  else errors.delete(replica.accepted.id);
  documentResidency.resize(documentResourceKey(replica.accepted.project_id, { documentId: replica.accepted.id }), replica.estimatedBytes, !replica.preservationBlocked);
  note(fromWire(replica.accepted, replica));
}

export function editorReplica(documentId: string | null | undefined): DocumentReplica | undefined {
  // Reading the reactive record tracks replica replacement and release.
  if (documentId) void documents[documentId];
  const replica = documentId ? replicas.get(documentId) : undefined;
  return replica?.initialized ? replica : undefined;
}

export function editorDraftPersistenceError(projectId: string): string | null {
  for (const [id, error] of errors) if (documents[id]?.projectId === projectId) return error;
  return null;
}

export function editorDocumentScreenStatus(documentId: string | null | undefined): SecretScreenStatus | null {
  return documentId ? documents[documentId]?.secretScreenStatus ?? null : null;
}

export async function runEditorDocumentOperation<T>(documentId: string, run: () => Promise<T>): Promise<T> {
  const previous = operationTails.get(documentId) ?? Promise.resolve();
  const result = previous.catch(() => undefined).then(run);
  const tail = result.then(() => undefined);
  void tail.catch(() => undefined);
  operationTails.set(documentId, tail);
  try { return await result; } finally { if (operationTails.get(documentId) === tail) operationTails.delete(documentId); }
}

export function receiveEditorDocument(document: EditorDocument): void {
  replicas.get(document.id)?.receive(document);
}

export function receiveEditorDocumentEvent(event: EditorDocumentEvent): void {
  wakeDocumentOutbox(event.id, event.revision);
  const replica = replicas.get(event.id);
  if (!replica) { applyFilesBufferDocumentStatus(event.project_id, event); return; }
  void replica.receiveEvent(event).catch((error: unknown) => {
    errors.set(event.id, error instanceof Error ? error.message : String(error));
  });
}

export async function resolveEditorDocument(projectId: string, buffer: EditorDraft, incarnation?: string, held?: DocumentReservation, priority: ResidencyPriority = "foreground", current = () => true): Promise<EditorDocumentState | null> {
  if (buffer.kind !== "text" || buffer.jobId || buffer.deleted) return null;
  if (buffer.documentId) {
    acquisitions.set(buffer.documentId, (acquisitions.get(buffer.documentId) ?? 0) + 1);
    const closing = evictions.get(buffer.documentId);
    if (closing) await closing;
  }
  const connection = connectionFor(projectId);
  const workspaceId = connection?.workspaceId?.() ?? "";
  const key = [projectId, workspaceId, buffer.rootId, buffer.path, buffer.fileId, buffer.encoding].join("\0");
  const suppliedId = suppliedDocuments.get(key);
  const existing = editorReplica(buffer.documentId) ?? (suppliedId ? replicas.get(suppliedId) : undefined);
  if (existing) {
    acquisitions.set(existing.accepted.id, (acquisitions.get(existing.accepted.id) ?? 0) + 1);
    await evictions.get(existing.accepted.id);
    await initializing.get(existing.accepted.id);
    if (evictions.has(existing.accepted.id)) await existing.synchronize();
    return documents[existing.accepted.id] ?? note(fromWire(existing.accepted, existing));
  }
  const pending = opening.get(key);
  if (pending) return pending;
  const task = (async () => {
    const reservation = held ?? await documentResidency.acquire(documentResourceKey(projectId, buffer), documentOpeningReservation(), priority, current);
    if (!reservation) return null;
    try { return await openReplica(projectId, buffer, connection, workspaceId, incarnation, reservation); }
    finally { if (!held) reservation.release(); }
  })();
  opening.set(key, task);
  try { return await task; } finally { if (opening.get(key) === task) opening.delete(key); }
}

async function openReplica(projectId: string, buffer: EditorDraft, connection: Connection | undefined, workspaceId: string, incarnation?: string, reservation?: DocumentReservation): Promise<EditorDocumentState | null> {
  const client = connection?.client();
  if (!client) throw new BackendTransportError(new Error("Waiting for the connection to open this file for editing."), "unreachable");
  const sessionId = connection?.sessionId();
  await prepareClientIdentity();
  const decodeAs = buffer.encoding === "utf-16le" || buffer.encoding === "utf-16be" ? buffer.encoding : undefined;
  const document = buffer.documentId ? await client.createEditorDocumentSnapshot(projectId, buffer.documentId, { client_id: clientIdentity() }) : await client.openEditorDocument(projectId, {
    path: buffer.path, root_id: buffer.rootId, client_id: clientIdentity(), ...(decodeAs ? { decode_as: decodeAs } : {}),
  }, sessionId);
  if (workspaceId && document.workspace_id !== workspaceId) {
    if (!document.workspace_id) throw new Error("The backend did not identify this file's workspace. Update and restart the backend, then retry editing.");
    throw new SourceWorkspaceMismatchError(workspaceId, document.workspace_id);
  }
  await reservation?.expand(documentAdmissionBytes(document.size_bytes, document.crdt_update.length));
  return adoptOpenedReplica(projectId, client, document, incarnation, undefined, reservation);
}

async function adoptOpenedReplica(projectId: string, client: LycaonClient, opened: EditorDocument, incarnation?: string, confirmed?: Y.Doc, reservation?: DocumentReservation, offline = false): Promise<EditorDocumentState> {
  const outbox = documentOutbox();
  const records = reservation ? await readReservedRecovery(outbox, opened.id, reservation) : await outbox.read(opened.id);
  const recovery = offline ? { document: opened, records } : await resumeDocumentCommands(client, opened, outbox, records, reservation ? () => readReservedRecovery(outbox, opened.id, reservation) : undefined);
  const document = recovery.document;
  if (!replicas.has(document.id)) {
    try { await reservation?.expand(documentAdmissionBytes(document.size_bytes, document.crdt_update.length, recoveryBytes(recovery.records))); }
    catch (error) { confirmed?.destroy(); throw error; }
  }
  let replica = replicas.get(document.id);
  if (replica) {
    // A retained replica receives the differential state the open projected against it.
    replica.receive(document);
    confirmed?.destroy();
  } else {
    const adopted = document === opened ? confirmed : undefined;
    if (adopted !== confirmed) confirmed?.destroy();
    replica = new DocumentReplica(document, () => connectionFor(projectId)?.client() ?? null, replicaChanged, undefined, () => ({
      sessionId: connectionFor(projectId)?.sessionId() ?? "",
    }), incarnation, adopted);
    replicas.set(document.id, replica);
    const created = replica;
    const initialization = created.initialize(recovery.records, offline).catch(async (error: unknown) => {
      if (replicas.get(document.id) === created) replicas.delete(document.id);
      await created.close();
      throw error;
    }).finally(() => { if (initializing.get(document.id) === initialization) initializing.delete(document.id); });
    initializing.set(document.id, initialization);
  }
  await initializing.get(document.id);
  reservation?.identify(documentResourceKey(projectId, { documentId: replica.accepted.id }));
  const resident = replica;
  reservation?.retain(resident.estimatedBytes, () => suspendFileDocument(projectId, resident.accepted.root_id, resident.accepted.path, resident.accepted.id));
  return note(fromWire(replica.accepted, replica));
}

/** A live replica or durable recovery state already held by this window. */
export type RetainedOpening = { replica?: RetainedReplica; checkpoint?: ReplicaCheckpoint };

/** What an open sends so the host projects only the state this window lacks. */
export async function retainedOpening(projectId: string, buffer: { rootId: string; path: string; documentId?: string | null }, reservation?: DocumentReservation): Promise<RetainedOpening | undefined> {
  const live = editorReplica(buffer.documentId ?? undefined);
  if (live) return { replica: live.retainedIdentity() };
  try {
    const outbox = documentOutbox();
    const entry = buffer.documentId ? { documentId: buffer.documentId }
      : (await outbox.list(projectId)).find((item) => item.rootId === buffer.rootId && item.path === buffer.path);
    if (!entry) return undefined;
    const records = await (reservation ? readReservedRecovery(outbox, entry.documentId, reservation) : outbox.read(entry.documentId));
    const windowRecords = records.filter(record => record.clientId === clientIdentity());
    const command = windowRecords.find(record => record.kind === "command" && record.historyBase);
    const checkpoint = windowRecords.find((record): record is ReplicaCheckpoint => record.kind === "checkpoint")
      ?? (command?.kind === "command" ? command.historyBase : undefined);
    const replica = checkpoint && retainedCheckpointIdentity(checkpoint);
    return checkpoint ? { replica, checkpoint } : undefined;
  } catch (error) {
    if (reservation) throw error;
    return undefined;
  }
}

/** Completes the opening frame from retained state before its first paint. */
export function presentOpenedDocument(document: EditorDocument | undefined, retained?: RetainedOpening): { confirmed?: Y.Doc; loaded: LoadedDocumentText } | undefined {
  if (!document) return undefined;
  const loaded = (text: string): LoadedDocumentText => ({ text, eol: document.eol, mixed: document.mixed_eol, dirty: document.dirty, absent: document.absent });
  const live = editorReplica(document.id);
  if (live) return { loaded: loaded(live.text.toString()) };
  const { confirmed, text } = decodeDocumentState(document, retained?.checkpoint);
  return { confirmed, loaded: loaded(text) };
}

/** Adopts the source response and reuses its decoded state from the first paint. */
export function receiveOpenedEditorDocument(projectId: string, client: LycaonClient, document: EditorDocument, confirmed?: Y.Doc, reservation?: DocumentReservation, offline = false): Promise<EditorDocumentState | null> {
  const key = [projectId, document.workspace_id, document.root_id, document.path, document.file_id, document.encoding].join("\0");
  suppliedDocuments.set(key, document.id);
  const existing = opening.get(key);
  if (existing) { confirmed?.destroy(); return existing; }
  const task = adoptOpenedReplica(projectId, client, document, crypto.randomUUID(), confirmed, reservation, offline);
  opening.set(key, task);
  void task.catch(() => {
    if (suppliedDocuments.get(key) === document.id) suppliedDocuments.delete(key);
  }).finally(() => {
    if (opening.get(key) === task) opening.delete(key);
    void releaseUnclaimedEditorDocument(projectId, document).catch(() => undefined);
  });
  return task;
}

export async function releaseUnclaimedEditorDocument(projectId: string, document: EditorDocument): Promise<void> {
  const wanted = (connections.get(projectId) ?? []).some(connection =>
    connection.workspaceId?.() === document.workspace_id && connection.drafts.all().some(buffer =>
      !buffer.jobId && !buffer.deleted && buffer.rootId === document.root_id && buffer.path === document.path &&
      (!buffer.fileId || buffer.fileId === document.file_id)));
  if (wanted) return;
  for (const [key, id] of suppliedDocuments) if (id === document.id) suppliedDocuments.delete(key);
  if (replicas.has(document.id)) await evictEditorDocument(document.id);
  // The open marked this document retained on the host; no tab claims it, so the published set drops it.
  refreshFileDocumentRetention(projectId);
}

export async function observeEditorDocument(projectId: string, buffer: EditorDraft): Promise<EditorDocumentState | null> {
  const client = connectionFor(projectId)?.client();
  const replica = editorReplica(buffer.documentId);
  if (!client || !replica) return null;
  // Replica updates include the observed disk branch.
  await client.observeEditorDocument(projectId, replica.accepted.id, { client_id: clientIdentity() });
  await replica.synchronize();
  return documents[replica.accepted.id] ?? null;
}

export async function flushEditorDocumentDraft(documentId: string | null | undefined): Promise<void> {
  if (!documentId) return;
  const pendingOperation = operationTails.get(documentId);
  const replica = replicas.get(documentId);
  if (replica) await replica.flush();
  await pendingOperation;
}

export type SyncedEditorDocument = { revision: number; text: string };
export async function syncEditorDocumentDraft(projectId: string, buffer: EditorDraft): Promise<SyncedEditorDocument | null> {
  const replica = editorReplica(buffer.documentId);
  if (!replica) return null;
  await publishEditorDocumentDraft(projectId, buffer);
  return { revision: replica.accepted.revision, text: replica.acceptedText };
}

export function scheduleEditorDocumentDraft(projectId: string, buffer: EditorDraft): void {
  const replica = editorReplica(buffer.documentId);
  if (!replica || replica.accepted.project_id !== projectId) return;
  if (!replica.setFormat({ eol: buffer.eol, mixedEol: buffer.mixedEol })) return;
  void replica.preserve().catch((error: unknown) => {
    errors.set(replica.accepted.id, error instanceof Error ? error.message : String(error));
  });
}

export async function publishEditorDocumentDraft(projectId: string, buffer: EditorDraft): Promise<boolean> {
  const publish = async (replica: DocumentReplica) => {
    if (replica.accepted.project_id !== projectId) return false;
    replica.setFormat({ eol: buffer.eol, mixedEol: buffer.mixedEol });
    await replica.flush();
    return true;
  };
  const replica = editorReplica(buffer.documentId);
  return replica ? publish(replica) : (await withEditorDocumentReplica(projectId, buffer, publish)) ?? false;
}

export async function preserveEditorDocumentDraft(buffer: FileBuffer): Promise<boolean> {
  const replica = editorReplica(buffer.documentId);
  if (!replica) {
    if (!buffer.documentId) return false;
    // Cold payloads can only arise from restart or a completed preservation.
    return buffer.content.state === "unloaded" ? buffer.editRevision === 0
      : buffer.content.state === "suspended" && buffer.content.generation === buffer.editRevision;
  }
  replica.setFormat({ eol: buffer.eol, mixedEol: buffer.mixedEol });
  await replica.preserve({ checkpoint: true });
  return true;
}

export async function preserveEditorDocumentOperations(): Promise<void> {
  flushFilesDraftSync();
  for (const replica of replicas.values()) await replica.preserve({ checkpoint: true });
}

/** Commands admit one replica without mounting a presentation. */
export async function withEditorDocumentReplica<T>(projectId: string, buffer: EditorDraft, run: (replica: DocumentReplica) => Promise<T>): Promise<T | null> {
  const cold = !editorReplica(buffer.documentId);
  const reservation = await documentResidency.acquire(documentResourceKey(projectId, buffer), documentOpeningReservation(), "foreground");
  if (!reservation) return null;
  let id = buffer.documentId;
  try {
    if (cold) id = (await resolveEditorDocument(projectId, buffer, undefined, reservation))?.documentId ?? id;
    const replica = editorReplica(id);
    return replica ? await run(replica) : null;
  } finally {
    try { if (cold) await suspendFileDocument(projectId, buffer.rootId, buffer.path, id ?? undefined, buffer.jobId); }
    finally { reservation.release(); }
  }
}

export async function discardEditorDocument(projectId: string, buffer: EditorDraft): Promise<EditorDocumentState | null> {
  return withEditorDocumentReplica(projectId, buffer, async replica => {
    await replica.flush();
    const document = await runEditorDocumentOperation(replica.accepted.id, () => replica.executeCommand(documentCommand(replica.accepted, { action: "discard", request: {
      ...replica.authorship, client_id: clientIdentity(), operation_id: crypto.randomUUID(), expected_revision: replica.accepted.revision,
    } })));
    replica.receive(document);
    return documents[document.id] ?? null;
  });
}

/** Failed unpinning is retried by a later commit or sweep without blocking tab closure. */
async function releaseRecoveryRetention(documentId: string, replica?: DocumentReplica): Promise<void> {
  try {
    if (replica) await replica.releaseRetention();
    else await documentOutbox().commit([], [], { document: { documentId }, clientId: clientIdentity(), retained: false });
  } catch (error) {
    errors.set(documentId, error instanceof Error ? error.message : String(error));
  }
}

export async function evictEditorDocument(documentId: string | null | undefined, options: { retainRecovery?: boolean; ifIdle?: boolean } = {}): Promise<boolean> {
  const replica = editorReplica(documentId);
  if (!replica) {
    if (documentId && !options.retainRecovery) await releaseRecoveryRetention(documentId);
    return true;
  }
  const existing = evictions.get(replica.accepted.id);
  if (existing) return existing;
  const acquired = acquisitions.get(replica.accepted.id) ?? 0;
  const closing = (async () => {
    try {
      await replica.preserve({ checkpoint: true });
      if ((acquisitions.get(replica.accepted.id) ?? 0) !== acquired) return false;
      if (options.ifIdle && documentResidency.isProtected(documentResourceKey(replica.accepted.project_id, { documentId: replica.accepted.id }))) return false;
      if (!(await replica.suspend(() =>
        (acquisitions.get(replica.accepted.id) ?? 0) === acquired &&
        (!options.ifIdle || !documentResidency.isProtected(documentResourceKey(replica.accepted.project_id, { documentId: replica.accepted.id })))))) return false;
      batch(() => {
        const projectId = replica.accepted.project_id;
        for (const buffer of Object.values(projectFilesState(projectId).byKey)) {
          if (buffer.documentId === replica.accepted.id) suspendFilesBufferContent(projectId, buffer.key);
        }
        replicas.delete(replica.accepted.id);
        for (const [key, id] of suppliedDocuments) if (id === replica.accepted.id) suppliedDocuments.delete(key);
        setDocuments(replica.accepted.id, undefined);
      });
      documentResidency.forget(documentResourceKey(replica.accepted.project_id, { documentId: replica.accepted.id }));
      if (!options.retainRecovery) await releaseRecoveryRetention(replica.accepted.id, replica);
      return true;
    } catch (error) {
      errors.set(replica.accepted.id, error instanceof Error ? error.message : String(error));
      throw error;
    }
  })();
  evictions.set(replica.accepted.id, closing);
  try { return await closing; } finally { if (evictions.get(replica.accepted.id) === closing) evictions.delete(replica.accepted.id); }
}

/** Tab selection retains a replica admitted by the opening controller. */
export function retainEditorDocument(buffer: EditorDraft): void {
  if (buffer.documentId && editorReplica(buffer.documentId)) {
    acquisitions.set(buffer.documentId, (acquisitions.get(buffer.documentId) ?? 0) + 1);
  }
}

export function forgetEditorDocument(documentId: string | null | undefined): void {
  const replica = editorReplica(documentId);
  if (replica) void replica.close();
  if (documentId) {
    for (const [key, id] of suppliedDocuments) if (id === documentId) suppliedDocuments.delete(key);
    replicas.delete(documentId); setDocuments(documentId, undefined);
  }
}

export async function flushEditorDocumentOperations(): Promise<void> {
  flushFilesDraftSync();
  const results = await Promise.allSettled([...replicas.keys()].map(flushEditorDocumentDraft));
  const failure = results.find((result) => result.status === "rejected");
  if (failure?.status === "rejected") throw failure.reason;
}

export function editorDocumentRevisionFor(projectId: string, rootId: string, path: string): number | null {
  return Object.values(documents).find((d) => d?.projectId === projectId && d.rootId === rootId && d.path === path && d.pending === 0)?.revision ?? null;
}

/** Live participant client identities for a file's document; reactive. */
export function editorDocumentParticipantsFor(projectId: string, rootId: string, path: string): readonly string[] {
  return Object.values(documents).find((d) => d?.projectId === projectId && d.rootId === rootId && d.path === path)?.participantIds ?? [];
}

export function subscribeEditorDocuments(listener: EditorDocumentListener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function resetEditorDocumentsForTests(): void {
  for (const replica of replicas.values()) void replica.close();
  suppliedDocuments.clear();
  replicas.clear(); listeners.clear(); connections.clear(); retiring.clear(); opening.clear(); initializing.clear(); operationTails.clear(); errors.clear(); acquisitions.clear(); evictions.clear();
  for (const id of Object.keys(documents)) setDocuments(id, undefined);
  documentResidency.reset();
}

export async function resolveEditorConflict(projectId: string, buffer: EditorDraft, content: string, diskSha256: string, reviewedRevision: number): Promise<EditorDocument> {
  const previous = editorReplica(buffer.documentId);
  const client = connectionFor(projectId)?.client();
  if (!previous || !client) throw new Error("The collaborative editor is not connected.");
  previous.resolving = true;
  replicaChanged(previous);
  const operationId = crypto.randomUUID();
  try {
    await previous.flush();
    await previous.synchronize();
    const document = await previous.executeCommand(documentCommand(previous.accepted, { action: "resolve", request: {
      ...previous.authorship,
      client_id: clientIdentity(), operation_id: operationId, expected_revision: reviewedRevision,
      disk_sha256: diskSha256, content, eol: buffer.eol,
    } }, connectionFor(projectId)?.sessionId()));
    previous.receive(document);
    return document;
  } finally {
    previous.resolving = false;
    replicaChanged(previous);
  }
}
