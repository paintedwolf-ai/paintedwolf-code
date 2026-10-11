import { Show, type ComponentProps } from "solid-js";
import { editorRevealInTreePref } from "../../settings/editor/editor-prefs.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { setSelectedFileVersion } from "../history/files-version-selection.ts";
import { ReviewLens } from "../review/ReviewLens.tsx";
import { FilesTree } from "../tree/FilesTree.tsx";
import { recordInventoryOpen } from "../tree/file-inventory.ts";
import type { ActiveFile, FilesTreeProps } from "../tree/files-tree-context.ts";
import { isAddedInScope, isInScope, resolvedScope, scopeBaseline } from "../tree/scope-resolution.ts";
import { leaveWalk } from "../walk/walk-store.ts";
import { FilesTreePane } from "./FilesTreePane.tsx";

type Props = Omit<ComponentProps<typeof FilesTreePane>, "tree" | "review"> &
  Pick<FilesTreeProps,
    "workspaceId" | "roots" | "sessionId" | "retainPresentation" |
    "navigationRevision" | "navigationOrigin" | "onRevealMotionStart" |
    "selectedEntry" | "onSelectEntry" | "agentPresenceForPath" | "onOpenFile" |
    "onRevertFile" | "createEntry" | "filterQuery" | "onFilterQueryChange" |
    "filterOpen" | "onFilterClose" | "renaming" | "onCommitRename" |
    "onCancelRename" | "onMovePath" | "onReady" | "onRowMenu" |
    "onWorkspaceMismatch" | "onInitialLoadSettled"> & {
      publishedTreeFile: ActiveFile;
      displayedTreeFile: ActiveFile;
      explicitTreeReveal: ActiveFile;
      scopeMarksVersion: number;
      onOpenReviewFile: ComponentProps<typeof ReviewLens>["onOpenFile"];
    };

export function FilesNavigationPane(props: Props) {
  const selectedTreeEntry = () => {
    const selected = props.selectedEntry;
    if (!editorRevealInTreePref() || selected?.kind !== "file") return selected;
    const explicit = props.explicitTreeReveal;
    if (explicit?.revision === props.navigationRevision && explicit.rootId === selected.rootId &&
      explicit.path === selected.path) return selected;
    const displayed = props.displayedTreeFile;
    return displayed ? { ...displayed, kind: "file" as const } : selected;
  };
  const reviewScope = () => {
    void props.scopeMarksVersion;
    const baseline = scopeBaseline(props.projectId);
    return baseline ? { baseline, mark_user_edits: resolvedScope(props.projectId).markUserEdits } : undefined;
  };
  return (
    <FilesTreePane
      projectId={props.projectId}
      appStore={props.appStore}
      client={props.client}
      paneMode={props.paneMode}
      hidden={props.hidden}
      style={props.style}
      history={props.history}
      lifecycleHooks={props.lifecycleHooks}
      walkInitialFile={props.walkInitialFile}
      summarySelection={props.summarySelection}
      onOpenSummaryLocation={props.onOpenSummaryLocation}
      review={
        <ReviewLens
          projectId={props.projectId}
          client={props.client}
          appStore={props.appStore}
          visible={props.paneMode === "review"}
          navigationRevision={props.navigationRevision}
          onOpenFile={props.onOpenReviewFile}
          onViewReviewed={(version) => {
            leaveWalk(props.projectId);
            const root = props.roots.find((entry) => entry.id === version.rootId);
            const key = openFilesBuffer(props.projectId, {
              rootId: version.rootId, rootLabel: root?.label ?? "repo",
              fileId: version.fileId, path: version.path, intent: "transient",
            });
            props.onSelectEntry({ rootId: version.rootId, path: version.path, kind: "file" });
            setSelectedFileVersion(props.projectId, key, version);
            recordInventoryOpen(props.projectId, { rootId: version.rootId, path: version.path });
          }}
          onRevertFile={props.onRevertFile}
          onRowMenu={(anchor, row) => props.onRowMenu?.({ x: anchor.clientX, y: anchor.clientY },
            { surface: "changed-file", rootId: row.rootId, path: row.path, name: row.basename })}
        />
      }
      tree={
        <Show when={props.roots.length > 0} fallback={
          <p class="den-files-empty" data-testid="files-tree-no-roots">Attach a folder to browse its files.</p>
        }>
          <FilesTree
            projectId={props.projectId}
            workspaceId={props.workspaceId}
            visible={props.paneMode === "files"}
            roots={props.roots}
            onWorkspaceMismatch={props.onWorkspaceMismatch}
            onInitialLoadSettled={props.onInitialLoadSettled}
            client={props.client}
            sessionId={props.sessionId}
            reviewScope={reviewScope()}
            retainPresentation={props.retainPresentation}
            autoReveal={editorRevealInTreePref()}
            navigationRevision={props.navigationRevision}
            navigationOrigin={props.navigationOrigin}
            activeFile={editorRevealInTreePref() ? props.publishedTreeFile : null}
            onRevealMotionStart={props.onRevealMotionStart}
            selectedEntry={selectedTreeEntry()}
            onSelectEntry={props.onSelectEntry}
            agentPresenceForPath={props.agentPresenceForPath}
            inScope={(rootId, path) => { void props.scopeMarksVersion; return isInScope(props.projectId, rootId, path); }}
            addedInScope={(rootId, path) => { void props.scopeMarksVersion; return isAddedInScope(props.projectId, rootId, path); }}
            onOpenFile={props.onOpenFile}
            onRevertFile={props.onRevertFile}
            createEntry={props.createEntry}
            filterQuery={props.filterQuery}
            onFilterQueryChange={props.onFilterQueryChange}
            filterOpen={props.filterOpen}
            onFilterClose={props.onFilterClose}
            renaming={props.renaming}
            onCommitRename={props.onCommitRename}
            onCancelRename={props.onCancelRename}
            onMovePath={props.onMovePath}
            onReady={props.onReady}
            onRowMenu={props.onRowMenu}
          />
        </Show>
      }
    />
  );
}
