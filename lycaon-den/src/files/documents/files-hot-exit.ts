import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { documentOutbox } from "./document-outbox.ts";
/** Hot exit persists tab presentation. */
import { openFilesBuffer, setFilesActiveBuffer } from "./project-files-buffers.ts";
import { openFilesProjectIds, projectFilesState } from "./files-buffer-state.ts";
import { isComposedBufferKind } from "./project-files-buffer-kind.ts";
import { flushFilesDraftSync } from "./files-draft-sync.ts";
import { fileBufferKey, type FileBufferKey } from "../components/project-files-model.ts";
import type { DenFilesHotExitBuffer, DenFilesHotExitProject, DenFilesHotExitState } from "../../../shared/app-state-types.ts";
import { getAppStateSnapshot, persistAppState } from "../../store/app-state-snapshot.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { captureBufferViewState, takeSessionFidelityPatch, scheduleSessionFidelityPersist } from "../editor/editor-session-fidelity.ts";
import { preserveEditorDocumentOperations, preserveEditorDocumentDraft } from "./editor-document.ts";
import { explicitSourceEncoding } from "../source/files-source-read.ts";

const PERSIST_DEBOUNCE_MS = 1000;
let persistTimer: ReturnType<typeof setTimeout> | undefined;
const dirtyProjects = new Set<string>();
const suppressWrite = new Set<string>();

export function suppressFilesHotExitWrite(projectId: string): void {
  const id = projectId.trim();
  if (!id) return;
  suppressWrite.add(id);
  dirtyProjects.delete(id);
}
export function clearFilesHotExitSuppress(projectId: string): void { suppressWrite.delete(projectId.trim()); }

function snapshotProject(projectId: string): DenFilesHotExitProject | null {
  const state = projectFilesState(projectId);
  const buffers = state.order.flatMap((key): DenFilesHotExitBuffer[] => {
    const buf = state.byKey[key];
    if (!buf || buf.jobId || isComposedBufferKind(buf.kind)) return [];
    return [{ ...(buf.documentId ? { documentId: buf.documentId, clientId: clientIdentity() } : {}), rootId: buf.rootId, path: buf.path, kind: buf.kind, rootLabel: buf.rootLabel,
      ...(buf.pinned ? { pinned: true } : {}),
      ...(explicitSourceEncoding(buf.encoding) ? { decodeAs: explicitSourceEncoding(buf.encoding) } : {}),
      ...(buf.preview ? { preview: true } : {}) }];
  });
  if (!buffers.length) return null;
  const aimedKey = state.pendingKey ?? state.activeKey;
  const active = aimedKey == null ? undefined : state.byKey[aimedKey];
  const activeIndex = active?.jobId || (active && isComposedBufferKind(active.kind))
    ? -1
    : buffers.findIndex(
      (row) => row.rootId === active?.rootId && row.path === active.path,
    );
  return { buffers, ...(activeIndex >= 0 ? { activeIndex } : {}) };
}

export function scheduleFilesHotExitPersist(projectId: string): void {
  if (suppressWrite.has(projectId)) return;
  dirtyProjects.add(projectId);
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    void flushFilesHotExitToDisk().catch(() => {
      if (dirtyProjects.size) scheduleFilesHotExitPersist([...dirtyProjects][0]!);
    });
  }, PERSIST_DEBOUNCE_MS);
}

export async function flushFilesHotExitToDisk(): Promise<void> {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  flushFilesDraftSync();
  for (const projectId of openFilesProjectIds()) {
    for (const buffer of Object.values(projectFilesState(projectId).byKey)) {
      if (!buffer.dirty || buffer.jobId || buffer.kind !== "text") continue;
      if (!(await preserveEditorDocumentDraft(buffer))) {
        throw new Error(`Couldn't preserve the unsaved draft for ${buffer.path}.`);
      }
    }
  }
  await preserveEditorDocumentOperations();
  const projects = [...dirtyProjects].filter((id) => !suppressWrite.has(id));
  for (const projectId of projects) dirtyProjects.delete(projectId);
  for (const projectId of projects) {
    const state = projectFilesState(projectId);
    for (const key of state.order) {
      const buf = state.byKey[key];
      if (buf?.kind === "text" && !buf.jobId) captureBufferViewState(buf, getFilesEditorView(projectId, key));
    }
  }
  const byProject = { ...(getAppStateSnapshot().filesHotExit?.byProject ?? {}) };
  let hotExitChanged = false;
  for (const projectId of projects) {
    hotExitChanged = true;
    const entry = snapshotProject(projectId);
    if (entry) byProject[projectId] = entry;
    else delete byProject[projectId];
  }
  const fidelity = takeSessionFidelityPatch();
  if (!hotExitChanged && !fidelity) return;
  try {
    await persistAppState({
      ...(hotExitChanged
        ? {
            filesHotExit: Object.keys(byProject).length
              ? { byProject }
              : undefined,
          }
        : {}),
      ...(fidelity ?? {}),
    });
  } catch (error) {
    for (const projectId of projects) dirtyProjects.add(projectId);
    if (fidelity) scheduleSessionFidelityPersist();
    throw error;
  }
  // A committed tab inventory releases only this window's orphaned undo references.
  for (const projectId of projects) {
    const ids = new Set(byProject[projectId]?.buffers.map(buffer => buffer.documentId));
    for (const entry of await documentOutbox().list(projectId)) {
      if (entry.retainedClients?.includes(clientIdentity()) && !ids.has(entry.documentId)) {
        await documentOutbox().commit([], [], { document: entry, clientId: clientIdentity(), retained: false });
      }
    }
  }
  // Typing can continue while the presentation snapshot is being written.
  await preserveEditorDocumentOperations();
}

export function syncFilesHotExitFromSnapshot(): void { clearTimeout(persistTimer); persistTimer = undefined; dirtyProjects.clear(); suppressWrite.clear(); }
export function restoreFilesHotExitForProject(projectId: string, roots: ProjectRoot[]): void {
  if (projectFilesState(projectId).order.length) return;
  const entry = getAppStateSnapshot().filesHotExit?.byProject?.[projectId];
  if (!entry?.buffers?.length) return;
  let activeKey: FileBufferKey | null = null;
  for (const [index, row] of entry.buffers.entries()) {
    const root = roots.find((item) => item.id === row.rootId);
    // Explicit file opens take precedence over restored tabs.
    const key = openFilesBuffer(projectId, { rootId: row.rootId, rootLabel: root?.label ?? row.rootLabel ?? row.rootId,
      path: row.path, intent: row.preview ? "transient" : "permanent",
      documentId: row.documentId, kind: row.kind, decodeAs: row.decodeAs, pinned: row.pinned === true, preview: row.preview === true,
      origin: "presentation" });
    if (entry.activeIndex === index) activeKey = key;
  }
  const first = entry.buffers[0];
  if (!activeKey && first) activeKey = fileBufferKey(first.rootId, first.path);
  if (activeKey) setFilesActiveBuffer(projectId, activeKey, "presentation");
}

export function parseFilesHotExitState(raw: unknown): DenFilesHotExitState | undefined {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return undefined;
  const source = (raw as { byProject?: unknown }).byProject;
  if (typeof source !== "object" || source === null || Array.isArray(source)) return undefined;
  const byProject: Record<string, DenFilesHotExitProject> = {};
  for (const [projectId, value] of Object.entries(source)) { const parsed = parseProject(value); if (parsed) byProject[projectId] = parsed; }
  return Object.keys(byProject).length ? { byProject } : undefined;
}

function parseProject(value: unknown): DenFilesHotExitProject | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const row = value as Partial<DenFilesHotExitProject>;
  if (!Array.isArray(row.buffers)) return null;
  const buffers: DenFilesHotExitBuffer[] = [];
  let activeIndex: number | undefined;
  for (const [index, value] of row.buffers.entries()) {
    if (typeof value !== "object" || value === null || Array.isArray(value)) continue;
    const buf = value as Partial<DenFilesHotExitBuffer>;
    if (typeof buf.rootId !== "string" || typeof buf.path !== "string" ||
      (buf.kind !== "text" && buf.kind !== "image" && buf.kind !== "info")) continue;
    if (index === row.activeIndex) activeIndex = buffers.length;
    buffers.push({ ...(typeof buf.documentId === "string" ? { documentId: buf.documentId } : {}),
      ...(typeof buf.clientId === "string" ? { clientId: buf.clientId } : {}), rootId: buf.rootId, path: buf.path, kind: buf.kind,
      ...(typeof buf.rootLabel === "string" ? { rootLabel: buf.rootLabel } : {}),
      ...(buf.decodeAs === "utf-16le" || buf.decodeAs === "utf-16be" ? { decodeAs: buf.decodeAs } : {}),
      ...(buf.pinned === true ? { pinned: true } : {}), ...(buf.preview === true ? { preview: true } : {}) });
  }
  if (!buffers.length) return null;
  return { buffers, ...(activeIndex !== undefined ? { activeIndex } : {}) };
}
