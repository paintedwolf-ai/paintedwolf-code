import type { EditorDocument } from "../../api/types.ts";
import { documentResidency, documentResourceKey, documentOpeningReservation } from "./document-residency.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import { clientIdentity, prepareClientIdentity } from "../../platform/connection/client-identity.ts";
import { deliverDocumentCommand, resumeDocumentCommands } from "./document-command.ts";
import { documentOutbox, recoveryBytes, readReservedRecovery, type DocumentOutbox } from "./document-outbox.ts";
import { DocumentReplica } from "./document-replica.ts";

type DeliveryAttempt = {
  incarnation: string;
  signature: string;
  client: LycaonClient;
  failures: number;
  retryAt: number;
  revision?: number;
};
const attempts = new WeakMap<DocumentOutbox, Map<string, DeliveryAttempt>>();

function deliveryAttempts(outbox: DocumentOutbox): Map<string, DeliveryAttempt> {
  let state = attempts.get(outbox);
  if (!state) { state = new Map(); attempts.set(outbox, state); }
  return state;
}

export function wakeDocumentOutbox(documentId: string, revision: number, outbox = documentOutbox()): void {
  const attempt = deliveryAttempts(outbox).get(documentId);
  if (attempt && (attempt.revision === undefined || revision > attempt.revision)) {
    attempt.revision = revision;
    attempt.retryAt = 0;
  }
}

/** Pending work follows document identity even when its tab closed or path moved. */
export async function relayDocumentOutbox(projectId: string | undefined, client: LycaonClient,
  resident: (id: string) => boolean, outbox: DocumentOutbox = documentOutbox(), stopped = () => false, synchronized?: (document: EditorDocument) => void): Promise<void> {
  await prepareClientIdentity();
  const state = deliveryAttempts(outbox);
  for (const item of await outbox.list(projectId)) {
    const projectId = item.projectId;
    if (stopped()) return;
    if (item.synchronized === true) { state.delete(item.documentId); continue; }
    const previous = state.get(item.documentId);
    if (previous?.client === client && previous.retryAt > Date.now()) continue;
    const isResident = resident(item.documentId);
    const reservation = await documentResidency.acquire(documentResourceKey(projectId, { documentId: item.documentId }), documentOpeningReservation(), "background", () => !stopped() && (isResident || !resident(item.documentId)));
    if (!reservation) continue;
    let replica: DocumentReplica | undefined;
    let attempt = previous;
    if (!attempt || attempt.client !== client) {
      attempt = { incarnation: crypto.randomUUID(), signature: "", client, failures: 0, retryAt: 0 };
      state.set(item.documentId, attempt);
    }
    try {
      const records = await readReservedRecovery(outbox, item.documentId, reservation);
      await reservation.expand(1024 * 1024 + recoveryBytes(records) * 2);
      const pending = records.filter((record) => record.kind === "format" || (record.kind === "update" && !record.acknowledged) || (record.kind === "command" && !record.result));
      if (pending.length === 0) { state.delete(item.documentId); continue; }
      if (isResident && !pending.some(record => record.kind === "command" && !record.historyBase)) continue;
      const signature = pending.map((record) => "operationId" in record ? record.operationId : "").sort().join(":");
      if (attempt.signature !== signature || attempt.client !== client) {
        Object.assign(attempt, { signature, client, failures: 0, retryAt: 0 });
      }
      if (attempt.retryAt > Date.now()) continue;
      let document = await client.createEditorDocumentSnapshot(projectId, item.documentId, { client_id: clientIdentity() });
      attempt.revision = Math.max(attempt.revision ?? 0, document.revision);
      document = (await resumeDocumentCommands(client, document, outbox, records, () => readReservedRecovery(outbox, item.documentId, reservation))).document;
      if (isResident) { state.delete(item.documentId); continue; }
      for (const command of pending) {
        if (command.kind !== "command" || !command.historyBase || command.clientId === clientIdentity()) continue;
        const accepted = await deliverDocumentCommand(client, command, outbox);
        if (accepted.revision >= document.revision) document = accepted;
      }
      if (stopped() || resident(item.documentId)) continue;
      replica = new DocumentReplica(document, () => client, () => undefined, outbox, undefined, attempt.incarnation, undefined, false);
      await replica.initialize(await readReservedRecovery(outbox, item.documentId, reservation));
      attempt.revision = Math.max(attempt.revision ?? 0, replica.accepted.revision);
      await replica.flush();
      if (!replica.pendingCount) synchronized?.(replica.accepted);
      attempt.failures = 0;
      attempt.retryAt = 0;
    } catch (error) {
      if (error instanceof LycaonApiError && error.code === "editor_document_not_found") {
        // The host has no such document; its records leave the outbox.
        await outbox.commit([], await readReservedRecovery(outbox, item.documentId, reservation)).catch(() => undefined);
        state.delete(item.documentId);
        continue;
      }
      attempt.failures++;
      attempt.retryAt = Date.now() + Math.min(60_000, 5_000 * 2 ** Math.min(attempt.failures - 1, 4));
    } finally {
      try { await replica?.close(); } finally { reservation.release(); }
    }
  }
}

export function startDocumentOutboxRelay(client: () => LycaonClient | null,
  resident: (id: string) => boolean, synchronized?: (document: EditorDocument) => void): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const run = async () => {
    try {
      const connected = client();
      if (connected) await relayDocumentOutbox(undefined, connected, resident, undefined, () => stopped, synchronized);
    } finally {
      if (!stopped) timer = setTimeout(() => { void run().catch(() => undefined); }, 5_000);
    }
  };
  void run().catch(() => undefined);
  return () => { stopped = true; clearTimeout(timer); };
}
