import { createSignal, untrack } from "solid-js";
import type {
  ContextNavItemId,
  NarrowSplitSurvivor,
  StagePlacement,
  SplitOrder,
  SplitPane,
  WorkspaceOrientation,
} from "../../shared/app-state-types.ts";
import type { ResizeSession } from "../layout/resize-session.ts";
import { PANE_HIDDEN_PX } from "../layout/drag-to-hide.ts";
import { clampNavWidthPx, resolveNavWidthPx } from "./shell-layout-model.ts";
import {
  clampChatWidthPx,
  narrowSurvivorFollowsLaunch,
  resolveHiddenSplitPane,
  resolveChatWidthPx,
  resolveMode,
  resolveNarrowSurvivor,
  resolveStartupCompanion,
  resolveWorkspaceOrientation,
  withChatWidthPx,
  withCompanion,
  withNarrowSurvivor,
  withPlacement,
  withStartupCompanion,
  withWorkspaceOrientation,
  resolveSplitCompanion,
  resolveStoredCompanion,
  type StagePlacementCommit,
} from "./stage-placement.ts";

import { currentPreferences, commitPreferences } from "../layout/layout-preferences.ts";
import { resizePreview, beginResize, cancelActiveResize } from "../layout/resize-transaction.ts";

const [viewportWidthPx, setViewportWidthPx] = createSignal(
  typeof window === "undefined" ? 0 : Math.round(window.innerWidth),
);
const [splitHostWidth, setSplitHostWidthSignal] = createSignal(0);
const [splitProjectId, setSplitProjectIdSignal] = createSignal<string | null>(
  null,
);
/** Default split region for keyboard actions. */
const SPLIT_COLUMN_DEFAULT = "chat" as const;
const [splitFocusRegion, setSplitFocusRegionSignal] = createSignal<
  "stage" | "chat"
>(SPLIT_COLUMN_DEFAULT);

export function setLayoutViewportWidth(px: number): void {
  setViewportWidthPx(Number.isFinite(px) ? Math.max(0, Math.round(px)) : 0);
}

export function layoutViewportWidthPx(): number {
  return viewportWidthPx();
}

export function preferredNavWidthPx(): number {
  return resolveNavWidthPx(currentPreferences());
}

export function effectiveNavWidthPx(): number {
  const preview = resizePreview("nav");
  return clampNavWidthPx(
    preview == null || preview === PANE_HIDDEN_PX ? preferredNavWidthPx() : preview,
    viewportWidthPx() || undefined,
  );
}

/** A sidebar drag has passed the point where releasing hides the rail. */
export function navHidePreviewed(): boolean {
  return resizePreview("nav") === PANE_HIDDEN_PX;
}

export function navCollapsedPref(): boolean {
  return currentPreferences().navCollapsed === true;
}

/** With `onHide`, a drag may preview and commit {@link PANE_HIDDEN_PX}. */
export function beginNavWidthResize(onHide?: () => void): ResizeSession {
  const viewport = viewportWidthPx() || undefined;
  const normalize = (value: number) =>
    onHide && value === PANE_HIDDEN_PX ? value : clampNavWidthPx(value, viewport);
  const startValue = untrack(effectiveNavWidthPx);
  return beginResize("nav", startValue, normalize, 0.5, (value) => {
    untrack(() => {
      if (value === PANE_HIDDEN_PX) {
        onHide?.();
        return;
      }
      void commitPreferences({ ...currentPreferences(), navWidthPx: value });
    });
  });
}

export async function resetNavWidthPx(): Promise<void> {
  const next = { ...currentPreferences() };
  delete next.navWidthPx;
  await commitPreferences(next);
}

export function commitNavCollapsedPref(collapsed: boolean): void {
  void commitPreferences(
    { ...currentPreferences(), navCollapsed: collapsed },
    { settleLayout: true },
  );
}

/** At most one split pane is explicitly hidden; automatic collapse is separate. */
export function hiddenSplitPanePref(): SplitPane | null {
  return resolveHiddenSplitPane(currentPreferences());
}

export function commitHiddenSplitPane(pane: SplitPane | null): void {
  cancelActiveResize();
  const next = { ...currentPreferences() };
  if (pane) next.hiddenSplitPane = pane;
  else delete next.hiddenSplitPane;
  void commitPreferences(next, { settleLayout: true });
}

export function workspaceOrientationPref(): WorkspaceOrientation {
  return resolveWorkspaceOrientation(currentPreferences());
}

export function splitOrderPref(): SplitOrder {
  return currentPreferences().splitOrder ?? "context-first";
}

export async function saveSplitOrder(splitOrder: SplitOrder): Promise<void> {
  cancelActiveResize();
  await commitPreferences({ ...currentPreferences(), splitOrder }, { settleLayout: true });
}

export async function swapSplitColumns(): Promise<void> {
  await saveSplitOrder(splitOrderPref() === "context-first" ? "chat-first" : "context-first");
}

export function stagePlacementMode(): StagePlacement {
  return resolveMode(currentPreferences());
}

export function startupCompanionPref(): ContextNavItemId | null {
  return resolveStartupCompanion(currentPreferences());
}

export async function saveStartupCompanion(
  stageId: ContextNavItemId | null,
): Promise<void> {
  await commitPreferences(withStartupCompanion(currentPreferences(), stageId));
}

/** Column a split keeps once the host is too narrow for both. */
export function narrowSurvivorPref(): NarrowSplitSurvivor {
  return resolveNarrowSurvivor(currentPreferences());
}

/** The survivor is derived from the launch layout, not pinned on its own. */
export function narrowSurvivorFollowsLaunchPref(): boolean {
  return narrowSurvivorFollowsLaunch(currentPreferences());
}

/** No settle: nothing moves until a host crosses the seam. */
export async function saveNarrowSurvivor(
  survivor: NarrowSplitSurvivor | null,
): Promise<void> {
  await commitPreferences(withNarrowSurvivor(currentPreferences(), survivor));
}

export function preferredChatWidthPx(): number {
  return resolveChatWidthPx(currentPreferences());
}

/** Conversation width requested by a drag or the saved layout, before fitting. */
export function requestedChatWidthPx(): number {
  const preview = resizePreview("split");
  return preview == null || preview === PANE_HIDDEN_PX
    ? preferredChatWidthPx()
    : preview;
}

/** Conversation width to render; the stage takes the rest of the host. */
export function effectiveChatWidthPx(hostWidthPx: number): number {
  return clampChatWidthPx(requestedChatWidthPx(), hostWidthPx);
}

/** A divider drag has passed the point where releasing hides the conversation. */
export function conversationHidePreviewed(): boolean {
  return resizePreview("split") === PANE_HIDDEN_PX;
}

/** With `onHideConversation`, a drag may preview and commit {@link PANE_HIDDEN_PX}. */
export function beginChatWidthResize(
  hostWidthPx: number,
  onHideConversation?: () => void,
): ResizeSession {
  const start = effectiveChatWidthPx(hostWidthPx);
  return beginResize(
    "split",
    start,
    (value) =>
      onHideConversation && value === PANE_HIDDEN_PX
        ? PANE_HIDDEN_PX
        : clampChatWidthPx(value, hostWidthPx),
    0.5,
    (value) => {
      untrack(() => {
        if (value === PANE_HIDDEN_PX) {
          onHideConversation?.();
          return;
        }
        void commitPreferences(withChatWidthPx(currentPreferences(), value));
      });
    },
  );
}

export async function resetChatWidthPx(): Promise<void> {
  const next = { ...currentPreferences() };
  delete next.chatWidthPx;
  await commitPreferences(next);
}

export async function saveWorkspaceOrientation(
  orientation: WorkspaceOrientation,
): Promise<void> {
  cancelActiveResize();
  await commitPreferences(
    withWorkspaceOrientation(currentPreferences(), orientation),
    { settleLayout: true },
  );
}

export async function toggleWorkspaceOrientation(): Promise<void> {
  await saveWorkspaceOrientation(
    workspaceOrientationPref() === "standard" ? "mirrored" : "standard",
  );
}

export async function saveStagePlacementMode(
  placement: StagePlacementCommit,
): Promise<void> {
  await commitPreferences(withPlacement(currentPreferences(), placement), {
    settleLayout: true,
    applyRelatedState: () => {
      if (placement.mode !== "split") {
        setSplitFocusRegionSignal(SPLIT_COLUMN_DEFAULT);
      }
    },
  });
}

export async function rememberCompanionStage(
  stageId: ContextNavItemId,
): Promise<void> {
  if (companionPref() === stageId) return;
  await commitPreferences(withCompanion(currentPreferences(), stageId));
}

export function companionPref(): ContextNavItemId | null {
  return resolveStoredCompanion(currentPreferences());
}

/** Host width for action and drag clamps. */
export function setSplitHostWidth(px: number): void {
  const next = Number.isFinite(px) ? Math.max(0, Math.round(px)) : 0;
  setSplitHostWidthSignal(next);
}

export function splitHostWidthPx(): number {
  return splitHostWidth();
}

export function setSplitProjectId(id: string | null): void {
  // Companion is a surface kind; only column focus resets.
  if (id !== splitProjectId()) {
    setSplitFocusRegionSignal(SPLIT_COLUMN_DEFAULT);
  }
  setSplitProjectIdSignal(id);
}

/** Files when split and companion is unset. */
export function companionStageId(): ContextNavItemId | null {
  if (stagePlacementMode() !== "split") return null;
  return resolveSplitCompanion(companionPref());
}

export function currentSplitFocusRegion(): "stage" | "chat" {
  return splitFocusRegion();
}

export function setSplitFocusRegion(region: "stage" | "chat"): void {
  setSplitFocusRegionSignal(region);
}

export function resetWorkspaceLayoutForTests(): void {
  setViewportWidthPx(
    typeof window === "undefined" ? 0 : Math.round(window.innerWidth),
  );
  setSplitHostWidth(0);
  setSplitProjectIdSignal(null);
  setSplitFocusRegionSignal(SPLIT_COLUMN_DEFAULT);
}
