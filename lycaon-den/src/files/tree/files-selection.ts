import { createEffect, createMemo, createSignal, untrack } from "solid-js";
import { editorRevealInTreePref } from "../../settings/editor/editor-prefs.ts";
import { hasFilesTreeAddress } from "./files-tree-reveal.ts";
import type { FilesTreeEntrySelection } from "./files-tree-context.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";

type FilesSelectionOptions = Pick<FilesScope, "filesRoots" | "state"> & {
  setFilterQuery: (query: string) => unknown;
};

export function createFilesSelection(options: FilesSelectionOptions) {
  const { filesRoots, state, setFilterQuery } = options;
  const [selectedEntry, setSelectedEntry] =
    createSignal<FilesTreeEntrySelection | null>(null);
  let initialRootSelectionEstablished = false;
  createEffect(() => {
    const firstRoot = filesRoots()[0];
    if (!firstRoot || initialRootSelectionEstablished) return;
    initialRootSelectionEstablished = true;
    if (state().activeKey || selectedEntry()) return;
    setSelectedEntry({ rootId: firstRoot.id, path: ".", kind: "folder" });
  });
  const selectedFolderContext = createMemo(() => {
    const selection = selectedEntry();
    if (selection?.kind !== "folder") return null;
    const root = filesRoots().find((entry) => entry.id === selection.rootId);
    return root ? { root, path: selection.path } : null;
  });
  const selectedProjectRoot = createMemo(() => {
    const selection = selectedFolderContext();
    return selection?.path === "." ? selection.root : null;
  });
  const selectedFolder = createMemo(() => {
    const selection = selectedFolderContext();
    return selection && selection.path !== "." ? selection : null;
  });
  const selectFileBuffer = (key: FileBufferKey) => {
    const buffer = state().byKey[key];
    if (!buffer) return;
    setSelectedEntry({
      rootId: buffer.rootId,
      path: buffer.path,
      kind: "file",
    });
  };
  let selectedAimRevision = -1;
  createEffect(() => {
    if (!editorRevealInTreePref()) {
      selectedAimRevision = -1;
      return;
    }
    const revision = state().aimRevision;
    if (revision === selectedAimRevision) return;
    selectedAimRevision = revision;
    const key = state().pendingKey ?? state().activeKey;
    const buffer = key ? state().byKey[key] : undefined;
    if (!buffer || !hasFilesTreeAddress(buffer)) return;
    setFilterQuery("");
    untrack(() => selectFileBuffer(buffer.key));
  });
  return { selectedEntry, setSelectedEntry, selectedProjectRoot, selectedFolder };
}
