/** Drawer requests carry a source reader and an initial file-change summary. */

import { createSignal } from "solid-js";

export type DiffViewerRequest = {
  projectId?: string;
  path: string;
  reader: import("../../api/source-reader.ts").SourceReaderAccess;
  /** Known before the reader summarizes, so chrome renders with the first frame. */
  change: import("../../components/source/reader/source-reader-change.ts").SourceReaderChange;
  /** Resolved by Shell when mounting the drawer; enables Reveal / Open in editor. */
  absolutePath?: string;
  rootId?: string;
};

const [viewerRequest, setViewerRequest] =
  createSignal<DiffViewerRequest | null>(null);

export function diffViewerRequest(): DiffViewerRequest | null {
  return viewerRequest();
}

export function openDiffViewer(req: DiffViewerRequest): void {
  setViewerRequest({ ...req });
}

export function closeDiffViewer(): void {
  setViewerRequest(null);
}

export function resetDiffViewerForTests(): void {
  setViewerRequest(null);
}
