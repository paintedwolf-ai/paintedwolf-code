import { chatContentDocumentKey, type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { produce } from "solid-js/store";
import { defaultEol } from "../../components/source/editor/eol.ts";
import { isComposedBufferKind, type FileBufferKind } from "./project-files-buffer-kind.ts";
import {
  clampDragIndexWithinGroup,
  orderWithPinnedGroup,
} from "../tabs/files-tab-strip.ts";
import {
  fileBufferKey,
  fileDisplayName,
  diffsBufferKey,
  walkPageBufferKey,
  type FileBufferKey,
} from "../components/project-files-model.ts";
import type { WalkGroupStep } from "../walk/walk-model.ts";
import { diffsAddressKey, type DiffsAddress } from "../review/diffs-address.ts";
import {
  evictEditorDocument,
  retainEditorDocument,
  editorReplica,
} from "./editor-document.ts";
import {
  type AimOrigin,
  type BufferOpenIntent,
  type FileBuffer,
  type FileDiffPreview,
  type GitReviewSelection,
  type ProjectFilesState,
  ensureProject,
  nextBufferPayloadGeneration,
  projectFilesState,
  setFilesStore,
} from "./files-buffer-state.ts";

export * from "./project-files-buffer-io.ts";
export * from "./project-files-buffer-lifecycle.ts";

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

export function bufferKeyAtAddress(
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
export function focusBuffer(
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
export function settleBufferPresentation(
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
