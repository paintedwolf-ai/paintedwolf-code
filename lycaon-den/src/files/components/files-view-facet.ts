import type { Accessor } from "solid-js";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { offerViewFacet, type ViewFacet } from "../../shell/view-report.ts";

type TreeEntry = { rootId: string; path: string; kind: string } | null | undefined;

/** What the files stage shows: its editor or diff, and the tree's selected folder while the tree is shown. */
export function filesViewFacet(buffer: FileBuffer | null | undefined, treeShown: boolean, entry: TreeEntry): ViewFacet {
  const facet: ViewFacet = {};
  if (buffer?.kind === "text" && buffer.documentId) facet.editor = { document_id: buffer.documentId };
  else if (buffer?.kind === "diff" && buffer.path) facet.diff = { root_id: buffer.rootId, path: buffer.path };
  if (treeShown && entry?.kind === "folder" && entry.path && entry.path !== ".") {
    facet.folder = { root_id: entry.rootId, path: entry.path };
  }
  return facet;
}

export function offerFilesViewFacet(deps: {
  projectId: Accessor<string>;
  displayedBuffer: Accessor<FileBuffer | null | undefined>;
  treeShown: Accessor<boolean>;
  selectedEntry: Accessor<TreeEntry>;
}): void {
  offerViewFacet(deps.projectId, () => filesViewFacet(deps.displayedBuffer(), deps.treeShown(), deps.selectedEntry()));
}
