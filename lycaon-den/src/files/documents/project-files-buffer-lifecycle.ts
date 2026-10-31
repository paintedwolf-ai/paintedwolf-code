import { batch } from "solid-js";
import { produce, unwrap } from "solid-js/store";
import {
  closeTargetKeys,
  orderWithPinnedGroup,
  type CloseFamily,
} from "../tabs/files-tab-strip.ts";
import {
  fileBufferKey,
  fileDisplayName,
  neighborKeyAfterClose,
  retargetPathUnderPath,
  type FileBufferKey,
} from "../components/project-files-model.ts";
import {
  flushFilesDraftSyncFor,
  cancelFilesDraftSyncFor,
  resetFilesDraftSyncForTests,
} from "./files-draft-sync.ts";
import {
  discardEditorDocument,
  evictEditorDocument,
  scheduleEditorDocumentDraft,
  flushEditorDocumentDraft,
  forgetEditorDocument,
  preserveEditorDocumentDraft,
  editorReplica,
} from "./editor-document.ts";
import {
  type FileBuffer,
  type GitReviewSelection,
  type ProjectFilesState,
  emptyFilesState,
  ensureProject,
  filesStore,
  nextBufferPayloadGeneration,
  projectFilesState,
  setFilesStore,
  setFilesStoreRaw,
} from "./files-buffer-state.ts";
import {
  bufferKeyAtAddress,
  focusBuffer,
  openFilesBuffer,
} from "./project-files-buffers.ts";
import { discardFilesBufferDraft } from "./project-files-buffer-io.ts";

/** Lease released, reused by a reopen, or restored with the tab after failure. */
export type FilesBufferRelease = "released" | "superseded" | "restored";

export const FILES_CLOSE_FAILED_MESSAGE =
  "Couldn't close this tab safely, so it was kept open with its draft. Close it again to retry.";

type ClosingBuffer = {
  buffer: FileBuffer;
  released: Promise<FilesBufferRelease>;
};

const closingBuffers = new Map<string, Set<ClosingBuffer>>();

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

/** Resolves the tabs affected by a close action. */
export function filesCloseTargetKeys(
  projectId: string,
  family: CloseFamily,
  focusKey: FileBufferKey | null,
): FileBufferKey[] {
  const state = projectFilesState(projectId);
  return closeTargetKeys(family, state.order, state.byKey, focusKey);
}

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
