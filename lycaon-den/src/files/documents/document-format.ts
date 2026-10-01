import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { EditorDocument } from "../../api/types.ts";
import { deliverDocumentCommand, documentCommand, type PendingDocumentCommand } from "./document-command.ts";
import { outboxDocument, type OutboxDocument, type DocumentOutbox, type OutboxRecord } from "./document-outbox.ts";

export type DocumentFormat = { eol: "lf" | "crlf"; mixedEol: boolean };
export type PendingDocumentFormat = DocumentFormat & OutboxDocument & {
  kind: "format";
  documentId: string;
  projectId: string;
  clientId: string;
  operationId: string;
  sessionId: string;
  createdAt: string;
  command?: PendingDocumentCommand;
};

/** Format intent is durable independently of the text version delivered with it. */
export class DocumentFormatQueue {
  private readonly records = new Map<string, PendingDocumentFormat>();
  private readonly unpreservedIds = new Set<string>();
  constructor(private readonly outbox: DocumentOutbox) {}

  restore(records: OutboxRecord[]): void {
    for (const record of records.filter((r): r is PendingDocumentFormat => r.kind === "format")
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.operationId.localeCompare(b.operationId))) {
      this.records.set(record.operationId, record);
    }
  }

  /** Intents the replica commits together with its next checkpoint or updates. */
  unpreserved(): PendingDocumentFormat[] {
    return [...this.unpreservedIds].flatMap((id) => { const record = this.records.get(id); return record ? [record] : []; });
  }

  preserved(records: readonly PendingDocumentFormat[]): void {
    for (const record of records) this.unpreservedIds.delete(record.operationId);
  }

  get count(): number { return this.records.size; }
  current(document: EditorDocument): DocumentFormat {
    const records = [...this.records.values()];
    return records[records.length - 1] ?? { eol: document.eol, mixedEol: document.mixed_eol };
  }

  stage(document: EditorDocument, clientId: string, format: DocumentFormat, author: { sessionId: string }): boolean {
    const current = this.current(document);
    if (current.eol === format.eol && current.mixedEol === format.mixedEol) return false;
    const record: PendingDocumentFormat = { kind: "format", ...outboxDocument(document),
      clientId, operationId: crypto.randomUUID(), createdAt: new Date().toISOString(), ...format, ...author };
    this.records.set(record.operationId, record);
    this.unpreservedIds.add(record.operationId);
    return true;
  }

  async deliver(client: LycaonClient, latest: () => { document: EditorDocument; text: string }, receive: (document: EditorDocument) => void,
    synchronize: () => Promise<void>): Promise<void> {
    for (const record of [...this.records.values()]) {
      for (let attempt = 0; ; attempt++) {
        const { document, text } = latest();
        const command = record.command ?? documentCommand(document, { action: "replace", request: {
          client_id: record.clientId, session_id: record.sessionId || null,
          history_vector: null, operation_id: crypto.randomUUID(), expected_revision: document.revision,
          content: text, eol: record.eol, mixed_eol: record.mixedEol,
        } });
        record.command = command;
        await this.outbox.put(record);
        this.unpreservedIds.delete(record.operationId);
        try {
          receive(await deliverDocumentCommand(client, command, this.outbox));
          break;
        } catch (error) {
          if (!(error instanceof LycaonApiError) || error.code !== "editor_revision_conflict") throw error;
          await this.outbox.remove(command);
          delete record.command;
          await this.outbox.put(record);
          if (attempt >= 2) throw error;
          await synchronize();
        }
      }
      await this.outbox.remove(record);
      this.records.delete(record.operationId);
      this.unpreservedIds.delete(record.operationId);
    }
  }
}
