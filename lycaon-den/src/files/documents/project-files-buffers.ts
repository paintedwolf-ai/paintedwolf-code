import { produce } from "solid-js/store";
import { clampDragIndexWithinGroup, orderWithPinnedGroup } from "../tabs/files-tab-strip.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import {
  type AimOrigin,
  bufferKeyAtAddress,
  ensureProject,
  filesBufferBase,
  filesBufferKeyAtAddress,
  filesBufferText,
  focusBuffer,
  projectFilesState,
  setFilesStore,
  settleBufferPresentation,
} from "./files-buffer-state.ts";

export {
  bufferKeyAtAddress,
  filesBufferBase,
  filesBufferKeyAtAddress,
  filesBufferText,
  focusBuffer,
  settleBufferPresentation,
};
export * from "./project-files-buffer-io.ts";
export * from "./project-files-buffer-lifecycle.ts";

/** Present an aimed buffer whose content is a past version, not a source read. */
export function presentFilesBufferVersion(projectId: string, key: FileBufferKey): void {
  setFilesStore(projectId, produce((state) => {
    if (!state.byKey[key]) return;
    settleBufferPresentation(state, key);
  }));
}

/** The buffer the reader is aimed at, presented or still arriving. */
export function focusedFilesBufferKey(projectId: string): FileBufferKey | null {
  const state = projectFilesState(projectId);
  return state.pendingKey ?? state.activeKey;
}

/** Promotes a transient buffer in place. */
export function promoteFilesBuffer(projectId: string, key: FileBufferKey): void {
  setFilesStore(projectId, produce((state) => {
    const buf = state.byKey[key];
    if (buf?.preview) buf.preview = false;
  }));
}

/** Composed page names follow their current comparison. */
export function renameFilesBuffer(projectId: string, key: FileBufferKey, name: string): void {
  setFilesStore(projectId, produce((state) => {
    const buffer = state.byKey[key];
    if (buffer && buffer.name !== name) buffer.name = name;
  }));
}

export function setFilesBufferPinned(projectId: string, key: FileBufferKey, pinned: boolean): void {
  setFilesStore(projectId, produce((state) => {
    const buf = state.byKey[key];
    if (!buf) return;
    buf.pinned = pinned;
    if (pinned) buf.preview = false;
    state.order = orderWithPinnedGroup(state.order, state.byKey);
  }));
}

export function toggleFilesBufferPinned(projectId: string, key: FileBufferKey): void {
  const buf = projectFilesState(projectId).byKey[key];
  if (!buf) return;
  setFilesBufferPinned(projectId, key, !buf.pinned);
}

/** The editor consumed the one-shot line and focus target. */
export function clearFilesBufferReveal(projectId: string, key: FileBufferKey): void {
  setFilesStore(projectId, produce((state) => {
    const buf = state.byKey[key];
    if (!buf) return;
    buf.revealLine = null;
    buf.revealColumn = null;
    buf.revealEndLine = null;
    buf.revealFocus = false;
  }));
}

/** The editor consumed the one-shot condensed-context request. */
export function clearFilesBufferCondense(projectId: string, key: FileBufferKey): void {
  setFilesStore(projectId, produce((state) => {
    const buf = state.byKey[key];
    if (buf) buf.condenseContext = false;
  }));
}

export function setFilesActiveBuffer(projectId: string, key: FileBufferKey, origin: AimOrigin = "reader"): void {
  ensureProject(projectId);
  setFilesStore(projectId, produce((state) => {
    if (!state.byKey[key]) return;
    focusBuffer(state, key, origin);
  }));
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

export function moveFilesBuffer(projectId: string, key: FileBufferKey, toIndex: number): void {
  setFilesStore(projectId, produce((state) => {
    const from = state.order.indexOf(key);
    if (from < 0) return;
    const to = clampDragIndexWithinGroup(state.order, state.byKey, from, toIndex);
    if (to === from) return;
    state.order.splice(from, 1);
    state.order.splice(to, 0, key);
    // Drag of a transient tab promotes it.
    const buf = state.byKey[key];
    if (buf?.preview) buf.preview = false;
  }));
}
