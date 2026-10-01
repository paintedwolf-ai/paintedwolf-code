import { documentResidency, documentResourceKey, documentOpeningReservation, documentAdmissionBytes, type DocumentReservation, type ResidencyPriority } from "../documents/document-residency.ts";
import { clientIdentity, prepareClientIdentity } from "../../platform/connection/client-identity.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { EditorDocument, OpenEditorDocumentRequest, ProjectSourceReadResponse, SourceEncoding } from "../../api/types.ts";
import { isBackendUnreachableError } from "../../platform/connection/request-connectivity.ts";
import { releaseUnclaimedEditorDocument, retainedOpening, type RetainedOpening } from "../documents/editor-document.ts";
import { isLockedImageMime } from "../documents/project-files-buffer-kind.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";

export function explicitSourceEncoding(
  encoding: SourceEncoding | null | undefined,
) {
  return encoding === "utf-16le" || encoding === "utf-16be" ? encoding : undefined;
}

/** Reuse a human's BOM-less encoding only when the current bytes need it. */
export async function readFileBufferSource(
  client: LycaonClient,
  projectId: string,
  buffer: Pick<FileBuffer, "encoding" | "path" | "jobId" | "rootId">,
  sessionId?: string,
) {
  const decodeAs = explicitSourceEncoding(buffer.encoding);
  const { path, jobId, rootId } = buffer;
  const read = (encoding?: "utf-16le" | "utf-16be") =>
    client.getProjectSource(projectId, path, {
      workerId: jobId,
      rootId: rootId || undefined,
      sessionId,
      decodeAs: encoding,
      includeDeleted: Boolean(rootId),
    });
  try {
    const source = await read();
    if (
      !decodeAs || !source.binary || source.over_limit ||
      isLockedImageMime(source.mime)
    ) return source;
  } catch (error: unknown) {
    if (
      !decodeAs || !(error instanceof LycaonApiError) ||
      error.code !== "unsupported_encoding"
    ) throw error;
  }
  return read(decodeAs);
}

/** The source response and the retained state used to complete its document frame. */
export type OpenedFileBufferSource = {
  source: ProjectSourceReadResponse;
  document?: EditorDocument;
  retained?: RetainedOpening;
  reservation?: DocumentReservation;
  offline?: boolean;
};

/** The source description of a file whose content lives in its host document. */
function documentSource(document: EditorDocument, writable: boolean): ProjectSourceReadResponse {
  return {
    file_id: document.file_id, version_id: "", workspace_id: document.workspace_id, workspace_kind: "project",
    root_id: document.root_id, path: document.path, content: "", over_limit: false, binary: false,
    size_bytes: document.size_bytes, encoding: document.encoding, sha256: document.base_sha256, writable,
  };
}

async function openSourceDocument(client: LycaonClient, projectId: string, request: OpenEditorDocumentRequest, sessionId?: string) {
  const document = await client.openEditorDocument(projectId, request, sessionId);
  return { source: documentSource(document, true), document };
}

/** A document for a deleted path is the file only while it holds a draft. */
function holdsDraft(document: EditorDocument): boolean {
  return !document.absent || document.dirty || !!document.held_agent_version_id;
}

/** The host opens only editable text as a document; any other file is read as source. */
function isSourceOnly(error: unknown): boolean {
  return error instanceof LycaonApiError && (error.code === "source_binary" || error.code === "source_content_too_large" || error.code === "source_read_only");
}

/** One round trip opens an editable file's host document, requesting only missing state. */
export async function openFileBufferSource(client: LycaonClient, projectId: string,
  buffer: Pick<FileBuffer, "encoding" | "path" | "jobId" | "rootId"> & Partial<Pick<FileBuffer, "documentId" | "fileId" | "writable">>, sessionId?: string, priority: ResidencyPriority = "foreground", current = () => true): Promise<OpenedFileBufferSource> {
  const reservation = await documentResidency.acquire(documentResourceKey(projectId, buffer), documentOpeningReservation(), priority, current);
  if (!reservation) throw new DOMException("File opening was superseded.", "AbortError");
  try {
    const opened = await readOpenedFileBuffer(client, projectId, buffer, reservation, sessionId);
    if (opened.document) await reservation.expand(documentAdmissionBytes(opened.document.size_bytes, opened.document.crdt_update.length));
    return { ...opened, reservation };
  }
  catch (error) { reservation.release(); throw error; }
}

async function readOpenedFileBuffer(client: LycaonClient, projectId: string,
  buffer: Pick<FileBuffer, "encoding" | "path" | "jobId" | "rootId"> & Partial<Pick<FileBuffer, "documentId" | "fileId" | "writable">>, reservation: DocumentReservation, sessionId?: string): Promise<OpenedFileBufferSource> {
  if (buffer.jobId || !buffer.rootId) return { source: await readFileBufferSource(client, projectId, buffer, sessionId) };
  await prepareClientIdentity();
  const retained = await retainedOpening(projectId, { rootId: buffer.rootId, path: buffer.path, documentId: buffer.documentId }, reservation);
  const read = async (decode_as?: "utf-16le" | "utf-16be"): Promise<OpenedFileBufferSource> => {
    try {
      const opened = await openSourceDocument(client, projectId, {
        path: buffer.path, root_id: buffer.rootId, client_id: clientIdentity(), ...(decode_as ? { decode_as } : {}), ...(retained?.replica ? { replica: retained.replica } : {}),
      }, sessionId);
      if (!holdsDraft(opened.document)) {
        void releaseUnclaimedEditorDocument(projectId, opened.document).catch(() => undefined);
        return { source: await readFileBufferSource(client, projectId, buffer, sessionId) };
      }
      return { ...opened, ...(retained ? { retained } : {}) };
    } catch (error) {
      if (!isSourceOnly(error)) throw error;
      return { source: await readFileBufferSource(client, projectId, buffer, sessionId) };
    }
  };
  const decodeAs = explicitSourceEncoding(buffer.encoding);
  try {
    try {
      const opened = await read();
      if (!decodeAs || !opened.source.binary || opened.source.over_limit || isLockedImageMime(opened.source.mime)) return opened;
    } catch (error) {
      if (!decodeAs || !(error instanceof LycaonApiError) || error.code !== "unsupported_encoding") throw error;
    }
    return await read(decodeAs);
  } catch (error) {
    if (!isBackendUnreachableError(error) || !retained?.checkpoint) throw error;
    const document = retained.checkpoint.confirmed;
    return { document, offline: true, retained, source: documentSource(document, buffer.writable === true) };
  }
}
