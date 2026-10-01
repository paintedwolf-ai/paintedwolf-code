import { chatContentDocumentKey, type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { batch } from "solid-js";
import { produce, unwrap } from "solid-js/store";
import type { ProjectSourceReadResponse, SourceEncoding } from "../../api/types.ts";
import { detectIndent, type IndentInfo } from "../../components/source/editor/indent-detect.ts";
import type { EditorConfigProps } from "../../components/source/editor/editorconfig.ts";
import { defaultEol, normalizeEolForEditor, type EolKind } from "../../components/source/editor/eol.ts";
import { bufferKindFromSourceRead, isComposedBufferKind, type FileBufferKind } from "./project-files-buffer-kind.ts";
import {
  clampDragIndexWithinGroup,
  closeTargetKeys,
  orderWithPinnedGroup,
  type CloseFamily,
} from "../tabs/files-tab-strip.ts";
import {
  fileBufferKey,
  fileDisplayName,
  neighborKeyAfterClose,
  retargetPathUnderPath,
  diffsBufferKey,
  walkPageBufferKey,
  type FileBufferKey,
} from "../components/project-files-model.ts";
import type { FileDocumentOpening } from "./file-document-opening.ts";
import type { WalkGroupStep } from "../walk/walk-model.ts";
import { diffsAddressKey, type DiffsAddress } from "../review/diffs-address.ts";
import { flushFilesDraftSyncFor, cancelFilesDraftSyncFor, resetFilesDraftSyncForTests } from "./files-draft-sync.ts";
import { getFilesEditorViewDoc, invalidateFilesEditorViewDoc } from "../editor/files-editor-host.ts";
import {
  discardEditorDocument,
  evictEditorDocument,
  retainEditorDocument,
  scheduleEditorDocumentDraft,
  flushEditorDocumentDraft,
  forgetEditorDocument,
  preserveEditorDocumentDraft,
  editorReplica,
} from "./editor-document.ts";
import type { EditorEditingBlock } from "./editor-draft.ts";
import {
  type AimOrigin,
  type BufferOpenIntent,
  type FileBuffer,
  type FileDiffPreview,
  type GitReviewSelection,
  type ProjectFilesState,
  emptyFilesState,
  ensureProject,
  filesBufferNeedsBody,
  filesStore,
  nextBufferPayloadGeneration,
  projectFilesState,
  setFilesStore,
  setFilesStoreRaw,
} from "./files-buffer-state.ts";

/** Text materialization is deferred until read. */
export function filesBufferText(buffer: Pick<FileBuffer, "content" | "documentId" | "editRevision">): string {
  void buffer.editRevision;
  if (buffer.content.state === "source") return buffer.content.text;
  return editorReplica(buffer.documentId)?.currentText ?? "";
}

export function filesBufferBase(buffer: Pick<FileBuffer, "content" | "documentId">): string {
  if (buffer.content.state === "source") return buffer.content.base;
  return editorReplica(buffer.documentId)?.baseText ?? "";
}

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

/** A checkout switch preserves replicas, then reopens only the substituted roots. */
export async function switchFilesRootBranches(projectId: string, branches: Record<string, string>, current: () => boolean): Promise<boolean> {
  ensureProject(projectId);
  const state = projectFilesState(projectId);
  const changed = new Set(Object.keys(branches).filter((root) => state.rootBranches[root] !== undefined && state.rootBranches[root] !== branches[root]));
  const affected = state.order.flatMap((key) => {
    const buffer = state.byKey[key];
    if (!buffer || buffer.jobId || !changed.has(buffer.rootId)) return [];
    flushFilesDraftSyncFor(projectId, key);
    return [{ ...unwrap(buffer) }];
  });
  if (!affected.length) {
    if (!current()) return false;
    setFilesStore(projectId, produce((next) => { next.rootBranches = branches; }));
    return true;
  }
  for (const buffer of affected) {
    if (buffer.dirty && !(await preserveEditorDocumentDraft(buffer))) {
      throw new Error(`Couldn't preserve the unsaved edits in ${buffer.path}.`);
    }
  }
  if (!current()) return false;
  for (const buffer of affected) {
    if (!(await evictEditorDocument(buffer.documentId))) return false;
  }
  if (!current()) return false;
  batch(() => {
    const order = [...state.order], aimed = state.pendingKey ?? state.activeKey, origin = state.aimOrigin;
    const replacements = new Map<string, string>();
    setFilesStore(projectId, produce((next) => {
      next.rootBranches = branches;
      for (const buffer of affected) detachBuffer(next, buffer.key);
      next.aimOrigin = "presentation";
    }));
    for (const buffer of affected) {
      const key = openFilesBuffer(projectId, { rootId: buffer.rootId, rootLabel: buffer.rootLabel, path: buffer.path,
        intent: buffer.preview ? "transient" : "permanent", pinned: buffer.pinned, origin: "presentation" });
      replacements.set(buffer.key, key);
    }
    setFilesStore(projectId, produce((next) => {
      next.order = order.map((key) => replacements.get(key) ?? key).filter((key) => next.byKey[key]);
      if (aimed) focusBuffer(next, replacements.get(aimed) ?? aimed, "presentation");
      next.aimOrigin = origin;
    }));
  });
  return true;
}

function bufferKeyAtAddress(
  state: ProjectFilesState,
  rootId: string,
  path: string,
  jobId?: string,
): FileBufferKey | null {
  const wantedJob = jobId?.trim() ?? "";
  const matches = (candidate: FileBufferKey) => {
    const buffer = state.byKey[candidate];
    return buffer != null &&
      buffer.rootId === rootId &&
      buffer.path === path &&
      (buffer.jobId ?? "") === wantedJob;
  };
  // Pending navigation takes precedence over the displayed buffer.
  const aimed = state.pendingKey ?? state.activeKey;
  if (aimed && matches(aimed)) return aimed;
  for (let index = state.order.length - 1; index >= 0; index -= 1) {
    const candidate = state.order[index];
    if (candidate && matches(candidate)) return candidate;
  }
  return null;
}

export function filesBufferKeyAtAddress(
  projectId: string,
  rootId: string,
  path: string,
  jobId?: string,
): FileBufferKey | null {
  return bufferKeyAtAddress(projectFilesState(projectId), rootId, path, jobId);
}

/** Aim the editor at `key`, presenting it once it has content. */
function focusBuffer(
  state: ProjectFilesState,
  key: FileBufferKey,
  origin: AimOrigin,
): void {
  const aimedKey = state.pendingKey ?? state.activeKey;
  const aimed = aimedKey ? state.byKey[aimedKey] : undefined;
  // Pending reader navigation takes precedence over presentation changes.
  if (
    origin === "presentation" &&
    state.aimOrigin === "reader" &&
    aimed?.loading &&
    aimedKey !== key
  ) {
    return;
  }
  if (origin === "reader") beginSourceNavigation();
  state.aimRevision++;
  const target = state.byKey[key];
  if (target?.content.state === "suspended") { target.loading = true; target.loadError = null; }
  const presenting = state.activeKey ? state.byKey[state.activeKey] : undefined;
  // A presentation that is itself loading holds nothing on screen.
  const retaining = presenting != null && !presenting.loading;
  // Re-aiming the reader's own file keeps the reader's claim on it.
  if (aimedKey !== key || origin === "reader") state.aimOrigin = origin;
  if (!target || !target.loading || !retaining) {
    state.activeKey = key;
    state.pendingKey = null;
    return;
  }
  state.pendingKey = key;
}

/** Present a buffer that finished loading, successfully or not. */
function settleBufferPresentation(
  state: ProjectFilesState,
  key: FileBufferKey,
): void {
  if (state.pendingKey !== key) return;
  state.activeKey = key;
  state.pendingKey = null;
}

/** Present an aimed buffer whose content is a past version, not a source read. */
export function presentFilesBufferVersion(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      if (!state.byKey[key]) return;
      settleBufferPresentation(state, key);
    }),
  );
}

/** The buffer the reader is aimed at, presented or still arriving. */
export function focusedFilesBufferKey(
  projectId: string,
): FileBufferKey | null {
  const state = projectFilesState(projectId);
  return state.pendingKey ?? state.activeKey;
}

function clearOtherPreviews(
  state: ProjectFilesState,
  exceptKey: FileBufferKey | null,
): void {
  for (const key of state.order) {
    if (key === exceptKey) continue;
    const buf = state.byKey[key];
    if (buf?.preview) buf.preview = false;
  }
}

function bufferIsDirty(buffer: FileBuffer): boolean {
  const replica = editorReplica(buffer.documentId);
  if (buffer.content.state === "suspended") return buffer.dirty;
  return (replica?.dirty ?? (filesBufferText(buffer) !== filesBufferBase(buffer)))
    || buffer.eol !== buffer.baseEol || buffer.mixedEol !== buffer.baseMixedEol;
}

export function openFilesBuffer(
  projectId: string,
  args: {
    rootId: string;
    rootLabel: string;
    fileId?: string;
    documentId?: string;
    path: string;
    intent: BufferOpenIntent;
    jobId?: string;
    revealLine?: number;
    revealColumn?: number;
    revealEndLine?: number;
    revealFocus?: boolean;
    condenseContext?: boolean;
    /** Initial pane while the source read is pending. */
    kind?: FileBufferKind;
    decodeAs?: "utf-16le" | "utf-16be";
    pinned?: boolean;
    preview?: boolean;
    diffPreview?: FileDiffPreview;
    /** The group step a walk page presents; keys the buffer by step. */
    walkStep?: WalkGroupStep;
    walkGitSelection?: GitReviewSelection;
    /** The comparison a diffs page reads; keys the buffer by comparison. */
    diffs?: DiffsAddress;
  chatContent?: ChatContentDocument;
    /** Tab name for a pane with no path to name it. */
    name?: string;
    /** Who is aiming; a reader unless the open follows a presentation. */
    origin?: AimOrigin;
  },
): FileBufferKey {
  const jobId = args.jobId?.trim() || undefined;
  const origin = args.origin ?? "reader";
  let key = args.kind === "chat" && args.chatContent
    ? chatContentDocumentKey(args.chatContent)
    : args.kind === "trust"
    ? `trust:${projectId}`
    : args.kind === "walk" && args.walkStep
    ? walkPageBufferKey(args.walkStep.key)
    : args.kind === "diffs" && args.diffs
    ? diffsBufferKey(diffsAddressKey(args.diffs))
    : fileBufferKey(args.rootId, args.path, jobId, args.fileId);
  ensureProject(projectId);
  setFilesStore(
    projectId,
    produce((state) => {
      if (!isComposedBufferKind(args.kind ?? "text") && !args.fileId && !state.byKey[key]) {
        const addressed = bufferKeyAtAddress(
          state,
          args.rootId,
          args.path,
          jobId,
        );
        if (addressed) key = addressed;
      }
      const existing = state.byKey[key];
      if (existing) {
        if (args.revealLine != null) existing.revealLine = args.revealLine;
        if (args.revealColumn != null) {
          existing.revealColumn = args.revealColumn;
        } else if (args.revealLine != null) {
          existing.revealColumn = null;
        }
        if (args.revealEndLine != null) {
          existing.revealEndLine = args.revealEndLine;
        } else if (args.revealLine != null) {
          existing.revealEndLine = null;
        }
        if (args.revealFocus) existing.revealFocus = true;
        if (args.condenseContext && existing.kind === "text") existing.condenseContext = true;
        if (args.intent === "permanent" && existing.preview) {
          existing.preview = false;
        }
        if (args.kind === "diff") {
          existing.rootId = args.rootId;
          existing.rootLabel = args.rootLabel;
          existing.path = args.path;
          existing.name = fileDisplayName(args.path);
          existing.diffPreview = args.diffPreview;
        }
        if (args.kind === "chat") { existing.chatContent = args.chatContent; existing.name = args.name ?? "Chat content"; }
        if (args.kind === "trust") { existing.name = args.name ?? "Trust changes"; }
        if (args.kind === "diffs" && args.diffs) {
          existing.diffs = args.diffs;
          if (args.name) existing.name = args.name;
        }
        if (args.kind === "walk") {
          existing.walkStep = args.walkStep;
          if (args.walkGitSelection) existing.walkGitSelection = args.walkGitSelection;
          if (args.name) existing.name = args.name;
        }
        focusBuffer(state, key, origin);
        retainEditorDocument(existing);
        return;
      }

      const wantPreview =
        args.preview === true ||
        (args.intent === "transient" && args.pinned !== true);
      const pinned = args.pinned === true;
      const preview = pinned ? false : wantPreview;

      if (preview) {
        // Clean previews reuse their strip position; dirty previews become permanent.
        const previewKey = state.order.find((k) => {
          const candidate = state.byKey[k];
          return candidate?.preview && !candidate.dirty;
        });
        if (previewKey && previewKey !== key) {
          const idx = state.order.indexOf(previewKey);
          void evictEditorDocument(state.byKey[previewKey]?.documentId).catch(() => undefined);
          delete state.byKey[previewKey];
          if (idx >= 0) state.order[idx] = key;
          else state.order.push(key);
        } else {
          state.order.push(key);
        }
        clearOtherPreviews(state, key);
      } else {
        state.order.push(key);
      }

      state.byKey[key] = {
        key,
        rootId: args.rootId,
        rootLabel: args.rootLabel,
        fileId: args.fileId?.trim() || null,
        path: args.path,
        name: args.name ?? fileDisplayName(args.path),
        documentId: args.documentId ?? null,
        documentRevision: null,
        ...(jobId ? { jobId } : {}),
        loading: true,
        sourcePresent: false,
        loadError: null,
        content: { state: "unloaded" },
        payloadGeneration: nextBufferPayloadGeneration(),
        eol: defaultEol(),
        baseEol: defaultEol(),
        mixedEol: false,
        baseMixedEol: false,
        detectedIndent: undefined,
        indentOverride: undefined,
        editorConfig: undefined,
        baseSha256: null,
        sizeBytes: 0,
        kind: args.kind ?? "text",
        mime: null,
        mtime: null,
        overLimit: false,
        encoding: args.decodeAs ?? null,
        writable: null,
        unsupportedEncodingDetected: null,
        dirty: false,
        editRevision: 0,
        preview,
        pinned,
        diverged: false,
        heldAgentVersionId: null,
        closeError: null,
        ...(args.kind === "diff" || args.diffPreview
          ? {
              diffPreview: args.diffPreview,
            }
          : {}),
        ...(args.kind === "chat" ? { chatContent: args.chatContent } : {}),
        ...(args.kind === "walk" ? { walkStep: args.walkStep, walkGitSelection: args.walkGitSelection } : {}),
        ...(args.kind === "diffs" ? { diffs: args.diffs } : {}),
      };
      if (args.revealLine != null) {
        state.byKey[key]!.revealLine = args.revealLine;
      }
      if (args.revealColumn != null) {
        state.byKey[key]!.revealColumn = args.revealColumn;
      }
      if (args.revealEndLine != null) {
        state.byKey[key]!.revealEndLine = args.revealEndLine;
      }
      if (args.revealFocus) state.byKey[key]!.revealFocus = true;
      if (args.condenseContext) state.byKey[key]!.condenseContext = true;
      // A composed pane has no read to wait for.
      if (args.kind && isComposedBufferKind(args.kind)) {
        state.byKey[key]!.loading = false;
        state.byKey[key]!.kind = args.kind;
      }
      // Restored pinned tabs stay in the pinned group.
      if (pinned) {
        state.order = orderWithPinnedGroup(state.order, state.byKey);
      }
      focusBuffer(state, key, origin);
    }),
  );
  return key;
}

/** Promotes a transient buffer in place. */
export function promoteFilesBuffer(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf?.preview) return;
      buf.preview = false;
    }),
  );
}

/** Composed page names follow their current comparison. */
export function renameFilesBuffer(
  projectId: string,
  key: FileBufferKey,
  name: string,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buffer = state.byKey[key];
      if (buffer && buffer.name !== name) buffer.name = name;
    }),
  );
}

export function setFilesBufferPinned(
  projectId: string,
  key: FileBufferKey,
  pinned: boolean,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (!buf) return;
      buf.pinned = pinned;
      if (pinned) {
        buf.preview = false;
      }
      state.order = orderWithPinnedGroup(state.order, state.byKey);
    }),
  );
}

export function toggleFilesBufferPinned(
  projectId: string,
  key: FileBufferKey,
): void {
  const buf = projectFilesState(projectId).byKey[key];
  if (!buf) return;
  setFilesBufferPinned(projectId, key, !buf.pinned);
}

/** Resolves the tabs affected by a close action. */
export function filesCloseTargetKeys(
  projectId: string,
  family: CloseFamily,
  focusKey: FileBufferKey | null,
): FileBufferKey[] {
  const state = projectFilesState(projectId);
  return closeTargetKeys(family, state.order, state.byKey, focusKey);
}

/** The editor consumed the one-shot line and focus target. */
export function clearFilesBufferReveal(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (buf) {
        buf.revealLine = null;
        buf.revealColumn = null;
        buf.revealEndLine = null;
        buf.revealFocus = false;
      }
    }),
  );
}

/** The editor consumed the one-shot condensed-context request. */
export function clearFilesBufferCondense(
  projectId: string,
  key: FileBufferKey,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const buf = state.byKey[key];
      if (buf) buf.condenseContext = false;
    }),
  );
}

export function setFilesActiveBuffer(
  projectId: string,
  key: FileBufferKey,
  origin: AimOrigin = "reader",
): void {
  ensureProject(projectId);
  setFilesStore(
    projectId,
    produce((state) => {
      if (!state.byKey[key]) return;
      focusBuffer(state, key, origin);
    }),
  );
}

/** Reveals an existing tab by identity, preserving historical and Walk presentations. */
export function revealFilesBuffer(projectId: string, key: FileBufferKey, line: number): boolean {
  if (!projectFilesState(projectId).byKey[key]) return false;
  setFilesStore(projectId, produce(state => {
    const buffer = state.byKey[key];
    if (!buffer) return;
    buffer.revealLine = Math.max(1, Math.floor(line));
    buffer.revealColumn = null;
    buffer.revealEndLine = null;
    focusBuffer(state, key, "reader");
  }));
  return true;
}

export function moveFilesBuffer(
  projectId: string,
  key: FileBufferKey,
  toIndex: number,
): void {
  setFilesStore(
    projectId,
    produce((state) => {
      const from = state.order.indexOf(key);
      if (from < 0) return;
      const to = clampDragIndexWithinGroup(state.order, state.byKey, from, toIndex);
      if (to === from) return;
      state.order.splice(from, 1);
      state.order.splice(to, 0, key);
      // Drag of a transient tab promotes it.
      const buf = state.byKey[key];
      if (buf?.preview) buf.preview = false;
    }),
  );
}

/** Lease released, reused by a reopen, or restored with the tab after failure. */
export type FilesBufferRelease = "released" | "superseded" | "restored";

export const FILES_CLOSE_FAILED_MESSAGE =
  "Couldn't close this tab safely, so it was kept open with its draft. Close it again to retry.";

type ClosingBuffer = {
  buffer: FileBuffer;
  released: Promise<FilesBufferRelease>;
};

const closingBuffers = new Map<string, Set<ClosingBuffer>>();

/** Removes `key` and passes the aim to its neighbour when it held it. */
function detachBuffer(state: ProjectFilesState, key: FileBufferKey): void {
  const aimedKey = state.pendingKey ?? state.activeKey;
  const successor = aimedKey === key
    ? neighborKeyAfterClose(state.order, key)
    : null;
  state.order = state.order.filter((k) => k !== key);
  delete state.byKey[key];
  if (state.pendingKey === key) state.pendingKey = null;
  if (state.activeKey === key) {
    // Nothing is left on screen, so the aim presents as it arrives.
    state.activeKey = state.pendingKey;
    state.pendingKey = null;
  }
  if (aimedKey !== key) return;
  if (successor) {
    focusBuffer(state, successor, "reader");
    return;
  }
  state.activeKey = null;
  state.pendingKey = null;
  state.aimOrigin = null;
}

/** Restores a failed close unless the address is already open. */
function restoreClosedBuffer(
  projectId: string,
  buffer: FileBuffer,
  index: number,
): boolean {
  let restored = false;
  setFilesStore(
    projectId,
    produce((state) => {
      if (state.byKey[buffer.key]) return;
      if (bufferKeyAtAddress(state, buffer.rootId, buffer.path, buffer.jobId)) return;
      const suspended = buffer.content.state === "document" && !editorReplica(buffer.documentId);
      state.byKey[buffer.key] = {
        ...buffer,
        ...(suspended ? { content: { state: "suspended" as const, generation: buffer.editRevision }, payloadGeneration: nextBufferPayloadGeneration(), loading: false } : {}),
        preview: false,
        closeError: FILES_CLOSE_FAILED_MESSAGE,
      };
      state.order.splice(Math.min(index, state.order.length), 0, buffer.key);
      state.order = orderWithPinnedGroup(state.order, state.byKey);
      if (!state.activeKey && !state.pendingKey) focusBuffer(state, buffer.key, "reader");
      restored = true;
    }),
  );
  return restored;
}

async function releaseClosedBuffer(
  projectId: string,
  buffer: FileBuffer,
  index: number,
  discard: boolean,
): Promise<FilesBufferRelease> {
  try {
    if (discard) await discardEditorDocument(projectId, buffer);
    return (await evictEditorDocument(buffer.documentId)) ? "released" : "superseded";
  } catch {
    return restoreClosedBuffer(projectId, buffer, index) ? "restored" : "superseded";
  }
}

/** Failed document release restores the closed tab and its draft. */
export function closeFilesBuffer(
  projectId: string,
  key: FileBufferKey,
  options: { discardDraft?: boolean } = {},
): Promise<FilesBufferRelease> | null {
  ensureProject(projectId);
  // Materialize typed input before the buffer leaves the store.
  flushFilesDraftSyncFor(projectId, key);
  const state = filesStore[projectId]!;
  if (options.discardDraft && state.byKey[key]?.dirty && !state.byKey[key]?.documentId) {
    discardFilesBufferDraft(projectId, key);
  }
  const buffer = state.byKey[key];
  if (!buffer) return null;
  if (buffer.dirty && !buffer.jobId && !buffer.documentId) return null;
  const discard = options.discardDraft === true && buffer.dirty && buffer.documentId != null;
  if (buffer.dirty && buffer.documentId && !discard) scheduleEditorDocumentDraft(projectId, buffer);
  const index = state.order.indexOf(key);
  const snapshot: FileBuffer = { ...unwrap(buffer), closeError: null };
  setFilesStore(projectId, produce((current) => detachBuffer(current, key)));

  const closing = closingBuffers.get(projectId) ?? new Set<ClosingBuffer>();
  closingBuffers.set(projectId, closing);
  const entry: ClosingBuffer = {
    buffer: snapshot,
    released: releaseClosedBuffer(projectId, snapshot, index, discard).finally(() => {
      closing.delete(entry);
      if (closing.size === 0 && closingBuffers.get(projectId) === closing) {
        closingBuffers.delete(projectId);
      }
    }),
  };
  closing.add(entry);
  return entry.released;
}

/** Closes many keys, stopping at the first that cannot close; the aim follows each. */
export function closeFilesBuffers(
  projectId: string,
  keys: readonly FileBufferKey[],
  options: { discardDraft?: boolean } = {},
): boolean {
  for (const key of keys) {
    if (!closeFilesBuffer(projectId, key, options)) return false;
  }
  return true;
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

export function retargetFilesBufferStoreUnderPath(
  projectId: string,
  rootId: string,
  fromPath: string,
  toPath: string,
): void {
  ensureProject(projectId);
  setFilesStore(
    projectId,
    produce((state) => {
      const updates: { oldKey: FileBufferKey; buf: FileBuffer }[] = [];
      for (const key of state.order) {
        const buf = state.byKey[key];
        if (!buf || buf.rootId !== rootId) continue;
        if (buf.jobId) continue; // Worker views retain their address.
        const nextPath = retargetPathUnderPath(buf.path, fromPath, toPath);
        if (nextPath == null) continue;
        updates.push({ oldKey: key, buf: { ...buf, path: nextPath } });
      }
      for (const { oldKey, buf } of updates) {
        const newKey = fileBufferKey(buf.rootId, buf.path, buf.jobId, buf.fileId ?? undefined);
        const next: FileBuffer = {
          ...buf,
          key: newKey,
          name: fileDisplayName(buf.path),
        };
        if (newKey === oldKey) {
          // Retained editor panes hold this file identity across path changes.
          Object.assign(state.byKey[oldKey]!, next);
          continue;
        }
        const idx = state.order.indexOf(oldKey);
        if (idx >= 0) state.order[idx] = newKey;
        delete state.byKey[oldKey];
        state.byKey[newKey] = next;
        if (state.activeKey === oldKey) state.activeKey = newKey;
        if (state.pendingKey === oldKey) state.pendingKey = newKey;
      }
    }),
  );
}

export function closeFilesBuffersUnderPath(
  projectId: string,
  rootId: string,
  path: string,
): boolean {
  ensureProject(projectId);
  const state = filesStore[projectId];
  if (!state) return true;
  const keys: FileBufferKey[] = [];
  for (const key of state.order) {
    const buf = state.byKey[key];
    if (!buf || buf.rootId !== rootId) continue;
    if (buf.path === path || (path !== "." && buf.path.startsWith(`${path}/`))) {
      keys.push(key);
    }
  }
  return closeFilesBuffers(projectId, keys);
}

/** Root detach waits for durable drafts and pending closes. */
export async function prepareFilesRootDetach(
  projectId: string,
  rootId: string,
): Promise<void> {
  ensureProject(projectId);
  // Failed closes restore buffers before their drafts are flushed.
  await Promise.all(
    [...(closingBuffers.get(projectId) ?? [])]
      .filter((closing) => closing.buffer.rootId === rootId)
      .map((closing) => closing.released),
  );
  const state = filesStore[projectId];
  if (!state) return;
  const documentIds: string[] = [];
  for (const key of state.order) {
    const buffer = state.byKey[key];
    if (!buffer || buffer.rootId !== rootId) continue;
    flushFilesDraftSyncFor(projectId, key);
    const current = filesStore[projectId]?.byKey[key];
    if (current?.dirty && !current.jobId && !current.documentId) {
      throw new Error(`Couldn't preserve the unsaved draft for ${current.path}.`);
    }
    if (current?.documentId) documentIds.push(current.documentId);
  }
  await Promise.all(documentIds.map((id) => flushEditorDocumentDraft(id)));
}

/** Successful root detach retires its local buffers. */
export function dropFilesBuffersForRoot(projectId: string, rootId: string): void {
  const state = filesStore[projectId];
  if (!state) return;
  const keys = state.order.filter((key) => state.byKey[key]?.rootId === rootId);
  retireFilesBuffers(projectId, keys);
}

/** Buffer identity survives project registry removal. */
export function dropFilesBuffersForProject(projectId: string): void {
  const state = filesStore[projectId];
  if (!state) return;
  batch(() => {
    retireFilesBuffers(projectId, [...state.order]);
    setFilesStore(projectId, emptyFilesState());
  });
}

function retireFilesBuffers(projectId: string, keys: readonly FileBufferKey[]): void {
  const state = filesStore[projectId];
  if (!state) return;
  for (const key of keys) {
    cancelFilesDraftSyncFor(projectId, key);
    forgetEditorDocument(state.byKey[key]?.documentId);
  }
  setFilesStore(
    projectId,
    produce((current) => {
      const removed = new Set(keys);
      current.order = current.order.filter((key) => !removed.has(key));
      for (const key of keys) delete current.byKey[key];
      if (current.pendingKey && removed.has(current.pendingKey)) {
        current.pendingKey = null;
      }
      if (current.activeKey && removed.has(current.activeKey)) {
        current.activeKey = current.order[0] ?? null;
      }
    }),
  );
}

/** Dirty buffers that would be closed by deleting `path`. */
export function dirtyFilesBuffersUnderPath(
  projectId: string,
  rootId: string,
  path: string,
): FileBuffer[] {
  const state = filesStore[projectId];
  if (!state) return [];
  const out: FileBuffer[] = [];
  for (const key of state.order) {
    const buf = state.byKey[key];
    if (!buf || buf.rootId !== rootId || !buf.dirty) continue;
    if (path === "." || buf.path === path || buf.path.startsWith(`${path}/`)) {
      out.push(buf);
    }
  }
  return out;
}

export function resetProjectFilesForTests(): void {
  resetFilesDraftSyncForTests();
  closingBuffers.clear();
  setFilesStoreRaw(
    produce((all) => {
      for (const k of Object.keys(all)) delete all[k];
    }),
  );
}

export function setWalkGitSelection(projectId: string, key: FileBufferKey, selection: GitReviewSelection): void {
  setFilesStore(projectId, produce((state) => {
    const buffer = state.byKey[key];
    if (buffer?.kind === "walk") buffer.walkGitSelection = selection;
  }));
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
