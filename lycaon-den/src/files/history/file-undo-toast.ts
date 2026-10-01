import type { FileUndo } from "../commands/file-mutations.ts";

export const FILE_UNDO_TOAST_MS = 8_000;

export type FileUndoToast = {
  id: string;
  projectId: string;
  label: string;
  undo: FileUndo[];
};

type Entry = { toast: FileUndoToast; timer: ReturnType<typeof setTimeout> };

/** One active Undo per project; a new action replaces only its own project's. */
const byProject = new Map<string, Entry>();
const listeners = new Set<(projectId: string) => void>();

export function subscribeFileUndoToast(
  fn: (projectId: string) => void,
): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function notify(projectId: string) {
  for (const fn of listeners) fn(projectId);
}

export function getFileUndoToast(projectId: string): FileUndoToast | null {
  const id = projectId.trim();
  if (!id) return null;
  return byProject.get(id)?.toast ?? null;
}

let toastSeq = 0;

export function showFileUndoToast(toast: Omit<FileUndoToast, "id">): FileUndoToast {
  const projectId = toast.projectId.trim();
  const existing = byProject.get(projectId);
  if (existing) clearTimeout(existing.timer);
  toastSeq += 1;
  const next: FileUndoToast = {
    ...toast,
    projectId,
    id: `file-undo-${Date.now()}-${toastSeq}`,
  };
  const timer = setTimeout(() => {
    if (byProject.get(projectId)?.toast.id === next.id) {
      byProject.delete(projectId);
      notify(projectId);
    }
  }, FILE_UNDO_TOAST_MS);
  byProject.set(projectId, { toast: next, timer });
  notify(projectId);
  return next;
}

export function dismissFileUndoToast(projectId: string, id?: string): void {
  const key = projectId.trim();
  const held = byProject.get(key);
  if (!held) return;
  if (id && held.toast.id !== id) return;
  clearTimeout(held.timer);
  byProject.delete(key);
  notify(key);
}

export function resetFileUndoToastForTests(): void {
  for (const entry of byProject.values()) clearTimeout(entry.timer);
  byProject.clear();
}
