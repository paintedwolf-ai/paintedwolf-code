import { Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { ProjectFolderSummary } from "../../components/project/ProjectFolderSummary.tsx";
import { ProjectRootSummary } from "../../components/project/ProjectRootSummary.tsx";
import { CopyIcon, FindIcon, GotoLineIcon } from "../../components/source/viewer-icons.tsx";
import { setFilesStagePaneMode } from "../review/review-pane.ts";
import type { FilesTreeSelection } from "../tree/files-tree-context.ts";
import { absolutePathForBuffer } from "./project-files-model.ts";

function EmptyFilesEditorToolbar() {
  return (
    <div
      class="den-files-toolbar"
      data-testid="files-editor-toolbar-empty"
      aria-disabled="true"
    >
      <nav class="den-files-editor__crumb" aria-label="File path">
        <button
          type="button"
          class="den-files-editor__crumb-seg cursor-default opacity-40"
          disabled
        >
          No file selected
        </button>
      </nav>
      <span class="den-files-editor__toolbar-actions">
        <button
          type="button"
          class="den-files-editor__tool den-inset-icon-btn"
          data-tip="Find"
          data-tip-pos="below"
          aria-label="Find in file"
          disabled
        >
          <FindIcon />
        </button>
        <button
          type="button"
          class="den-files-editor__tool den-inset-icon-btn"
          data-tip="Go to line"
          data-tip-pos="below"
          aria-label="Go to line"
          disabled
        >
          <GotoLineIcon />
        </button>
        <button
          type="button"
          class="den-files-editor__tool den-inset-icon-btn"
          data-tip="Copy path"
          data-tip-pos="below"
          aria-label="Copy path"
          disabled
        >
          <CopyIcon />
        </button>
      </span>
    </div>
  );
}

/** With no open tabs the editor area summarizes the selected root or folder. */
export function FilesEmptyEditor(props: {
  projectId: string;
  sessionId: string | undefined;
  client: LycaonClient | null;
  roots: ProjectRoot[];
  rootId: string | undefined;
  folder: { root: ProjectRoot; path: string } | null;
  childCount: (rootId: string, path: string) => number | null;
  onCopyPath: (rootId: string, path: string) => void;
  onCopyRelativePath: (rootId: string, path: string) => void;
  onOpenFile: (selection: FilesTreeSelection) => void;
}) {
  return (
    <>
      <EmptyFilesEditorToolbar />
      <div
        class="den-files-empty-editor"
        data-testid="files-editor-empty"
      >
        <Show
          when={props.rootId}
          keyed
          fallback={
            <Show
              when={props.folder}
              keyed
              fallback={
                <p class="den-files-empty">
                  {props.roots.length > 0
                    ? "Select a file to view or edit."
                    : "Files you open will show here."}
                </p>
              }
            >
              {(folder) => (
                <ProjectFolderSummary
                  root={folder.root}
                  path={folder.path}
                  childCount={props.childCount(folder.root.id, folder.path)}
                  openIn={{ absolutePath: absolutePathForBuffer(folder.root.path, folder.path), projectRoots: props.roots.map((r) => r.path), entryKind: "folder" }}
                  onCopyPath={() => props.onCopyPath(folder.root.id, folder.path)}
                  onCopyRelativePath={() => props.onCopyRelativePath(folder.root.id, folder.path)}
                />
              )}
            </Show>
          }
        >
          {(rootId) => (
            <ShowLatest when={props.roots.find((root) => root.id === rootId)} by={(root) => root.id}>
              {(root) => (
                <ProjectRootSummary
                  projectId={props.projectId}
                  sessionId={props.sessionId}
                  root={root()}
                  roots={props.roots}
                  client={props.client}
                  onOpenFile={props.onOpenFile}
                  onOpenChanges={() => setFilesStagePaneMode(props.projectId, "review")}
                />
              )}
            </ShowLatest>
          )}
        </Show>
      </div>
    </>
  );
}
