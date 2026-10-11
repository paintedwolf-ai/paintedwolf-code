import { Show, type ComponentProps } from "solid-js";
import { ResidentPortal } from "../../components/primitives/ResidentPortal.tsx";
import { EditorCommandBar } from "../../find/EditorCommandBar.tsx";
import { editorCommandBarPlacement } from "../../find/find-controller.ts";
import { FileUndoToast } from "../history/FileUndoToast.tsx";
import { EmptyFilesTabStrip, FilesTabStrip } from "../tabs/FilesTabStrip.tsx";
import { WalkTransport } from "../walk/WalkTransport.tsx";
import { FilesEmptyEditor } from "./FilesEmptyEditor.tsx";
import { FilesPaneDeck } from "./FilesPaneDeck.tsx";

type Props = ComponentProps<typeof FilesPaneDeck> & ComponentProps<typeof FilesTabStrip> &
  Pick<ComponentProps<typeof FilesEmptyEditor>, "rootId" | "folder" | "childCount" | "onCopyPath" | "onCopyRelativePath" | "onOpenFile"> & {
    onEditorElement: (element: HTMLDivElement) => void;
    editorElement: HTMLDivElement | undefined;
    walkPresentation: ComponentProps<typeof WalkTransport>["presentation"];
    walkDockMount: HTMLElement | undefined;
    onUndo: ComponentProps<typeof FileUndoToast>["onUndo"];
  };

export function FilesEditorWorkspace(props: Props) {
  return (
    <div class="den-files-editor-pane" ref={props.onEditorElement}>
      <Show when={editorCommandBarPlacement() === "files-editor"}>
        <div class="den-editor-command-bar-host--files" data-testid="find-bar-host-files">
          <EditorCommandBar />
        </div>
      </Show>
      <FileUndoToast projectId={props.projectId} onUndo={props.onUndo} />
      <Show when={props.walking}>
        <WalkTransport projectId={props.projectId} presentation={props.walkPresentation}
          host={props.editorElement} dockMount={props.walkDockMount} />
      </Show>
      <Show when={props.scope.state().order.length > 0} fallback={
        <>
          <EmptyFilesTabStrip treeCollapsed={props.treeCollapsed} side={props.side}
            onToggleTree={props.onToggleTree} onAutoRestore={props.onAutoRestore} />
          <FilesEmptyEditor
            projectId={props.projectId}
            sessionId={props.sessionId}
            client={props.client}
            roots={props.roots}
            rootId={props.rootId}
            folder={props.folder}
            childCount={props.childCount}
            onCopyPath={props.onCopyPath}
            onCopyRelativePath={props.onCopyRelativePath}
            onOpenFile={props.onOpenFile}
          />
        </>
      }>
        <FilesTabStrip
          treeCollapsed={props.treeCollapsed}
          side={props.side}
          onToggleTree={props.onToggleTree}
          onAutoRestore={props.onAutoRestore}
          scope={props.scope}
          tabs={props.tabs}
          drag={props.drag}
          hiddenTabCount={props.hiddenTabCount}
          showLeadingFade={props.showLeadingFade}
          showTrailingFade={props.showTrailingFade}
          scopeMarksVersion={props.scopeMarksVersion}
          previousVersionForBuffer={props.previousVersionForBuffer}
          agentPresenceForPath={props.agentPresenceForPath}
          mergingKey={props.mergingKey}
          requestCloseTab={props.requestCloseTab}
          onTabContextMenu={props.onTabContextMenu}
          onScrollerElement={props.onScrollerElement}
          onTabsElement={props.onTabsElement}
        />
        <FilesPaneDeck
          projectId={props.projectId}
          sessionId={props.sessionId}
          client={props.client}
          roots={props.roots}
          scanUpdate={props.scanUpdate}
          document={props.document}
          onRevealInTranscript={props.onRevealInTranscript}
          activePane={props.activePane}
          onDisplayedChange={props.onDisplayedChange}
          onPublishingChange={props.onPublishingChange}
          pending={props.pending}
          retainedWait={props.retainedWait}
          preparation={props.preparation}
          walking={props.walking}
          walkReady={props.walkReady}
          readerHandles={props.readerHandles}
          onDockMount={props.onDockMount}
          saveError={props.saveError}
          updatedNote={props.updatedNote}
          restoringVersion={props.restoringVersion}
          cursor={props.cursor}
          onCursor={props.onCursor}
          onInnerLayerChange={props.onInnerLayerChange}
          observationNoticeFor={props.observationNoticeFor}
          revealInTree={props.revealInTree}
          retryEditorOpening={props.retryEditorOpening}
          scrollActiveTabIntoView={props.scrollActiveTabIntoView}
          openWalkStepFile={props.openWalkStepFile}
          reopenBufferAs={props.reopenBufferAs}
          confirmations={props.confirmations}
          documents={props.documents}
          versions={props.versions}
          history={props.history}
          restoration={props.restoration}
          definitions={props.definitions}
          merge={props.merge}
          editorActions={props.editorActions}
        />
      </Show>
    </div>
  );
}

export function FilesTabPullPreview(props: {
  preview: ReturnType<ComponentProps<typeof FilesTabStrip>["drag"]["pullOffPreview"]>;
}) {
  return (
    <Show when={props.preview}>
      <ResidentPortal mount={document.body}>
        <div class="den-files-tab-pull-preview"
          style={{ left: `${props.preview?.x ?? 0}px`, top: `${props.preview?.y ?? 0}px` }}
          role="status" aria-label={`Release to move ${props.preview?.name ?? "file"} in a new window`}
          data-testid="files-tab-pull-preview">
          <span class="den-files-tab-pull-preview__window" aria-hidden="true"><span /></span>
          <span class="den-files-tab-pull-preview__name">{props.preview?.name}</span>
          <span class="den-files-tab-pull-preview__hint">Release to move to new window</span>
        </div>
      </ResidentPortal>
    </Show>
  );
}
