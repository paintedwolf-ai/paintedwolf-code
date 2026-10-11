import { createEffect, type Accessor } from "solid-js";
import { observeElementExtent } from "../../layout/element-extent.ts";
import {
  FILES_TREE_SURFACE,
  clampListPaneSizePx,
  listPaneSizeMinPx,
  maxListPaneSizePx,
  resolveListPaneSizePx,
  type ListPaneAxis,
} from "../../list/list-pane-model.ts";
import { widenWindowBy } from "../../platform/windows/window-chrome.ts";
import { dragRequestsHide, PANE_HIDDEN_PX } from "../../layout/drag-to-hide.ts";
import {
  beginListPaneResize,
  listPaneHidePreviewed,
  listPanePrefs,
  resetListPaneSize,
} from "../../list/list-layout.ts";
import {
  layoutViewportWidthPx,
  navCollapsedPref,
  preferredChatWidthPx,
  preferredNavWidthPx,
  splitHostWidthPx,
  workspaceOrientationPref,
} from "../../shell/workspace-layout.ts";
import { filesShowWindowDeficitPx } from "../../shell/responsive-collapse.ts";
import {
  filesTreeCollapsed,
  setFilesTreeClaimed,
  setFilesTreeCollapsed,
} from "./files-tree-window-state.ts";

export function createFilesTreeLayout(stageEl: Accessor<HTMLElement | undefined>) {
  // Observed extent avoids layout reads during dragging.
  const stageExtent = observeElementExtent(stageEl);
  const bodySize = (axis: ListPaneAxis) =>
    axis === "width" ? stageExtent.width() : stageExtent.height();
  const treeStyle = () => {
    // A drag past the hide threshold previews the tree closed.
    if (listPaneHidePreviewed(FILES_TREE_SURFACE, "width")) {
      return { "--den-files-tree-w": `${PANE_HIDDEN_PX}px` };
    }
    const prefs = listPanePrefs(FILES_TREE_SURFACE);
    if (prefs.widthPx == null) return undefined;
    return {
      "--den-files-tree-w": `${clampListPaneSizePx(FILES_TREE_SURFACE, "width", prefs.widthPx, bodySize("width"))}px`,
    };
  };
  let lastFilesClaimViewport = layoutViewportWidthPx();
  createEffect(() => {
    const viewport = layoutViewportWidthPx();
    if (viewport === lastFilesClaimViewport) return;
    lastFilesClaimViewport = viewport;
    setFilesTreeClaimed(false);
  });

  const showFilesTree = () => {
    setFilesTreeCollapsed(false);
    setFilesTreeClaimed(true);
    const viewportWidth = layoutViewportWidthPx();
    const hostWidth = splitHostWidthPx();
    const stageWidth = bodySize("width");
    const splitLive = stageWidth != null && hostWidth - stageWidth > 1;
    void widenWindowBy(
      filesShowWindowDeficitPx({
        viewportWidthPx: viewportWidth,
        navWidthPx: preferredNavWidthPx(),
        navCurrentlyVisible: viewportWidth - hostWidth > 1,
        navUserCollapsed: navCollapsedPref(),
        splitColumns: splitLive,
        chatWidthPx: preferredChatWidthPx(),
      }),
    );
  };
  const stripSide = () => workspaceOrientationPref() === "mirrored" ? "right" : "left";
  const hideFilesTree = () => {
    setFilesTreeClaimed(false);
    setFilesTreeCollapsed(true);
  };
  const toggleFilesTree = () => {
    if (filesTreeCollapsed()) showFilesTree();
    else hideFilesTree();
  };

  return {
    treeStyle,
    showFilesTree,
    toggleFilesTree,
    stripSide,
    resizeDirection: () => workspaceOrientationPref() === "mirrored" ? -1 as const : 1 as const,
    size: (axis: ListPaneAxis) => resolveListPaneSizePx(
      FILES_TREE_SURFACE, listPanePrefs(FILES_TREE_SURFACE), axis, bodySize(axis),
    ),
    clamp: (value: number, axis: ListPaneAxis) =>
      axis === "width" && dragRequestsHide(value, listPaneSizeMinPx(FILES_TREE_SURFACE, axis))
        ? PANE_HIDDEN_PX
        : clampListPaneSizePx(FILES_TREE_SURFACE, axis, value, bodySize(axis)),
    beginResize: (axis: ListPaneAxis, startSize: number) => beginListPaneResize(
      FILES_TREE_SURFACE, axis, startSize, bodySize(axis), axis === "width" ? hideFilesTree : undefined,
    ),
    resetSize: (axis: ListPaneAxis) => { void resetListPaneSize(FILES_TREE_SURFACE, axis); },
    minSize: (axis: ListPaneAxis) => listPaneSizeMinPx(FILES_TREE_SURFACE, axis),
    maxSize: (axis: ListPaneAxis) => maxListPaneSizePx(FILES_TREE_SURFACE, axis, bodySize(axis)),
  };
}
