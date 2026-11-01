import { type ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { createStore, produce } from "solid-js/store";
import type { ProjectSourceReadResponse, SourceEncoding } from "../../api/types.ts";
import { type IndentInfo } from "../../components/source/editor/indent-detect.ts";
import type { EditorConfigProps } from "../../components/source/editor/editorconfig.ts";
import { type EolKind } from "../../components/source/editor/eol.ts";
import { type FileBufferKind } from "./project-files-buffer-kind.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FileVersionView } from "../history/file-version.ts";
import type { ContentNotice } from "../source/source-content-availability.ts";
import type { FileDocumentOpening } from "./file-document-opening.ts";
import type { WalkGroupStep } from "../walk/walk-model.ts";
import { type DiffsAddress } from "../review/diffs-address.ts";
import type { EditorEditingBlock } from "./editor-draft.ts";

let payloadGeneration = 0;

export type FileBufferContent =
  | { state: "unloaded" }
  | { state: "source"; text: string; base: string }
  | { state: "document"; generation: number }
  | { state: "suspended"; generation: number };

export type BufferOpenIntent = "transient" | "permanent";

export type GitReviewSelection = { movement?: boolean; parent?: number };

export type FileDiffPreview = {
  version: FileVersionView;
  title?: string;
  detail?: string;
  notice?: ContentNotice;
  walkStep?: WalkGroupStep;
  walkSelection?: GitReviewSelection;
};

export type FileBuffer = {
  key: FileBufferKey;
  rootId: string;
  rootLabel: string;
  /** Host file identity, when available. */
  fileId: string | null;
  /** Root-relative slash path; non-file tabs have no source address. */
  path: string;
  name: string;
  editorParticipants?: number;
  editorResolving?: boolean;
  editorEditingBlock?: EditorEditingBlock | null;
  /** The host's last refusal of this window's edits; cleared by the next accepted delivery. */
  editorRefusal?: string | null;
  editorSynchronization?: "preserving" | "pending" | "accepted" | "error";
  editorOpening?: FileDocumentOpening;
  /** Host document identity for editable text. */
  documentId: string | null;
  documentRevision: number | null;
  /** Worker whose live overlay serves this read-only view. */
  jobId?: string;
  /** Scroll and emphasis target, cleared after presentation. */
  revealLine?: number | null;
  /** 1-based column paired with revealLine (quick-open `:line:col`). */
  revealColumn?: number | null;
  /** Inclusive end line when revealing a selection range. */
  revealEndLine?: number | null;
  /** Keyboard attention on presentation, cleared with the reveal target. */
  revealFocus?: boolean;
  /** Fold unchanged runs once the selected comparison loads. */
  condenseContext?: boolean;
  loading: boolean;
  loadError: string | null;
  /** Current-file presence established by the source read, independent of text hashes. */
  sourcePresent: boolean;
  /** The host document holds a draft for a path whose file left the disk. */
  absent?: boolean;
  deleted?: ProjectSourceReadResponse["deleted"];
  content: FileBufferContent;
  payloadGeneration: number;
  /** Wire EOL restored on save. */
  eol: EolKind;
  /** EOL selected when baseSha256 was loaded or last saved. */
  baseEol: EolKind;
  /** Mixed endings were detected on load and will normalize on save. */
  mixedEol: boolean;
  /** Whether the base bytes contained mixed endings. */
  baseMixedEol: boolean;
  /** Indent detected from the loaded file; undefined ⇒ use prefs. */
  detectedIndent: IndentInfo | undefined;
  /** Session-only override from the status-bar chip menu. */
  indentOverride: IndentInfo | undefined;
  /** Resolved `.editorconfig` props for this path (undefined = none / pending). */
  editorConfig: EditorConfigProps | undefined;
  baseSha256: string | null;
  sizeBytes: number;
  kind: FileBufferKind;
  mime: string | null;
  /** Host grammar bound to the path whose metadata was read. */
  language?: { path: string; name: string };
  mtime: string | null;
  overLimit: boolean;
  /** Exact UTF-8 or UTF-16 representation returned by the host read. */
  encoding: SourceEncoding | null;
  /** Write access reported by the host; null until loaded. */
  writable: boolean | null;
  /** Encoding label from an unsupported source read. */
  unsupportedEncodingDetected: string | null;
  dirty: boolean;
  /** Monotonic edit generation for rejecting stale completions. */
  editRevision: number;
  /** At most one preview tab per project. */
  preview: boolean;
  pinned: boolean;
  /** Host-reported mismatch between the saved base and disk. */
  diverged: boolean;
  /** Retained state holding an agent edit disk never received. */
  heldAgentVersionId: string | null;
  /** Error retained when closing the tab fails. */
  closeError: string | null;
  /** Immutable version rendered by the version editor. */
  diffPreview?: FileDiffPreview;
  /** Group step presented by this Walk page. */
  walkStep?: WalkGroupStep;
  /** Comparison this diffs page reads. */
  diffs?: DiffsAddress;
  walkGitSelection?: GitReviewSelection;
  chatContent?: ChatContentDocument;
};

/** Explicit navigation takes precedence over background presentation changes. */
export type AimOrigin = "reader" | "presentation";

export type ProjectFilesState = {
  rootBranches: Record<string, string>;
  order: FileBufferKey[];
  byKey: Record<FileBufferKey, FileBuffer>;
  /** The buffer the editor is presenting; moves only to one that has content. */
  activeKey: FileBufferKey | null;
  /** The buffer the reader asked for while it loads; the tab strip follows it. */
  pendingKey: FileBufferKey | null;
  /** Provenance of the current aim (`pendingKey ?? activeKey`). */
  aimOrigin: AimOrigin | null;
  /** Each admitted navigation, including another click on the same file. */
  aimRevision: number;
};

/** Fresh containers per project namespace. */
export function emptyFilesState(): ProjectFilesState {
  return { rootBranches: {}, order: [], byKey: {}, activeKey: null, pendingKey: null, aimOrigin: null, aimRevision: 0 };
}

/** Read-only stand-in for a project with no buffers yet. */
const EMPTY: ProjectFilesState = Object.freeze(emptyFilesState());

export const [filesStore, setFilesStoreRaw] = createStore<
  Record<string, ProjectFilesState>
>({});
export function setFilesStore(
  projectId: string,
  update: ProjectFilesState | ((state: ProjectFilesState) => ProjectFilesState),
): void {
  setFilesStoreRaw(projectId, update);
}

export function projectFilesState(projectId: string): ProjectFilesState {
  return filesStore[projectId] ?? EMPTY;
}

export function openFilesProjectIds(): string[] {
  return Object.keys(filesStore);
}

export function ensureProject(projectId: string): void {
  if (!filesStore[projectId]) {
    setFilesStore(projectId, emptyFilesState());
  }
}

/** Releases bodies while preserving tab identity and host status. */
export function suspendFilesBufferContent(projectId: string, key: string, resume = false): void {
  setFilesStore(projectId, produce(state => {
    const buffer = state.byKey[key];
    if (!buffer) return;
    if (buffer.content.state !== "suspended") {
      buffer.content = { state: "suspended", generation: buffer.editRevision };
      buffer.payloadGeneration = ++payloadGeneration;
    }
    buffer.loading = resume;
    buffer.editorOpening = undefined;
  }));
}

export function filesBufferNeedsBody(buffer: FileBuffer): boolean {
  return buffer.content.state === "unloaded" || buffer.content.state === "suspended";
}

/** Cold tabs follow accepted status while their local recovery stays on disk. */
export function applyFilesBufferDocumentStatus(projectId: string, document: import("../../api/types.ts").EditorDocumentStatus | import("../../api/types.ts").EditorDocumentEvent | import("../../api/types.ts").EditorDocument, synchronized = false): void {
  setFilesStore(projectId, produce(state => {
    for (const buffer of Object.values(state.byKey)) {
      if (buffer.documentId !== document.id || !filesBufferNeedsBody(buffer) || (buffer.documentRevision ?? 0) > document.revision) continue;
      buffer.documentRevision = document.revision;
      buffer.diverged = document.diverged;
      buffer.absent = document.absent;
      buffer.sourcePresent = !document.absent;
      buffer.heldAgentVersionId = document.held_agent_version_id ?? null;
      if (synchronized) buffer.editorSynchronization = "accepted";
      buffer.dirty = document.dirty || buffer.editorSynchronization !== "accepted";
      buffer.sizeBytes = document.size_bytes;
      if (buffer.editorSynchronization === "accepted") {
        buffer.baseSha256 = document.base_sha256;
        buffer.eol = document.eol;
        buffer.baseEol = document.base_eol;
        buffer.mixedEol = document.mixed_eol;
        buffer.baseMixedEol = document.base_mixed_eol;
      }
    }
  }));
}

export function nextBufferPayloadGeneration(): number { return ++payloadGeneration; }

/** Text an open response carried inside its editor document, with the host's line-ending facts. */
export type LoadedDocumentText = { text: string; eol: EolKind; mixed: boolean; dirty: boolean; absent?: boolean };

type ReplicaSnapshot = { currentText: string; baseText: string; dirty: boolean };
let replicaReader: ((documentId: string | null | undefined) => ReplicaSnapshot | undefined) | undefined;

export function registerFilesBufferReplicaReader(
  reader: (documentId: string | null | undefined) => ReplicaSnapshot | undefined,
): void {
  replicaReader = reader;
}

/** Text materialization is deferred until read. */
export function filesBufferText(buffer: Pick<FileBuffer, "content" | "documentId" | "editRevision">): string {
  void buffer.editRevision;
  if (buffer.content.state === "source") return buffer.content.text;
  return replicaReader?.(buffer.documentId)?.currentText ?? "";
}

export function filesBufferBase(buffer: Pick<FileBuffer, "content" | "documentId">): string {
  if (buffer.content.state === "source") return buffer.content.base;
  return replicaReader?.(buffer.documentId)?.baseText ?? "";
}

export function bufferKeyAtAddress(
  state: ProjectFilesState, rootId: string, path: string, jobId?: string,
): FileBufferKey | null {
  const wantedJob = jobId?.trim() ?? "";
  const matches = (candidate: FileBufferKey) => {
    const buffer = state.byKey[candidate];
    return buffer != null && buffer.rootId === rootId && buffer.path === path && (buffer.jobId ?? "") === wantedJob;
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
  projectId: string, rootId: string, path: string, jobId?: string,
): FileBufferKey | null {
  return bufferKeyAtAddress(projectFilesState(projectId), rootId, path, jobId);
}

/** Aim the editor at `key`, presenting it once it has content. */
export function focusBuffer(state: ProjectFilesState, key: FileBufferKey, origin: AimOrigin): void {
  const aimedKey = state.pendingKey ?? state.activeKey;
  const aimed = aimedKey ? state.byKey[aimedKey] : undefined;
  // Pending reader navigation takes precedence over presentation changes.
  if (origin === "presentation" && state.aimOrigin === "reader" && aimed?.loading && aimedKey !== key) return;
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
export function settleBufferPresentation(state: ProjectFilesState, key: FileBufferKey): void {
  if (state.pendingKey !== key) return;
  state.activeKey = key;
  state.pendingKey = null;
}

export function markFilesDocumentUnavailable(projectId: string, documentId: string): void {
  setFilesStore(projectId, produce(state => {
    for (const buffer of Object.values(state.byKey)) {
      if (buffer.documentId !== documentId || !filesBufferNeedsBody(buffer)) continue;
      buffer.loadError = "This document is no longer available from the host. Preserved edits remain in local storage.";
    }
  }));
}
