import { DIVIDER_PX, STAGE_COL_MIN } from "./stage-placement.ts";
import { type ContextNavItemId } from "../../shared/app-state-types.ts";

/** Inline non-Files stage floor while the application nav is visible. */
export const APP_NAV_STAGE_MIN_PX = 680;

/** Restore headroom prevents threshold chatter. */
export const AUTO_COLLAPSE_RESTORE_HEADROOM_PX = 24;

/** Rounding clearance for the CSS Files seam. */
const FILES_RESTORE_HEADROOM_PX = 4;

export type NavCollapseInput = {
  automaticCollapsed: boolean;
  viewportWidthPx: number;
  navWidthPx: number;
  stageMinWidthPx: number;
  /** The stage and the conversation both take width; a hidden conversation takes none. */
  splitColumns: boolean;
  /** Width the split conversation keeps while the sidebars yield. */
  chatWidthPx: number;
};

export function workspaceStageFloorPx(input: {
  splitLive: boolean;
  stageId: ContextNavItemId | null;
}): number {
  // A live split, and Files anywhere, keep the tree beside the content.
  if (input.splitLive || input.stageId === "files") return STAGE_COL_MIN;
  return APP_NAV_STAGE_MIN_PX;
}

function splitHostWidthForStagePx(stageWidthPx: number, chatWidthPx: number): number {
  return stageWidthPx + chatWidthPx + DIVIDER_PX;
}

function requiredHostWidth(
  input: Pick<NavCollapseInput, "splitColumns" | "stageMinWidthPx" | "chatWidthPx">,
): number {
  // The sidebars yield before the conversation narrows.
  return input.splitColumns
    ? splitHostWidthForStagePx(input.stageMinWidthPx, input.chatWidthPx)
    : input.stageMinWidthPx;
}

/** Automatic collapse restores with headroom. */
export function shouldAutomaticallyCollapseNav(
  input: NavCollapseInput,
): boolean {
  const collapseAt = input.navWidthPx + requiredHostWidth(input);
  const restoreAt = collapseAt + AUTO_COLLAPSE_RESTORE_HEADROOM_PX;
  const boundary = input.automaticCollapsed ? restoreAt : collapseAt;

  return input.viewportWidthPx < boundary;
}

export type StageOpenDeficitInput = {
  viewportWidthPx: number;
  navWidthPx: number;
  /** Whether the target width has to hold the sidebar as well. */
  countNavWidth: boolean;
  stageMinWidthPx: number;
  /** The stage and the conversation both take width. */
  splitColumns: boolean;
  /** Width the split conversation keeps. */
  chatWidthPx: number;
};

/** Window growth needed to preserve stage chrome. */
export function stageOpenWindowDeficitPx(input: StageOpenDeficitInput): number {
  const nav = input.countNavWidth ? input.navWidthPx : 0;
  return Math.max(
    0,
    nav +
      requiredHostWidth(input) +
      AUTO_COLLAPSE_RESTORE_HEADROOM_PX -
      input.viewportWidthPx,
  );
}

export type FilesShowInput = {
  viewportWidthPx: number;
  navWidthPx: number;
  navCurrentlyVisible: boolean;
  navUserCollapsed: boolean;
  /** The Files stage shares the host with a visible conversation. */
  splitColumns: boolean;
  /** Width the split conversation keeps. */
  chatWidthPx: number;
};

/** Window growth needed to reveal Files. */
export function filesShowWindowDeficitPx(input: FilesShowInput): number {
  const desiredStageWidth =
    STAGE_COL_MIN + FILES_RESTORE_HEADROOM_PX;
  let targetHostWidth = desiredStageWidth;

  if (input.splitColumns) {
    targetHostWidth = splitHostWidthForStagePx(desiredStageWidth, input.chatWidthPx);
  }

  const navRestoreThreshold =
    input.navWidthPx +
    requiredHostWidth({
      splitColumns: input.splitColumns,
      stageMinWidthPx: STAGE_COL_MIN,
      chatWidthPx: input.chatWidthPx,
    }) +
    AUTO_COLLAPSE_RESTORE_HEADROOM_PX;
  const navWillBeVisible =
    !input.navUserCollapsed &&
    (input.navCurrentlyVisible || targetHostWidth >= navRestoreThreshold);
  const targetViewport =
    targetHostWidth + (navWillBeVisible ? input.navWidthPx : 0);
  return Math.max(0, targetViewport - input.viewportWidthPx);
}
