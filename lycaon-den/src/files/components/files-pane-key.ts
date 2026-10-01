import { FILE_BUFFER_KINDS, type FileBufferKind } from "../documents/project-files-buffer-kind.ts";
import type { FileBufferKey } from "./project-files-model.ts";

const KINDS = new Set<FileBufferKind>(FILE_BUFFER_KINDS);

/** Primitive keys preserve panes across reactive object allocations. */
export function filesPaneKey(
  key: FileBufferKey,
  kind: FileBufferKind,
): string {
  return `${key}\0${kind}`;
}

export function parseFilesPaneKey(
  pane: string,
): { key: FileBufferKey; kind: FileBufferKind } | null {
  const sep = pane.lastIndexOf("\0");
  if (sep <= 0) return null;
  const kind = pane.slice(sep + 1) as FileBufferKind;
  if (!KINDS.has(kind)) return null;
  const key = pane.slice(0, sep);
  return { key, kind };
}
