import type { FileBufferKind } from "../documents/project-files-buffer-kind.ts";

/** Stage chrome configuration for a pane based on address, document, and page facts. */
export type FilesPaneChrome = {
  /** A place in the tree: the breadcrumb, and Reveal in tree. */
  address: boolean;
  /** A readable document: the status bar. */
  document: boolean;
  /** Its own heading in place of the toolbar; every other pane has the toolbar. */
  page: boolean;
};

const PANE_CHROME: Record<FileBufferKind, FilesPaneChrome> = {
  // Files, presented as documents.
  text: { address: true, document: true, page: false },
  image: { address: true, document: true, page: false },
  diff: { address: true, document: true, page: false },
  // A file the editor cannot open as a document.
  info: { address: true, document: false, page: false },
  // Recorded chat content: a document with nowhere in the tree.
  chat: { address: false, document: true, page: false },
  // Pages over a set of changes.
  walk: { address: false, document: false, page: true },
  trust: { address: false, document: false, page: true },
  // One reader document of every changed file.
  diffs: { address: false, document: true, page: true },
};

export function filesPaneChrome(kind: FileBufferKind): FilesPaneChrome {
  return PANE_CHROME[kind];
}
