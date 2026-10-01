import { documentResidency, documentResourceKey, documentOpeningReservation, type DocumentReservation } from "./document-residency.ts";
import { suspendFileDocument } from "./files-residency.ts";
import { deliverDocumentCommand, documentCommand, resumeDocumentCommands } from "./document-command.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { EditorDocument } from "../../api/types.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { saveHygieneChanges } from "../../components/source/editor/save-hygiene.ts";
import { refreshOpenEditorConfigs } from "../../components/source/editor/editorconfig-load.ts";
import {
  canEditLoadedSource,
  isSourceWriteConflict,
  sourceSaveErrorMessage,
} from "../../components/source/editor/source-editor-model.ts";
import { LycaonApiError } from "../../api/http.ts";
import { applyFilesBufferSaved } from "./project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "./files-buffer-state.ts";
import { flushFilesDraftSyncFor } from "./files-draft-sync.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import {
  editorReplica,
  resolveEditorDocument,
  publishEditorDocumentDraft,
  observeEditorDocument,
  receiveEditorDocument,
  runEditorDocumentOperation,
} from "./editor-document.ts";

type ProjectFileDocumentSaveResult =
  | { status: "clean"; buffer: FileBuffer }
  | { status: "saved"; buffer: FileBuffer; sha256: string }
  | { status: "unavailable"; message: string }
  | { status: "conflict"; message: string }
  | { status: "error"; message: string };

/** Saves a Files buffer independently of its presentation. */
type SaveArguments = {
  client: LycaonClient;
  projectId: string;
  key: FileBufferKey;
  sessionId?: string;
};

export async function saveProjectFileDocument(args: SaveArguments): Promise<ProjectFileDocumentSaveResult> {
  const buffer = projectFilesState(args.projectId).byKey[args.key];
  if (!buffer || !buffer.dirty) return saveResidentDocument(args);
  const cold = buffer.content.state === "suspended";
  let reservation: DocumentReservation | null = null;
  try {
    reservation = await documentResidency.acquire(documentResourceKey(args.projectId, buffer), documentOpeningReservation(), "foreground");
    if (!reservation) return { status: "unavailable", message: "The file is no longer open." };
    if (!editorReplica(buffer.documentId)) await resolveEditorDocument(args.projectId, buffer, undefined, reservation);
    return await saveResidentDocument(args);
  } catch (error) {
    return { status: "error", message: sourceSaveErrorMessage(error) };
  } finally {
    reservation?.release();
    // Preservation failure keeps the replica and its storage block visible.
    if (cold && reservation) await suspendFileDocument(args.projectId, buffer.rootId, buffer.path, buffer.documentId ?? undefined).catch(() => false);
  }
}

async function saveResidentDocument(args: SaveArguments): Promise<ProjectFileDocumentSaveResult> {
  flushFilesDraftSyncFor(args.projectId, args.key);
  const buffer = projectFilesState(args.projectId).byKey[args.key];
  if (!buffer) {
    return { status: "unavailable", message: "The file buffer is not open." };
  }
  if (!buffer.dirty) return { status: "clean", buffer };
  const baseSha256 = buffer.baseSha256;
  const encoding = buffer.encoding;
  if (
    !baseSha256 ||
    !encoding ||
    !buffer.documentId ||
    buffer.documentRevision == null ||
    !canEditLoadedSource({
      kind: buffer.kind,
      overLimit: buffer.overLimit,
      sha256: buffer.baseSha256,
      encoding: buffer.encoding,
      jobId: buffer.jobId,
      writable: buffer.writable,
    })
  ) {
    return {
      status: "unavailable",
      message: "This file cannot be saved from the in-app editor.",
    };
  }

  const replica = editorReplica(buffer.documentId);
  if (!replica) return { status: "unavailable", message: "The collaborative editor is not connected." };
  // Exact edits, so every untouched character keeps its author.
  const hygiene = saveHygieneChanges(replica.history.state.doc, buffer.eol, buffer.editorConfig);
  const eol = hygiene.eol;
  const editRevision = buffer.editRevision;
  let releaseDelivery: (() => void) | undefined;
  try {
    const documentId = buffer.documentId;
    replica.history.boundary();
    replica.applyLocalChanges(hygiene.changes, "input.hygiene");
    releaseDelivery = replica.holdLaterDelivery();
    const save = () => deliverDocumentCommand(args.client, documentCommand(replica.accepted, { action: "save", request: {
      ...replica.authorship,
      client_id: clientIdentity(), expected_revision: 0, operation_id: crypto.randomUUID(),
    } }, args.sessionId));
    const response = await runEditorDocumentOperation(documentId, async () => {
      await publishEditorDocumentDraft(args.projectId, { ...buffer, eol, mixedEol: false });
      receiveEditorDocument((await resumeDocumentCommands(args.client, replica.accepted)).document);
      let saved: EditorDocument;
      try {
        saved = await save();
      } catch (error) {
        // A reservation behind the saved base is reserved again, once.
        if (!(error instanceof LycaonApiError && error.code === "editor_revision_conflict")) throw error;
        await replica.synchronize();
        if (replica.accepted.diverged) throw error;
        saved = await save();
      }
      receiveEditorDocument(saved);
      return saved;
    });
    applyFilesBufferSaved(args.projectId, args.key, {
      content: replica.baseText,
      sha256: response.base_sha256,
      sizeBytes: response.size_bytes,
      eol,
      editRevision,
      fileId: response.file_id,
      documentId: response.id,
      documentRevision: response.revision,
    });
    if (
      buffer.path === ".editorconfig" ||
      buffer.path.endsWith("/.editorconfig")
    ) {
      await refreshOpenEditorConfigs({
        client: args.client,
        projectId: args.projectId,
        rootId: buffer.rootId,
        sessionId: args.sessionId,
      });
    }
    const saved = projectFilesState(args.projectId).byKey[args.key];
    return {
      status: "saved",
      buffer: saved ?? buffer,
      sha256: response.base_sha256,
    };
  } catch (error: unknown) {
    const message = sourceSaveErrorMessage(error);
    if (isSourceWriteConflict(error)) {
      const current = projectFilesState(args.projectId).byKey[args.key];
      if (current) {
        await observeEditorDocument(args.projectId, current).catch(() => undefined);
      }
      return { status: "conflict", message };
    }
    return { status: "error", message };
  } finally {
    releaseDelivery?.();
  }
}
