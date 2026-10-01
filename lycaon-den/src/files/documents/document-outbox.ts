import type { DocumentReservation } from "./document-residency.ts";
import { invoke } from "@tauri-apps/api/core";
import type { EditorDocument } from "../../api/types.ts";
import type { PendingDocumentCommand } from "./document-command.ts";
import type { PendingDocumentFormat } from "./document-format.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";

export type OutboxDocument = {
  documentId: string;
  projectId: string;
  rootId: string;
  fileId: string;
  path: string;
};

/** Only an explicit synchronized inventory entry can skip recovery reads. */
export type OutboxInventoryEntry = OutboxDocument & { synchronized?: boolean; retainedClients?: string[]; bytes?: number };

export function outboxDocument(document: EditorDocument): OutboxDocument {
  return { documentId: document.id, projectId: document.project_id, rootId: document.root_id, fileId: document.file_id, path: document.path };
}

export type ReplicaCheckpoint = OutboxDocument & {
  kind: "checkpoint";
  documentId: string;
  projectId: string;
  clientId: string;
  epoch: number;
  state: string;
  replicaId: number;
  incarnation: string;
  confirmed: EditorDocument;
  /** True only when every local text/format operation is acknowledged by the host. */
  synchronized?: boolean;
  pendingOperations?: string[];
  history?: unknown;
  lastHistoryCommand?: string;
};

export type PendingDocumentUpdate = OutboxDocument & {
  kind: "update";
  documentId: string;
  projectId: string;
  clientId: string;
  replicaId: number;
  epoch: number;
  operationId: string;
  update: string;
  acknowledged: boolean;
  attempted?: boolean;
  sequence: number;
  /** The chat focused when the edit was typed. */
  sessionId: string;
};

export type OutboxRecord = ReplicaCheckpoint | PendingDocumentUpdate | PendingDocumentCommand | PendingDocumentFormat;

export type DocumentRetention = { document: Pick<OutboxDocument, "documentId">; clientId: string; retained: boolean };

export interface DocumentOutbox {
  list(projectId?: string): Promise<OutboxInventoryEntry[]>;
  /** One document's inventory entry, without scanning the others. */
  inspect(documentId: string): Promise<OutboxInventoryEntry | undefined>;
  read(documentId: string, maxBytes?: number): Promise<OutboxRecord[]>;
  commit(records: OutboxRecord[], remove?: OutboxRecord[], retention?: DocumentRetention): Promise<void>;
  put(record: OutboxRecord): Promise<void>;
  remove(record: OutboxRecord): Promise<void>;
}

const PREFIX = "documentOutbox:";

function recordKey(record: OutboxRecord): string {
  return `${PREFIX}${record.documentId}:${record.clientId}:${record.kind === "checkpoint" ? "checkpoint" : `${record.kind}:${record.operationId}`}`;
}

let database: Promise<IDBDatabase> | undefined;

function openDatabase(): Promise<IDBDatabase> {
  database ??= new Promise((resolve, reject) => {
    const request = indexedDB.open("painted-wolf-documents", 1);
    request.onupgradeneeded = () => {
      const store = request.result.createObjectStore("outbox");
      store.createIndex("document", "documentId");
      request.result.createObjectStore("documents", { keyPath: "documentId" }).createIndex("project", "projectId");
    };
    request.onsuccess = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains("outbox") || !db.objectStoreNames.contains("documents")) {
        db.close(); database = undefined;
        reject(new DocumentRecoveryFormatError("This development editor store has an unknown format. Its preserved bytes have been left untouched."));
        return;
      }
      db.onversionchange = () => { db.close(); database = undefined; };
      resolve(db);
    };
    request.onblocked = () => { database = undefined; reject(new Error("Another window is holding editor storage open.")); };
    request.onerror = () => { database = undefined; reject(request.error ?? new Error("Editor storage could not be opened.")); };
  });
  return database;
}

async function transaction<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore, tx: IDBTransaction) => IDBRequest<T>): Promise<T> {
  const db = await openDatabase();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(["outbox", "documents"], mode, { durability: "strict" });
    let request: IDBRequest<T>;
    try { request = run(tx.objectStore("outbox"), tx); }
    catch (error) { tx.abort(); reject(error instanceof Error ? error : new Error(String(error))); return; }
    tx.oncomplete = () => resolve(request.result);
    tx.onabort = () => reject(tx.error ?? new Error("The editor draft could not be preserved."));
    tx.onerror = () => reject(tx.error ?? new Error("The editor draft could not be preserved."));
  });
}

const browserOutbox: DocumentOutbox = {
  list: (projectId) => transaction("readonly", (_store, tx) => projectId === undefined ? tx.objectStore("documents").getAll() : tx.objectStore("documents").index("project").getAll(projectId)) as Promise<OutboxInventoryEntry[]>,
  inspect: (documentId) => transaction("readonly", (_store, tx) => tx.objectStore("documents").get(documentId)) as Promise<OutboxInventoryEntry | undefined>,
  read: readBrowserRecords,
  commit: async (records, remove = [], retention) => { await transaction("readwrite", (store, tx) => {
    for (const record of records) store.put(record, recordKey(record));
    for (const record of remove) store.delete(recordKey(record));
    const acknowledged = new Set(records.filter((r): r is PendingDocumentUpdate => r.kind === "update" && r.acknowledged).map(r => r.operationId));
    const formatFinished = remove.some(r => r.kind === "format");
    const id = records[0]?.documentId ?? remove[0]?.documentId ?? retention?.document.documentId;
    if (id) {
      const current = store.index("document").getAll(id) as IDBRequest<OutboxRecord[]>;
      current.onsuccess = () => {
        const hasFormat = current.result.some(r => r.kind === "format");
        for (const record of current.result) {
          if ((!acknowledged.size && !formatFinished) || record.kind !== "checkpoint" || !record.pendingOperations) continue;
          const pendingOperations = record.pendingOperations.filter(id => !acknowledged.has(id));
          const synchronized = record.synchronized || (pendingOperations.length === 0 && !hasFormat && (record.pendingOperations.length > 0 || formatFinished));
          Object.assign(record, { pendingOperations, synchronized });
          store.put(record, recordKey(record));
        }
        const entry = records[0] ?? remove[0];
        const inventory = tx.objectStore("documents");
        const previous = inventory.get(id) as IDBRequest<OutboxInventoryEntry | undefined>;
        previous.onsuccess = () => {
          const retainedClients = new Set(previous.result?.retainedClients ?? []);
          if (retention) {
            if (retention.retained) {
              if (!current.result.some(record => record.kind === "checkpoint" && record.clientId === retention.clientId)) { tx.abort(); return; }
              retainedClients.add(retention.clientId);
            } else retainedClients.delete(retention.clientId);
          }
          if (!current.result.length) { inventory.delete(id); return; }
          const address = entry ?? previous.result;
          if (address) inventory.put({
            documentId: address.documentId, projectId: address.projectId, rootId: address.rootId,
            fileId: address.fileId, path: address.path, retainedClients: [...retainedClients].filter(client => current.result.some(record => record.kind === "checkpoint" && record.clientId === client)),
            bytes: recoveryBytes(current.result),
            synchronized: current.result.every(record => record.kind === "checkpoint" && record.synchronized === true),
          });
        };
      };
    }
    return store.count();
  }); },
  put: (record) => browserOutbox.commit([record]),
  remove: (record) => browserOutbox.commit([], [record]),
};

const nativeOutbox: DocumentOutbox = {
  async list(projectId) {
    return invoke<OutboxInventoryEntry[]>("list_document_outbox", { projectId });
  },
  async inspect(documentId) {
    return await invoke<OutboxInventoryEntry | null>("inspect_document_outbox", { documentId }) ?? undefined;
  },
  async read(documentId, maxBytes) {
    return invoke<OutboxRecord[]>("read_document_outbox", { documentId, maxBytes });
  },
  async commit(records, remove = [], retention) {
    await invoke("commit_document_outbox", { records, remove, retention: retention ?? null });
  },
  put: (record) => nativeOutbox.commit([record]),
  remove: (record) => nativeOutbox.commit([], [record]),
};

export class DocumentRecoveryFormatError extends Error {}

export class DocumentStorageError extends Error {
  readonly retryable: boolean;
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause), { cause });
    this.name = "DocumentStorageError";
    this.retryable = !(cause instanceof DocumentRecoveryFormatError);
  }
}

function withStorageErrors(storage: DocumentOutbox): DocumentOutbox {
  const run = async <T>(operation: () => Promise<T>): Promise<T> => {
    try { return await operation(); }
    catch (cause) { throw new DocumentStorageError(cause); }
  };
  return {
    list: (project) => run(() => storage.list(project)),
    inspect: (document) => run(() => storage.inspect(document)),
    read: (document, maxBytes) => run(async () => {
      const records = await storage.read(document, maxBytes);
      for (const record of records) {
        if (record.kind === "checkpoint") validateCheckpoint(record);
        if (record.kind === "command" && record.historyBase) validateCheckpoint(record.historyBase);
      }
      return records;
    }),
    commit: (records, remove, retention) => records.length || remove?.length || retention
      ? run(() => storage.commit(records, remove, retention)) : Promise.resolve(),
    put: (record) => run(() => storage.put(record)),
    remove: (record) => run(() => storage.remove(record)),
  };
}

const browserStorage = withStorageErrors(browserOutbox);
const nativeStorage = withStorageErrors(nativeOutbox);

export function documentOutbox(): DocumentOutbox {
  return isTauriRuntime() ? nativeStorage : browserStorage;
}

const BASE64_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const BASE64_CODES = Uint8Array.from(BASE64_ALPHABET, (character) => character.charCodeAt(0));
const BASE64_VALUES = new Uint8Array(128);
BASE64_CODES.forEach((code, value) => { BASE64_VALUES[code] = value; });
const PADDING = 61;
const ascii = new TextDecoder();

type Base64Bytes = Uint8Array & { toBase64?: () => string };
type Base64Constructor = Uint8ArrayConstructor & { fromBase64?: (text: string) => Uint8Array };

/** Portable base64 for engines without `Uint8Array.prototype.toBase64`. */
export function base64FromBytes(bytes: Uint8Array): string {
  const full = bytes.length - (bytes.length % 3);
  const out = new Uint8Array(Math.ceil(bytes.length / 3) * 4);
  let o = 0;
  for (let i = 0; i < full; i += 3) {
    const triple = (bytes[i]! << 16) | (bytes[i + 1]! << 8) | bytes[i + 2]!;
    out[o++] = BASE64_CODES[triple >> 18]!;
    out[o++] = BASE64_CODES[(triple >> 12) & 63]!;
    out[o++] = BASE64_CODES[(triple >> 6) & 63]!;
    out[o++] = BASE64_CODES[triple & 63]!;
  }
  if (full < bytes.length) {
    const a = bytes[full]!;
    const hasB = full + 1 < bytes.length;
    const b = hasB ? bytes[full + 1]! : 0;
    out[o++] = BASE64_CODES[a >> 2]!;
    out[o++] = BASE64_CODES[((a & 3) << 4) | (b >> 4)]!;
    out[o++] = hasB ? BASE64_CODES[(b & 15) << 2]! : PADDING;
    out[o++] = PADDING;
  }
  return ascii.decode(out);
}

/** Portable base64 decoding for engines without `Uint8Array.fromBase64`. */
export function bytesFromBase64(text: string): Uint8Array {
  let end = text.length;
  while (end > 0 && text.charCodeAt(end - 1) === PADDING) end--;
  const bytes = new Uint8Array(Math.floor((end * 3) / 4));
  let out = 0;
  let bits = 0;
  let accumulator = 0;
  for (let i = 0; i < end; i++) {
    accumulator = ((accumulator << 6) | BASE64_VALUES[text.charCodeAt(i)]!) & 0xffffff;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      bytes[out++] = (accumulator >> bits) & 255;
    }
  }
  return bytes;
}

/** Multi-megabyte documents pass through here on open; iterator and callback paths cost seconds. */
export function encodeUpdate(update: Uint8Array): string {
  const native = (update as Base64Bytes).toBase64;
  return typeof native === "function" ? native.call(update) : base64FromBytes(update);
}

export function decodeUpdate(update: string): Uint8Array {
  const native = (Uint8Array as Base64Constructor).fromBase64;
  return typeof native === "function" ? native(update) : bytesFromBase64(update);
}

/** Account for serialized recovery without building another serialized copy. */
export function recoveryBytes(value: unknown): number {
  if (typeof value === "string") return value.length * 2;
  if (value === null || typeof value !== "object") return 8;
  if (Array.isArray(value)) return value.reduce((bytes, item) => bytes + recoveryBytes(item), 24);
  return Object.values(value).reduce<number>((bytes, item) => bytes + recoveryBytes(item), 64);
}

function validateCheckpoint(checkpoint: ReplicaCheckpoint): void {
  if (!Number.isSafeInteger(checkpoint.replicaId) || checkpoint.replicaId <= 0 || checkpoint.replicaId > 0xffffffff
    || typeof checkpoint.incarnation !== "string" || !checkpoint.incarnation
    || !checkpoint.confirmed || checkpoint.confirmed.id !== checkpoint.documentId
    || checkpoint.confirmed.project_id !== checkpoint.projectId || typeof checkpoint.confirmed.crdt_update !== "string"
    || typeof checkpoint.state !== "string" || !Number.isSafeInteger(checkpoint.epoch) || checkpoint.epoch <= 0) {
    throw new DocumentRecoveryFormatError("This editor recovery record has an unknown format. Its preserved bytes have been left untouched.");
  }
}

async function readBrowserRecords(id: string, maxBytes?: number): Promise<OutboxRecord[]> {
  const db = await openDatabase();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(["outbox", "documents"], "readonly");
    let records: OutboxRecord[] = [];
    let failure: Error | undefined;
    const summary: IDBRequest<OutboxInventoryEntry | undefined> = tx.objectStore("documents").get(id);
    summary.onsuccess = () => {
      if (maxBytes !== undefined && (summary.result?.bytes ?? 0) > maxBytes) {
        failure = new Error("The editor recovery record grew beyond its memory reservation. Retry opening it.");
        tx.abort(); return;
      }
      const request: IDBRequest<OutboxRecord[]> = tx.objectStore("outbox").index("document").getAll(id);
      request.onsuccess = () => { records = request.result; };
    };
    tx.oncomplete = () => resolve(records);
    tx.onabort = () => reject(failure ?? tx.error ?? new Error("Editor recovery could not be read."));
    tx.onerror = () => reject(tx.error ?? new Error("Editor recovery could not be read."));
  });
}

export async function readReservedRecovery(outbox: DocumentOutbox, documentId: string, reservation: DocumentReservation): Promise<OutboxRecord[]> {
  const entry = await outbox.inspect(documentId);
  if (entry?.bytes !== undefined) await reservation.expand(1024 * 1024 + entry.bytes * 2);
  return entry?.bytes === undefined ? outbox.read(documentId) : outbox.read(documentId, entry.bytes);
}
