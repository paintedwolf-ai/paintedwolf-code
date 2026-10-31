import { batch } from "solid-js";
import { produce } from "solid-js/store";
import type { ProjectSourceReadResponse, SourceEncoding } from "../../api/types.ts";
import { detectIndent, type IndentInfo } from "../../components/source/editor/indent-detect.ts";
import type { EditorConfigProps } from "../../components/source/editor/editorconfig.ts";
import { defaultEol, normalizeEolForEditor, type EolKind } from "../../components/source/editor/eol.ts";
import { bufferKindFromSourceRead } from "./project-files-buffer-kind.ts";
import { fileBufferKey, type FileBufferKey } from "../components/project-files-model.ts";
import type { FileDocumentOpening } from "./file-document-opening.ts";
import { flushFilesDraftSyncFor, cancelFilesDraftSyncFor } from "./files-draft-sync.ts";
import { getFilesEditorViewDoc, invalidateFilesEditorViewDoc } from "../editor/files-editor-host.ts";
import { retainEditorDocument, editorReplica } from "./editor-document.ts";
import type { EditorEditingBlock } from "./editor-draft.ts";
import {
  type FileBuffer,
  filesBufferNeedsBody,
  projectFilesState,
  setFilesStore,
} from "./files-buffer-state.ts";
import {
  filesBufferBase,
  filesBufferText,
  settleBufferPresentation,
} from "./project-files-buffers.ts";

type EditorDocumentStatus = {
  resolving?: boolean;
  editingBlock?: EditorEditingBlock | null;
  refusal?: string | null;
  participants?: number;
  synchronization?: "preserving" | "pending" | "accepted" | "error";
  fileId: string;
  documentId: string;
  revision: number;
  diverged: boolean;
  absent: boolean;
  heldAgentVersionId: string | null;
};

type EditorDocumentContent = EditorDocumentStatus & {
  encoding: SourceEncoding;
  localGeneration: number;
  dirty: boolean;
  baseSha256: string | null;
  sizeBytes: number;
  eol: EolKind;
  baseEol: EolKind;
  mixedEol: boolean;
  baseMixedEol: boolean;
};

function bufferIsDirty(buffer: FileBuffer): boolean {
  const replica = editorReplica(buffer.documentId);
  if (buffer.content.state === "suspended") return buffer.dirty;
  return (replica?.dirty ?? (filesBufferText(buffer) !== filesBufferBase(buffer)))
    || buffer.eol !== buffer.baseEol || buffer.mixedEol !== buffer.baseMixedEol;
}

export function markFilesBufferLoading(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.loading = true;
      buf.editorOpening = undefined;
      buf.loadError = null;
    }),
  );
}

/** A superseded read settles without replacing the current draft. */
export function finishFilesBufferLoad(projectId: string, key: FileBufferKey): void {
  setFilesStore(projectId, produce((state) => {
    const buffer = state.byKey[key];
    if (!buffer) return;
    buffer.loading = false;
    buffer.condenseContext = false;
    settleBufferPresentation(state, key);
  }));
}

/** A background read cannot replace a newer edit or a reopened buffer. */
export function applyFilesBufferRefresh(
  projectId: string,
  key: FileBufferKey,
  expectedBuffer: FileBuffer,
  expectedRevision: number,
  res: ProjectSourceReadResponse,
): boolean {
  flushFilesDraftSyncFor(projectId, key);
  const current = projectFilesState(projectId).byKey[key];
  if (
    current !== expectedBuffer || current.dirty || filesBufferNeedsBody(current) ||
    current.editRevision !== expectedRevision
  ) return false;
  applyFilesBufferLoad(projectId, key, res);
  return true;
}

/** Text an open response carried inside its editor document, with the host's line-ending facts. */
export type LoadedDocumentText = { text: string; eol: EolKind; mixed: boolean; dirty: boolean; absent?: boolean };

export function applyFilesBufferLoad(
  projectId: string,
  key: FileBufferKey,
  res: ProjectSourceReadResponse,
  document?: LoadedDocumentText,
): FileBufferKey {
  return batch(() => {
    flushFilesDraftSyncFor(projectId, key);
    const held = projectFilesState(projectId).byKey[key];
    const fileId = res.file_id?.trim() ?? "";
    const stableKey = held && fileId
      ? fileBufferKey(held.rootId, res.path, held.jobId, fileId)
      : key;
    let reuseLoadedBuffer = false;
    if (stableKey !== key) {
      cancelFilesDraftSyncFor(projectId, key);
      invalidateFilesEditorViewDoc(projectId, key);
      setFilesStore(
        projectId,
        produce((state) => {
          const provisional = state.byKey[key];
          if (!provisional) return;
          const existing = state.byKey[stableKey];
          if (existing) {
            // A duplicate read cannot replace an already loaded editing session.
            reuseLoadedBuffer = existing.dirty || !existing.loading;
            state.order = state.order.filter((entry) => entry !== key);
            delete state.byKey[key];
            if (state.activeKey === key) state.activeKey = stableKey;
            if (state.pendingKey === key) state.pendingKey = stableKey;
            if (reuseLoadedBuffer) settleBufferPresentation(state, stableKey);
            return;
          }
          const next = { ...provisional, key: stableKey, fileId };
          state.byKey[stableKey] = next;
          delete state.byKey[key];
          state.order = state.order.map((entry) => entry === key ? stableKey : entry);
          if (state.activeKey === key) state.activeKey = stableKey;
          if (state.pendingKey === key) state.pendingKey = stableKey;
        }),
      );
    }
    key = stableKey;
    if (reuseLoadedBuffer) return key;
    const kind = bufferKindFromSourceRead(res);
    const { text, eol, mixed } =
      kind !== "text"
        ? { text: "", eol: defaultEol(), mixed: false }
        : document ?? normalizeEolForEditor(res.content);
    const detectedIndent = kind === "text" ? detectIndent(text) : undefined;
    setFilesStore(
      projectId,
      produce((state) => {
        const buf = state.byKey[key];
        if (!buf) return;
        buf.loading = false;
        settleBufferPresentation(state, key);
        buf.fileId = fileId || null;
        buf.deleted = res.deleted;
        buf.absent = document?.absent === true;
        buf.sourcePresent = res.deleted == null && !buf.absent;
        if (res.deleted || kind !== "text") {
          buf.documentId = null;
          buf.documentRevision = null;
          buf.condenseContext = false;
        }
        buf.loadError = null;
        buf.kind = kind;
        buf.mime = res.mime?.trim() || null;
        buf.language = res.language ? { path: res.path, name: res.language } : undefined;
        buf.mtime = res.modified_at?.trim() || null;
        buf.overLimit = res.over_limit;
        buf.encoding = res.encoding ?? null;
        buf.writable = res.writable;
        buf.editorOpening = kind === "text" && !res.over_limit && res.encoding && res.writable !== false && !buf.jobId && !res.deleted
          ? { status: "opening" } : undefined;
        buf.unsupportedEncodingDetected = null;
        buf.content = { state: "source", text, base: text };
        buf.eol = eol;
        buf.baseEol = eol;
        buf.mixedEol = mixed;
        buf.baseMixedEol = mixed;
        buf.detectedIndent = detectedIndent;
        buf.indentOverride = undefined;
        buf.editorConfig = undefined;
        buf.baseSha256 = res.sha256?.trim() || null;
        buf.sizeBytes = res.size_bytes;
        buf.dirty = document ? buf.dirty || document.dirty : false;
        buf.editRevision += 1;
        buf.diverged = false;
        buf.heldAgentVersionId = null;
      }),
    );
    return key;
  });
}

/** Apply a 415 unsupported_encoding refusal as an info-card buffer. */
export function applyFilesBufferUnsupportedEncoding(
  projectId: string,
  key: FileBufferKey,
  detected: string,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.loading = false;
      buf.condenseContext = false;
      settleBufferPresentation(state, key);
      buf.loadError = null;
      buf.kind = "info";
      buf.deleted = undefined;
      buf.sourcePresent = true;
      buf.mime = null;
      buf.mtime = null;
      buf.encoding = null;
      buf.writable = false;
      buf.sizeBytes = 0;
      buf.overLimit = false;
      buf.unsupportedEncodingDetected =
        detected.trim() || "unknown";
      buf.content = { state: "source", text: "", base: "" };
      buf.baseSha256 = null;
      buf.dirty = false;
      buf.editRevision += 1;
      buf.diverged = false;
      buf.heldAgentVersionId = null;
    }),
  );
}

/** Session indent settings leave existing whitespace unchanged. */
export function setFilesBufferIndentOverride(
  projectId: string,
  key: FileBufferKey,
  indent: IndentInfo | undefined,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.indentOverride = indent;
    }),
  );
}

/** Undefined clears the path's resolved formatting settings. */
export function setFilesBufferEditorConfig(
  projectId: string,
  key: FileBufferKey,
  props: EditorConfigProps | undefined,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.editorConfig = props;
    }),
  );
}

/** Select the representation used at the next save; choosing normalizes mixed EOLs. */
export function setFilesBufferEol(
  projectId: string,
  key: FileBufferKey,
  eol: EolKind,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf || (buf.eol === eol && !buf.mixedEol)) return;
      buf.eol = eol;
      buf.mixedEol = false;
      buf.editRevision += 1;
      buf.dirty = bufferIsDirty(buf);
      if (buf.dirty) buf.preview = false;
    }),
  );
}

export function applyFilesBufferLoadError(
  projectId: string,
  key: FileBufferKey,
  message: string,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.loading = false;
      buf.condenseContext = false;
      settleBufferPresentation(state, key);
      buf.loadError = message;
    }),
  );
}

export function setFilesBufferDocumentOpening(
  projectId: string,
  key: FileBufferKey,
  opening: FileDocumentOpening | undefined,
): void {
  setFilesStore(projectId, produce((state) => {
    const buffer = state.byKey[key];
    if (buffer) {
      const previous = buffer.editorOpening;
      if (previous?.status === opening?.status && previous?.editingBlock === opening?.editingBlock &&
        (previous && "message" in previous ? previous.message : undefined) === (opening && "message" in opening ? opening.message : undefined)) return;
      buffer.editorOpening = opening;
      if (opening?.editingBlock) buffer.editorEditingBlock = opening.editingBlock;
      else if (opening?.status === "opening") buffer.editorEditingBlock = null;
    }
  }));
}

export function applyFilesBufferDraft(
  projectId: string,
  key: FileBufferKey,
  draft: string,
): void {
  // The live editor may be ahead of its debounced store write.
  const liveDoc = getFilesEditorViewDoc(projectId, key);
  if (liveDoc != null && liveDoc !== draft) {
    invalidateFilesEditorViewDoc(projectId, key);
  }
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf || buf.kind !== "text") return;
      if (filesBufferText(buf) !== draft) {
        if (buf.content.state === "source") buf.content = { ...buf.content, text: draft };
        buf.editRevision += 1;
      }
      buf.dirty = bufferIsDirty(buf);
      if (buf.dirty) buf.preview = false;
    }),
  );
}

/** Publishes resident metadata without retaining another text representation. */
export function applyFilesBufferEditorDocument(
  projectId: string,
  key: FileBufferKey,
  document: EditorDocumentContent,
): void {
  invalidateFilesEditorViewDoc(projectId, key);
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf || buf.kind !== "text" || buf.deleted) return;
      const changed =
        buf.content.state !== "document" || buf.content.generation !== document.localGeneration ||
        buf.baseSha256 !== document.baseSha256 ||
        buf.encoding !== document.encoding ||
        buf.eol !== document.eol ||
        buf.baseEol !== document.baseEol ||
        buf.mixedEol !== document.mixedEol ||
        buf.baseMixedEol !== document.baseMixedEol;
      buf.fileId = document.fileId;
      buf.editorOpening = undefined;
      buf.editorResolving = document.resolving ?? false;
      buf.editorEditingBlock = document.editingBlock ?? null;
      buf.editorRefusal = document.refusal ?? null;
      buf.editorParticipants = document.participants ?? 1;
      buf.editorSynchronization = document.synchronization;
      buf.documentId = document.documentId;
      buf.documentRevision = document.revision;
      buf.diverged = document.diverged;
      buf.absent = document.absent;
      buf.sourcePresent = !document.absent;
      buf.heldAgentVersionId = document.heldAgentVersionId;
      buf.content = { state: "document", generation: document.localGeneration };
      buf.baseSha256 = document.baseSha256;
      buf.encoding = document.encoding;
      buf.sizeBytes = document.sizeBytes;
      buf.eol = document.eol;
      buf.baseEol = document.baseEol;
      buf.mixedEol = document.mixedEol;
      buf.baseMixedEol = document.baseMixedEol;
      if (changed) buf.editRevision += 1;
      buf.dirty = document.dirty;
      if (buf.dirty) buf.preview = false;
    }),
  );
}

/** Marks the buffer dirty before the debounced draft update. */
export function markFilesBufferTyping(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf || buf.kind !== "text") return;
      retainEditorDocument(buf);
      // Revision moves when the draft materializes.
      if (buf.dirty && !buf.preview) return;
      if (buf.preview) buf.preview = false;
      buf.dirty = true;
      buf.editRevision += 1;
    }),
  );
}

export function applyFilesBufferSaved(
  projectId: string,
  key: FileBufferKey,
  saved: {
    content: string;
    sha256: string;
    sizeBytes: number;
    eol: EolKind;
    editRevision: number;
    fileId: string;
    documentId: string;
    documentRevision: number;
  },
): void {
  flushFilesDraftSyncFor(projectId, key);
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      if (buf.documentRevision != null && saved.documentRevision < buf.documentRevision) return;
      const unchangedSinceSave = buf.editRevision === saved.editRevision;
      if (buf.content.state === "source") buf.content = { ...buf.content, base: saved.content };
      buf.baseEol = saved.eol;
      buf.baseMixedEol = false;
      if (unchangedSinceSave) {
        if (buf.content.state === "source") buf.content = { ...buf.content, text: saved.content };
        buf.eol = saved.eol;
        buf.mixedEol = false;
      }
      buf.baseSha256 = saved.sha256;
      buf.fileId = saved.fileId;
      buf.documentId = saved.documentId;
      buf.documentRevision = saved.documentRevision;
      buf.sizeBytes = saved.sizeBytes;
      buf.dirty = editorReplica(buf.documentId)?.dirty ?? bufferIsDirty(buf);
      buf.diverged = false;
      // Saving settles the retained agent edit.
      buf.heldAgentVersionId = null;
      buf.closeError = null;
      if (buf.preview) buf.preview = false;
    }),
  );
}

export function discardFilesBufferDraft(
  projectId: string,
  key: FileBufferKey,
): void {
  flushFilesDraftSyncFor(projectId, key);
  invalidateFilesEditorViewDoc(projectId, key);
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      const changed =
        filesBufferText(buf) !== filesBufferBase(buf) ||
        buf.eol !== buf.baseEol ||
        buf.mixedEol !== buf.baseMixedEol;
      if (buf.content.state === "source") buf.content = { ...buf.content, text: buf.content.base };
      buf.eol = buf.baseEol;
      buf.mixedEol = buf.baseMixedEol;
      if (changed) buf.editRevision += 1;
      buf.dirty = bufferIsDirty(buf);
      buf.diverged = false;
      // History retains the discarded agent edit.
      buf.heldAgentVersionId = null;
    }),
  );
}

export function setFilesBufferCapacityError(projectId: string, key: string, message: string): void {
  setFilesStore(projectId, produce(state => {
    const buffer = state.byKey[key];
    if (!buffer) return;
    buffer.editorEditingBlock = "capacity";
    buffer.loadError = message;
    buffer.loading = false;
  }));
}

export function markFilesDocumentUnavailable(projectId: string, documentId: string): void {
  setFilesStore(projectId, produce(state => {
    for (const buffer of Object.values(state.byKey)) {
      if (buffer.documentId !== documentId || !filesBufferNeedsBody(buffer)) continue;
      buffer.loadError = "This document is no longer available from the host. Preserved edits remain in local storage.";
    }
  }));
}
