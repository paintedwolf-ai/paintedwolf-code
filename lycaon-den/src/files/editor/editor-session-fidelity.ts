import * as Y from "yjs";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { editorReplica } from "../documents/editor-document.ts";
import { encodeUpdate, decodeUpdate } from "../documents/document-outbox.ts";
import { openFilesProjectIds, projectFilesState } from "../documents/files-buffer-state.ts";
import type { DenEditorViewStateEntry, DenReaderViewState } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
import {
  EditorViewStateMap,
  captureEditorViewState,
  editorViewStateKey,
  restoreEditorViewState,
} from "../../components/source/editor/editor-view-state.ts";
import {
  loadClosedBufferRingStore,
  pushClosedBuffer,
  toClosedBufferRingStore,
  closedEntryFromBuffer,
} from "../tabs/closed-buffer-ring.ts";
import type { EditorView } from "@codemirror/view";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { explicitSourceEncoding } from "../source/files-source-read.ts";

const viewStates = new EditorViewStateMap();
const restoring = new Set<string>();
let persistTimer: ReturnType<typeof setTimeout> | undefined;
let dirty = false;

const PERSIST_DEBOUNCE_MS = 1000;

function isEditorViewStateRestoring(key: string): boolean {
  return restoring.has(key);
}

function markEditorViewStateRestoring(key: string, on: boolean): void {
  if (on) restoring.add(key);
  else restoring.delete(key);
}

export function scheduleSessionFidelityPersist(): void {
  dirty = true;
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    void flushSessionFidelityToDisk().catch(() => undefined);
  }, PERSIST_DEBOUNCE_MS);
}

/** The caller persists the patch after pending state is cleared. */
export function takeSessionFidelityPatch(): {
  editorViewState: ReturnType<EditorViewStateMap["toStore"]>;
  closedBufferRing: ReturnType<typeof toClosedBufferRingStore>;
} | null {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  if (!dirty) return null;
  dirty = false;
  return {
    editorViewState: viewStates.toStore(undefined, new Set(openFilesProjectIds().flatMap(id => Object.values(projectFilesState(id).byKey).flatMap(bufferViewStateKeys)))),
    closedBufferRing: toClosedBufferRingStore(),
  };
}

export async function flushSessionFidelityToDisk(): Promise<void> {
  const patch = takeSessionFidelityPatch();
  if (!patch) return;
  try {
    await persistAppState(patch);
  } catch (error) {
    scheduleSessionFidelityPersist();
    throw error;
  }
}

export function syncSessionFidelityFromSnapshot(): void {
  const snap = getAppStateSnapshot();
  viewStates.loadStore(snap.editorViewState);
  loadClosedBufferRingStore(snap.closedBufferRing);
}

export function captureBufferViewState(
  buf: FileBuffer,
  view: EditorView | undefined | null,
): DenEditorViewStateEntry | null {
  if (!view || buf.jobId || isEditorViewStateRestoring(buf.key)) return null;
  const sha = (buf.baseSha256 ?? "").trim();
  if (!sha) return null;
  const record = captureEditorViewState(view, sha);
  if (!record) return null;
  const replica = editorReplica(buf.documentId);
  if (replica) {
    const anchor = (position: number) => encodeUpdate(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(replica.text, position)));
    record.document = { id: replica.accepted.id, epoch: replica.accepted.epoch,
      selections: view.state.selection.ranges.map(range => ({ anchor: anchor(range.anchor), head: anchor(range.head) })),
      folds: record.folds.map(fold => ({ from: anchor(fold.from), to: anchor(fold.to) })) };
  }
  storeBufferViewState(buf, record);
  scheduleSessionFidelityPersist();
  return record;
}

function lookupBufferViewState(
  buf: Pick<FileBuffer, "rootId" | "path" | "jobId" | "baseSha256" | "documentId">,
): DenEditorViewStateEntry | undefined {
  if (buf.jobId) return undefined;
  const entry = bufferViewStateKeys(buf).map(key => viewStates.get(key)).find(candidate => candidate != null);
  if (!entry) return undefined;
  const sha = (buf.baseSha256 ?? "").trim();
  const replica = editorReplica(buf.documentId);
  if (entry.document && replica && entry.document.id === replica.accepted.id && entry.document.epoch === replica.accepted.epoch) {
    try {
      const position = (anchor: string) => Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decodeUpdate(anchor)), replica.doc)?.index;
      const ranges: Array<{ anchor: number; head: number }> = [];
      for (const range of entry.document.selections) {
        const anchor = position(range.anchor), head = position(range.head);
        if (anchor === undefined || head === undefined) return undefined;
        ranges.push({ anchor, head });
      }
      const folds = entry.document.folds.flatMap(fold => {
        const from = position(fold.from), to = position(fold.to);
        return from !== undefined && to !== undefined && to > from ? [{ from, to }] : [];
      });
      const cursor = ranges[entry.mainSelection ?? 0];
      if (!cursor) return undefined;
      return { ...entry, sha, selections: ranges, cursor, folds };
    } catch { return undefined; }
  }
  // The buffer's own document is still being adopted; its relative positions arrive with the replica.
  if (entry.document && !replica && buf.documentId === entry.document.id) return undefined;
  // Otherwise the saved base still anchors the recorded offsets.
  if (!sha || sha !== entry.sha) return undefined;
  return entry;
}

export function tryRestoreBufferViewState(
  buf: FileBuffer,
  view: EditorView,
): boolean {
  if (buf.jobId) return false;
  const record = lookupBufferViewState(buf);
  if (!record) return false;
  markEditorViewStateRestoring(buf.key, true);
  try {
    return restoreEditorViewState(view, record, buf.baseSha256);
  } finally {
    markEditorViewStateRestoring(buf.key, false);
  }
}

export function rekeySessionFidelityUnderPath(
  rootId: string,
  fromPath: string,
  toPath: string,
): void {
  viewStates.rekeyUnderPath(rootId, fromPath, toPath);
  scheduleSessionFidelityPersist();
}

export function rememberClosedBufferForProject(
  projectId: string,
  buf: FileBuffer,
  view: EditorView | undefined | null,
): void {
  if (buf.jobId || isComposedBufferKind(buf.kind)) return;
  let viewState: DenEditorViewStateEntry | null = null;
  if (view && !isEditorViewStateRestoring(buf.key)) {
    viewState = captureEditorViewState(view, (buf.baseSha256 ?? "").trim());
  }
  if (!viewState) {
    viewState = lookupBufferViewState(buf) ?? null;
  }
  if (viewState) storeBufferViewState(buf, viewState);
  const entry = closedEntryFromBuffer({
    key: buf.key,
    rootId: buf.rootId,
    path: buf.path,
    rootLabel: buf.rootLabel,
    pinned: buf.pinned,
    decodeAs: explicitSourceEncoding(buf.encoding),
    jobId: buf.jobId,
    viewState,
  });
  if (!entry) return;
  pushClosedBuffer(projectId, entry);
  scheduleSessionFidelityPersist();
}

export function putBufferViewState(
  rootId: string,
  path: string,
  record: DenEditorViewStateEntry,
): void {
  storeBufferViewState({ rootId, path, documentId: record.document?.id ?? null }, record);
}

/**
 * A document key carries this window's relative positions for one host document;
 * the path key keeps the saved-base offsets that outlive the document identity.
 */
function bufferViewStateKeys(buffer: Pick<FileBuffer, "rootId" | "path" | "documentId">): string[] {
  const keys = [editorViewStateKey(buffer.rootId, buffer.path)];
  if (buffer.documentId) keys.unshift(`${clientIdentity()}\0${buffer.documentId}`);
  return keys;
}

function storeBufferViewState(buffer: Pick<FileBuffer, "rootId" | "path" | "documentId">, record: DenEditorViewStateEntry): void {
  for (const key of bufferViewStateKeys(buffer)) viewStates.set(key, record);
}

export function readerBufferViewState(buffer: FileBuffer, sha: string): DenReaderViewState | undefined {
  const saved = bufferViewStateKeys(buffer).map(key => viewStates.get(key)).find(candidate => candidate != null);
  return saved?.sha === sha ? saved.reader : undefined;
}

export function captureReaderBufferViewState(buffer: FileBuffer, sha: string, reader: DenReaderViewState): void {
  if (buffer.jobId || !sha) return;
  storeBufferViewState(buffer, { sha, reader, cursor: { anchor: 0, head: 0 }, scrollTop: 0, folds: [], capturedAt: Date.now() });
  scheduleSessionFidelityPersist();
}
