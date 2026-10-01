import { Show, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { BrowseSegmented } from "../../components/browse/BrowseSegmented.tsx";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { SidebarScopePicker } from "../../components/project/SidebarScopePicker.tsx";
import { bindLayoutBand, LAYOUT_BAND_SCALES } from "../../layout/layout-bands.ts";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import type { LifecycleHooks } from "../commands/project-files-lifecycle.ts";
import { retryProjectSourceHistory, type createSourceHistoryCommands } from "../history/project-files-history.ts";
import { setFilesStagePaneMode } from "../review/review-pane.ts";
import type { WalkFileTarget } from "../walk/walk-model.ts";
import { WalkButton } from "../walk/WalkButton.tsx";
import type { FileBriefingSelection, FileBriefingTarget } from "./file-briefing-target.ts";
import { FileOperationStatus } from "./FileOperationStatus.tsx";
import { FileSummaryDrawer } from "./FileSummaryDrawer.tsx";

/**
 * The tree column: pane switch, file history, Walk and comparison pickers, both pane scroll
 * hosts, and file operation status.
 */
export function FilesTreePane(props: {
  projectId: string;
  appStore: AppStore;
  client: LycaonClient | null;
  paneMode: string;
  hidden: boolean;
  style: JSX.CSSProperties | undefined;
  history: ReturnType<typeof createSourceHistoryCommands>;
  lifecycleHooks: () => LifecycleHooks;
  walkInitialFile: WalkFileTarget | null;
  summarySelection: FileBriefingSelection;
  onOpenSummaryLocation: (target: FileBriefingTarget, line?: number) => void;
  review: JSX.Element;
  tree: JSX.Element;
}) {
  const hiddenPane = (mode: string) => props.paneMode !== mode;
  return (
    <div
      class="den-files-tree-pane"
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.filesTree)}
      data-testid="files-tree-pane"
      hidden={props.hidden}
      style={props.style}
    >
      {/* The tree column's share of the titlebar band the shell header yields to Files. */}
      <div
        class="den-files-tree-pane__drag-band"
        aria-hidden="true"
        {...chromeProps()}
        {...tauriDragRegionProps()}
      />
      <div
        class="den-files-tree-pane-header den-files-tree-pane-header--switch"
        data-files-ctx="no-menu"
      >
        <BrowseSegmented
          class="den-files-pane-segment"
          testId="files-pane-segment"
          ariaLabel="Tree pane"
          value={props.paneMode}
          onChange={(id) => {
            if (id === "files" || id === "review") {
              setFilesStagePaneMode(props.projectId, id);
            }
          }}
          options={[
            { id: "files", label: "Files", testId: "files-pane-files" },
            {
              id: "review",
              label: "Review",
              testId: "files-pane-review",
              ariaLabel: "Review",
            },
          ]}
        />
        <div class="den-files-history-controls" role="group" aria-label="File history">
          <button
            type="button"
            class="den-files-history-button"
            data-tip={props.history.sourceHistory().undo?.label ?? "Nothing to undo"}
            data-tip-pos="below"
            aria-label={props.history.sourceHistory().undo?.label ?? "Nothing to undo"}
            disabled={props.history.historyBusy() || !props.history.sourceHistory().undo}
            onClick={() => void props.history.runSourceHistory("undo")}
          >
            <ThemeIcon slot="undo" size={16} />
          </button>
          <button
            type="button"
            class="den-files-history-button"
            data-tip={props.history.sourceHistory().redo?.label ?? "Nothing to redo"}
            data-tip-pos="below"
            aria-label={props.history.sourceHistory().redo?.label ?? "Nothing to redo"}
            disabled={props.history.historyBusy() || !props.history.sourceHistory().redo}
            onClick={() => void props.history.runSourceHistory("redo")}
          >
            <ThemeIcon slot="redo" size={16} />
          </button>
        </div>
        <WalkButton
          projectId={props.projectId}
          client={props.client}
          appStore={props.appStore}
          initialFile={props.walkInitialFile}
        />
        <SidebarScopePicker
          projectId={props.projectId}
          client={props.client}
          appStore={props.appStore}
        />
      </div>
      {/* Persistent scroll hosts preserve each pane's scrollTop. */}
      <div class="den-files-tree-panes">
        <Scrollport
          class="den-files-tree-body__pane den-files-review-scroll"
          classList={{
            "den-files-tree-body__pane--hidden": hiddenPane("review"),
          }}
          contentClass="den-files-tree-body"
          hidden={hiddenPane("review") ? true : undefined}
          aria-hidden={hiddenPane("review")}
          inert={hiddenPane("review") ? true : undefined}
          data-testid="files-review-scroll"
        >
          {props.review}
        </Scrollport>
        <div
          class="den-files-tree-body__pane den-files-tree-scroll-frame"
          {...chromeProps()}
          classList={{
            "den-files-tree-body__pane--hidden": hiddenPane("files"),
          }}
          hidden={hiddenPane("files") ? true : undefined}
          aria-hidden={hiddenPane("files")}
          inert={hiddenPane("files") ? true : undefined}
          data-testid="files-tree-scroll-frame"
        >
          <div
            class="den-files-tree-sticky-host"
            data-files-tree-sticky-host
            data-testid="files-tree-sticky-host"
          />
          <div
            class="den-files-tree-scroll"
            data-testid="files-tree-scroll"
          >
            <div class="den-files-tree-body">{props.tree}</div>
          </div>
        </div>
      </div>
      <FileOperationStatus
        projectId={props.projectId}
        client={props.client}
        onRetry={async (operation) => {
          const c = props.client;
          if (!c) return;
          if (operation.operation === "undoProjectSourceHistory" || operation.operation === "redoProjectSourceHistory") {
            await retryProjectSourceHistory(
              c,
              props.projectId,
              operation.operation_id,
              operation.operation === "undoProjectSourceHistory" ? "undo" : "redo",
              props.lifecycleHooks(),
              operation.expected_entry_id ?? "",
            );
            return;
          }
          const path = operation.from_path || operation.path;
          if (operation.root_id && path && await props.lifecycleHooks().guardDirtyUnderPath(operation.root_id, path) === "cancel") return;
          await c.retrySourceOperation(props.projectId, operation.operation_id);
        }}
        onComplete={(operation) => {
          const undone = operation.operation === "undoProjectSourceHistory";
          props.history.setHistoryNotice({ label: undone ? "File change undone" : "File operation completed", action: undone ? "redo" : "undo" });
          void props.history.refreshSourceHistory();
        }}
      />
      <Show when={props.history.historyNotice()}>
        <div
          class="den-files-history-toast"
          role="status"
          data-testid="files-history-toast"
        >
          <span class="den-files-history-toast__message">
            {props.history.historyNotice()?.label}
          </span>
          <Show when={props.history.historyNotice()} keyed>
            {(notice) => (
              <DenButton
                compact
                variant="primary"
                disabled={props.history.historyBusy()}
                onClick={() => void props.history.runSourceHistory(notice.action)}
              >
                {notice.action === "undo" ? "Undo" : "Redo"}
              </DenButton>
            )}
          </Show>
          <button
            type="button"
            class="den-files-history-toast__close"
            aria-label="Dismiss file history notice"
            onClick={() => {
              props.history.setHistoryNotice(null);
            }}
          >
            ×
          </button>
        </div>
      </Show>
      <FileSummaryDrawer
        projectId={props.projectId}
        client={props.client}
        selection={props.summarySelection}
        onOpenLocation={props.onOpenSummaryLocation}
      />
    </div>
  );
}
