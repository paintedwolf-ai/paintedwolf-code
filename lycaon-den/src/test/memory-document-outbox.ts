import type { DocumentOutbox, OutboxInventoryEntry, OutboxRecord } from "../files/documents/document-outbox.ts";

/** Durable transactions and body-free inventory for collaboration tests. */
export function memoryDocumentOutbox(
  records = new Map<string, OutboxRecord>(),
  retained = new Map<string, Set<string>>(),
): DocumentOutbox {
  const key = (record: OutboxRecord) => `${record.documentId}:${record.clientId}:${record.kind === "checkpoint" ? "checkpoint" : `${record.kind}:${record.operationId}`}`;
  const bytes = (entries: OutboxRecord[]) => JSON.stringify(entries).length * 2;
  const outbox: DocumentOutbox = {
    list: async projectId => {
      const inventory = new Map<string, OutboxInventoryEntry>();
      for (const record of records.values()) {
        if (projectId !== undefined && record.projectId !== projectId) continue;
        const entries = [...records.values()].filter(item => item.documentId === record.documentId);
        const pending = entries.some(item => item.kind === "update" ? !item.acknowledged : item.kind === "command" ? !item.result : item.kind === "format");
        const checkpoints = entries.filter(item => item.kind === "checkpoint");
        inventory.set(record.documentId, {
          documentId: record.documentId, projectId: record.projectId, rootId: record.rootId, path: record.path, fileId: record.fileId,
          bytes: bytes(entries), synchronized: !pending && checkpoints.every(item => item.synchronized === true),
          retainedClients: [...retained.get(record.documentId) ?? []],
        });
      }
      return [...inventory.values()];
    },
    inspect: async id => (await outbox.list()).find(entry => entry.documentId === id),
    read: async (id, maxBytes) => {
      const entries = [...records.values()].filter(record => record.documentId === id);
      if (maxBytes !== undefined && bytes(entries) > maxBytes) throw new Error("Recovery exceeds its reservation.");
      return structuredClone(entries);
    },
    commit: async (entries, remove = [], retention) => {
      const next = new Map(records);
      for (const record of entries) next.set(key(record), structuredClone(record));
      for (const record of remove) next.delete(key(record));
      const acknowledged = new Set(entries.flatMap(record => record.kind === "update" && record.acknowledged ? [record.operationId] : []));
      const formatFinished = remove.some(record => record.kind === "format");
      for (const [id, record] of next) {
        if (record.kind !== "checkpoint" || !record.pendingOperations || (!acknowledged.size && !formatFinished)) continue;
        const pendingOperations = record.pendingOperations.filter(id => !acknowledged.has(id));
        const hasFormat = [...next.values()].some(item => item.documentId === record.documentId && item.kind === "format");
        next.set(id, { ...record, pendingOperations, synchronized: record.synchronized || (!pendingOperations.length && !hasFormat && (record.pendingOperations.length > 0 || formatFinished)) });
      }
      if (retention) {
        const id = retention.document.documentId;
        const clients = new Set(retained.get(id));
        if (retention.retained) {
          if (![...next.values()].some(record => record.kind === "checkpoint" && record.documentId === id && record.clientId === retention.clientId)) throw new Error("Missing covering checkpoint.");
          clients.add(retention.clientId);
        } else clients.delete(retention.clientId);
        retained.set(id, clients);
      }
      records.clear();
      for (const [id, record] of next) records.set(id, record);
    },
    put: record => outbox.commit([record]),
    remove: record => outbox.commit([], [record]),
  };
  return outbox;
}
