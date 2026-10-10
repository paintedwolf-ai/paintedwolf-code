import {
  CONTEXT_NAV_CATALOG,
  type ContextNavItemId,
  type DenLayoutPrefs,
  type NarrowSplitSurvivor,
  type StagePlacement,
  type SplitPane,
  type WorkspaceOrientation,
} from "../../shared/app-state-types.ts";

/** A 240px Files tree and 320px editor set the shared split-width floor. */
export const STAGE_COL_MIN = 560;
export const CHAT_COL_MIN = 340;
export const DIVIDER_PX = 1;
export const SPLIT_MIN_HOST = STAGE_COL_MIN + CHAT_COL_MIN + DIVIDER_PX;

/** Conversation width a split opens at until the divider moves. */
export const CHAT_WIDTH_DEFAULT_PX = 440;
/** Stage shares a divider drag settles on. */
const STAGE_SHARE_SNAPS = [1 / 3, 0.5, 2 / 3] as const;
const SNAP_TOLERANCE = 0.02;

export const DEFAULT_WORKSPACE_ORIENTATION = "standard" as const;
export const DEFAULT_PLACEMENT: StagePlacement = "inline";
export const DEFAULT_SPLIT_COMPANION: ContextNavItemId = "files";

export function resolveMode(
  prefs: DenLayoutPrefs | undefined,
): StagePlacement {
  const mode = prefs?.mode;
  if (mode === "split") return mode;
  return DEFAULT_PLACEMENT;
}

export function resolveHiddenSplitPane(prefs: DenLayoutPrefs | undefined): SplitPane | null {
  return prefs?.hiddenSplitPane ?? null;
}

export function resolveWorkspaceOrientation(
  prefs: DenLayoutPrefs | undefined,
): WorkspaceOrientation {
  return prefs?.workspaceOrientation === "mirrored"
    ? "mirrored"
    : DEFAULT_WORKSPACE_ORIENTATION;
}

/** Stage the launch layout opens beside the conversation; null opens chat alone. */
export function resolveStartupCompanion(
  prefs: DenLayoutPrefs | undefined,
): ContextNavItemId | null {
  const stageId = prefs?.startupCompanion;
  if (!stageId) return null;
  return CONTEXT_NAV_CATALOG.includes(stageId) ? stageId : null;
}

/** Below the split-width floor, an unset survivor follows the launch layout. */
export function resolveNarrowSurvivor(
  prefs: DenLayoutPrefs | undefined,
): NarrowSplitSurvivor {
  const pinned = prefs?.narrowSurvivor;
  if (pinned === "conversation" || pinned === "stage") return pinned;
  return resolveStartupCompanion(prefs) ? "stage" : "conversation";
}

/** The survivor tracks the launch layout rather than a choice of its own. */
export function narrowSurvivorFollowsLaunch(
  prefs: DenLayoutPrefs | undefined,
): boolean {
  const pinned = prefs?.narrowSurvivor;
  return pinned !== "conversation" && pinned !== "stage";
}

/** Which split columns are legible. Outside a split the one column answers true. */
export type SplitColumnsOnScreen = {
  conversation: boolean;
  stage: boolean;
};

/** Below the split-width floor, only the chosen survivor remains visible. */
export function splitColumnsOnScreen(input: {
  splitLive: boolean;
  hidden: SplitPane | null;
  hostWidthPx: number;
  narrowSurvivor: NarrowSplitSurvivor;
}): SplitColumnsOnScreen {
  if (!input.splitLive) return { conversation: true, stage: true };
  if (input.hidden) return { conversation: input.hidden !== "conversation", stage: input.hidden !== "stage" };
  if (input.hostWidthPx >= SPLIT_MIN_HOST) {
    return { conversation: true, stage: true };
  }
  return {
    conversation: input.narrowSurvivor === "conversation",
    stage: input.narrowSurvivor === "stage",
  };
}

export function withNarrowSurvivor(
  prefs: DenLayoutPrefs | undefined,
  survivor: NarrowSplitSurvivor | null,
): DenLayoutPrefs {
  const next = { ...(prefs ?? {}) };
  if (survivor) next.narrowSurvivor = survivor;
  else delete next.narrowSurvivor;
  return next;
}

export function resolveStoredCompanion(
  prefs: DenLayoutPrefs | undefined,
): ContextNavItemId | null {
  const stageId = prefs?.companion;
  if (!stageId) return null;
  return CONTEXT_NAV_CATALOG.includes(stageId) ? stageId : null;
}

export function resolveSplitCompanion(
  companion: ContextNavItemId | null,
  isAvailable: (id: ContextNavItemId) => boolean = () => true,
): ContextNavItemId {
  if (companion && isAvailable(companion)) return companion;
  return DEFAULT_SPLIT_COMPANION;
}

/** A stored conversation width, or undefined when it is not a width. */
export function normalizeChatWidthPx(px: number): number | undefined {
  if (!Number.isFinite(px)) return undefined;
  return Math.max(CHAT_COL_MIN, Math.round(px));
}

/** Conversation column width. The stage absorbs window resizes. */
export function resolveChatWidthPx(prefs: DenLayoutPrefs | undefined): number {
  const px = prefs?.chatWidthPx;
  return (typeof px === "number" ? normalizeChatWidthPx(px) : undefined) ??
    CHAT_WIDTH_DEFAULT_PX;
}

/** Widest conversation that still leaves the stage its floor. */
export function maxChatWidthPx(hostWidthPx: number): number {
  if (!Number.isFinite(hostWidthPx) || hostWidthPx <= DIVIDER_PX) {
    return Number.POSITIVE_INFINITY;
  }
  const usable = hostWidthPx - DIVIDER_PX;
  if (usable - STAGE_COL_MIN >= CHAT_COL_MIN) return usable - STAGE_COL_MIN;
  // The visual split is hidden when both floors cannot fit.
  return Number.POSITIVE_INFINITY;
}

/** Reserves the stage's minimum width when both split columns fit. */
export function clampChatWidthPx(chatWidthPx: number, hostWidthPx: number): number {
  const px = normalizeChatWidthPx(chatWidthPx) ?? CHAT_WIDTH_DEFAULT_PX;
  return Math.min(maxChatWidthPx(hostWidthPx), px);
}

/** Snaps a dragged conversation width so the stage lands on a third or half. */
export function snapChatWidthPx(chatWidthPx: number, hostWidthPx: number): number {
  const usable = hostWidthPx - DIVIDER_PX;
  if (!(usable > 0)) return chatWidthPx;
  for (const share of STAGE_SHARE_SNAPS) {
    const snapped = usable * (1 - share);
    if (Math.abs(chatWidthPx - snapped) <= SNAP_TOLERANCE * usable) {
      return Math.round(snapped);
    }
  }
  return chatWidthPx;
}

/** Stage share of the split for a conversation width (0–1). */
export function stageShare(chatWidthPx: number, hostWidthPx: number): number {
  const usable = hostWidthPx - DIVIDER_PX;
  if (!(usable > 0)) return 0;
  return Math.min(1, Math.max(0, (usable - chatWidthPx) / usable));
}

export type StagePlacementCommit =
  | { mode: "inline" }
  | { mode: "split"; companion: ContextNavItemId };

export function withPlacement(
  prefs: DenLayoutPrefs | undefined,
  placement: StagePlacementCommit,
): DenLayoutPrefs {
  const next: DenLayoutPrefs = { ...(prefs ?? {}), mode: placement.mode };
  if (placement.mode === "split") next.companion = placement.companion;
  // Choosing a placement asks to see it whole.
  delete next.hiddenSplitPane;
  return next;
}

export function withCompanion(
  prefs: DenLayoutPrefs | undefined,
  stageId: ContextNavItemId | null,
): DenLayoutPrefs {
  const next = { ...(prefs ?? {}) };
  if (stageId) next.companion = stageId;
  else delete next.companion;
  return next;
}

export function withWorkspaceOrientation(
  prefs: DenLayoutPrefs | undefined,
  orientation: WorkspaceOrientation,
): DenLayoutPrefs {
  return { ...(prefs ?? {}), workspaceOrientation: orientation };
}

export function withStartupCompanion(
  prefs: DenLayoutPrefs | undefined,
  stageId: ContextNavItemId | null,
): DenLayoutPrefs {
  const next = { ...(prefs ?? {}) };
  if (stageId) next.startupCompanion = stageId;
  else delete next.startupCompanion;
  return next;
}

export function withChatWidthPx(
  prefs: DenLayoutPrefs | undefined,
  chatWidthPx: number,
): DenLayoutPrefs {
  const next = { ...(prefs ?? {}) };
  const px = normalizeChatWidthPx(chatWidthPx);
  if (px === undefined) delete next.chatWidthPx;
  else next.chatWidthPx = px;
  return next;
}

export type StartupPlacementInput = {
  /** First run is still on screen, so the workspace has not been entered. */
  firstRunPending: boolean;
  chatLive: boolean;
  /** Open-at-launch override. */
  launchCompanion: ContextNavItemId | null;
  splitMode: boolean;
  persistedCompanion: ContextNavItemId | null;
  isAvailable: (id: ContextNavItemId) => boolean;
};

export type StartupPlacementDecision = {
  settled: boolean;
  openStage: ContextNavItemId | null;
};

/** Applies launch placement when the first conversation is live. */
export function startupPlacementDecision(
  input: StartupPlacementInput,
): StartupPlacementDecision {
  if (input.firstRunPending || !input.chatLive) {
    return { settled: false, openStage: null };
  }
  if (input.launchCompanion) {
    return {
      settled: true,
      openStage: resolveSplitCompanion(
        input.launchCompanion,
        input.isAvailable,
      ),
    };
  }
  if (!input.splitMode) return { settled: true, openStage: null };
  return {
    settled: true,
    openStage: resolveSplitCompanion(
      input.persistedCompanion,
      input.isAvailable,
    ),
  };
}

export type StageColumnInput = {
  navStage: ContextNavItemId | null;
  foregroundIsChat: boolean;
  hasChat: boolean;
  companion: ContextNavItemId | null;
  splitMode: boolean;
};

export type StageColumnResolution = {
  splitLive: boolean;
  stageId: ContextNavItemId | null;
};

/** Which stage occupies the split column. */
export function resolveStageColumn(
  input: StageColumnInput,
): StageColumnResolution {
  const live = input.hasChat && input.splitMode;
  if (input.navStage) return { splitLive: live, stageId: input.navStage };
  if (!input.foregroundIsChat) return { splitLive: false, stageId: null };
  if (!live) return { splitLive: false, stageId: null };
  return {
    splitLive: true,
    stageId: input.companion ?? DEFAULT_SPLIT_COMPANION,
  };
}
