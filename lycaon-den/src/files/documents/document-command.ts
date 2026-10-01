import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import type { EditorDocument, SaveEditorDocumentRequest, ResolveEditorDocumentRequest, RevertEditorDocumentChangeRequest, ReplaceEditorDocumentRequest, EditorDocumentCommandRequest } from "../../api/types.ts";
import { documentOutbox, outboxDocument, type DocumentOutbox, type OutboxDocument, type ReplicaCheckpoint, type OutboxRecord } from "./document-outbox.ts";

/** Failures that leave a command's outcome open, so it stays queued for replay. */
const UNSETTLED_CODES = ["unauthorized", "rate_limited", "internal_error", "host_fault"] as const;

export type CommandPayload =
  | { action: "save"; request: SaveEditorDocumentRequest }
  | { action: "resolve"; request: ResolveEditorDocumentRequest }
  | { action: "revert"; changeId: string; request: RevertEditorDocumentChangeRequest }
  | { action: "replace"; request: ReplaceEditorDocumentRequest }
  | { action: "discard" | "reload"; request: EditorDocumentCommandRequest };

export type PendingDocumentCommand = CommandPayload & OutboxDocument & {
  kind: "command";
  documentId: string;
  projectId: string;
  clientId: string;
  operationId: string;
  sessionId?: string;
  createdAt: string;
  historyBase?: ReplicaCheckpoint;
  result?: EditorDocument;
};

export function documentCommand(document: EditorDocument, payload: CommandPayload, sessionId?: string): PendingDocumentCommand {
  const authored = { ...payload };
  authored.request = { ...(sessionId ? { session_id: sessionId } : {}), ...payload.request };
  return { ...authored, kind: "command", ...outboxDocument(document), clientId: payload.request.client_id,
    operationId: payload.request.operation_id, sessionId, createdAt: new Date().toISOString() };
}

async function sendCommand(client: LycaonClient, command: PendingDocumentCommand): Promise<EditorDocument> {
  const { projectId, documentId, sessionId } = command;
  switch (command.action) {
    case "save": return client.saveEditorDocument(projectId, documentId, command.request, sessionId);
    case "resolve": return client.resolveEditorDocumentConflict(projectId, documentId, command.request, sessionId);
    case "revert": return client.revertEditorDocumentChange(projectId, documentId, command.changeId, command.request);
    case "replace": return client.replaceEditorDocument(projectId, documentId, command.request);
    case "discard": return client.discardEditorDocument(projectId, documentId, command.request);
    case "reload": return client.reloadEditorDocument(projectId, documentId, command.request, sessionId);
  }
}

function definitiveRejection(cause: unknown): boolean {
  return cause instanceof LycaonApiError && !isApiErrorCode(cause, UNSETTLED_CODES);
}

// Retain the request until acknowledgement and local effects complete for safe replay.
export async function deliverDocumentCommand(client: LycaonClient | null, command: PendingDocumentCommand, outbox = documentOutbox(), accepted?: (document: EditorDocument) => Promise<void>): Promise<EditorDocument> {
  await outbox.put(command);
  let document: EditorDocument;
  try {
    if (!client && !command.result) throw new Error("The editor is offline.");
    // Persist intent before reserving host state. Revision zero is local-only:
    // restart retries the same reservation and records its exact revision before publication.
    if (command.action === "save" && command.request.expected_revision === 0) {
      const pinned = await client!.createEditorDocumentSnapshot(command.projectId, command.documentId, {
        client_id: command.clientId, operation_id: command.operationId,
      });
      command = { ...command, request: { ...command.request, expected_revision: pinned.save_revision ?? pinned.revision } };
      await outbox.put(command);
    }
    document = command.result ?? await sendCommand(client!, command);
  }
  catch (cause) {
    if (definitiveRejection(cause)) {
      // The host rejected this action without accepting its effect. The draft and
      // undo checkpoint remain preserved; a transport record needs no manual disposal.
      await outbox.remove(command);
    }
    throw cause;
  }
  if (command.historyBase) {
    await outbox.put({ ...command, result: document });
    if (!accepted) return document;
  }
  await accepted?.(document);
  await outbox.remove(command);
  return document;
}

export async function resumeDocumentCommands(client: LycaonClient, document: EditorDocument, outbox: DocumentOutbox = documentOutbox(), recovered?: OutboxRecord[], read = () => outbox.read(document.id)): Promise<{ document: EditorDocument; records: OutboxRecord[] }> {
  const records = recovered ?? await read();
  const commands = records.filter((record): record is PendingDocumentCommand =>
    record.kind === "command" && !record.historyBase)
    .sort((a,b) => a.createdAt.localeCompare(b.createdAt) || a.operationId.localeCompare(b.operationId));
  for (const command of commands) {
    try {
      const response = await deliverDocumentCommand(client, command, outbox);
      if (response.revision >= document.revision) document = response;
    } catch (cause) {
      if (!definitiveRejection(cause)) throw cause;
    }
  }
  return { document, records: commands.length ? await read() : records };
}
