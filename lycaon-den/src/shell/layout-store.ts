import { batch, createSignal, untrack } from "solid-js";
import type {
  ContextNavItemId,
  DenLayoutPrefs,
  DenListPanePrefs,
  DenListPanesPrefs,
  NarrowSplitSurvivor,
  StagePlacement,
  SplitOrder,
  SplitPane,
  WorkspaceOrientation,
} from "../../shared/app-state-types.ts";
import {
  clampListPaneSizePx,
  resolveListPanePrefs,
  type ListPaneAxis,
} from "../list/list-pane-model.ts";
import {
  clampColumnWidthPx,
  type ColumnWidth,
} from "../list/list-columns.ts";
import type { SortState } from "../list/list-sort.ts";
import type { ResizeSession } from "../layout/resize-session.ts";
import { PANE_HIDDEN_PX } from "../layout/drag-to-hide.ts";
import { windowSubject } from "../platform/windows/window-subject.ts";
import { getAppStateSnapshot } from "../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../store/app-state-background-write.ts";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  resetShellLayoutBusyForTests,
} from "./shell-layout-busy.ts";
import { clampNavWidthPx, resolveNavWidthPx } from "./shell-layout-model.ts";
import {
  DEFAULT_PLACEMENT,
  DEFAULT_WORKSPACE_ORIENTATION,
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

type LayoutScope = "persistent-main" | "ephemeral-peer";
type ResizeKey =
  | "nav"
  | "split"
  | `pane:${string}:${ListPaneAxis}`
  | `column:${string}:${string}`;

type ResizeTransaction = {
  id: number;
  key: ResizeKey;
  startValue: number;
  previewValue: number;
  movementThreshold: number;
  commit: (value: number) => void;
};

const scope: LayoutScope =
  windowSubject() == null ? "persistent-main" : "ephemeral-peer";
const [preferences, setPreferences] = createSignal<DenLayoutPrefs>({});
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
const [resize, setResize] = createSignal<ResizeTransaction | null>(null);

let peerInitialized = false;
let nextResizeId = 0;

function currentPreferences(): DenLayoutPrefs {
  return preferences();
}

async function commitPreferences(
  next: DenLayoutPrefs,
  options?: {
    settleLayout?: boolean;
    applyRelatedState?: () => void;
  },
): Promise<void> {
  if (options?.settleLayout) beginShellLayoutBusy();
  try {
    batch(() => {
      setPreferences(next);
      options?.applyRelatedState?.();
    });
  } finally {
    if (options?.settleLayout) endShellLayoutBusy();
  }
  if (scope === "persistent-main") {
    await persistAppStateInBackground({ layout: next });
  }
}

function resizePreview(key: ResizeKey): number | null {
  const current = resize();
  return current?.key === key ? current.previewValue : null;
}

function finishResize(id: number, outcome: "commit" | "cancel"): void {
  const current = resize();
  if (!current || current.id !== id) return;
  batch(() => {
    setResize(null);
    if (
      outcome === "commit" &&
      Math.abs(current.previewValue - current.startValue) >= current.movementThreshold
    ) {
      current.commit(current.previewValue);
    }
  });
  endShellLayoutBusy();
}

function cancelActiveResize(): void {
  const current = resize();
  if (current) finishResize(current.id, "cancel");
}

function beginResize(
  key: ResizeKey,
  startValue: number,
  normalize: (value: number) => number,
  movementThreshold: number,
  commitValue: (value: number) => void,
): ResizeSession {
  cancelActiveResize();
  const id = ++nextResizeId;
  const initial = normalize(Number.isFinite(startValue) ? startValue : 0);
  setResize({
    id,
    key,
    startValue: initial,
    previewValue: initial,
    movementThreshold,
    commit: commitValue,
  });
  beginShellLayoutBusy();

  return {
    preview(value) {
      if (!Number.isFinite(value)) return;
      const next = normalize(value);
      setResize((current) => {
        if (!current || current.id !== id || current.previewValue === next) return current;
        return {
          ...current,
          previewValue: next,
        };
      });
    },
    commit() {
      finishResize(id, "commit");
    },
    cancel() {
      finishResize(id, "cancel");
    },
  };
}

/** Peer windows seed once and keep local state. */
export function syncLayoutFromSnapshot(): void {
  const stored = getAppStateSnapshot().layout ?? {};
  if (scope === "ephemeral-peer") {
    if (peerInitialized) return;
    setPreferences({ ...stored, mode: "inline", navCollapsed: true });
    peerInitialized = true;
    return;
  }
  setPreferences(stored);
}

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

function baseListPanePrefs(surface: string): DenListPanePrefs {
  return resolveListPanePrefs(currentPreferences(), surface);
}

export function listPanePrefs(surface: string): DenListPanePrefs {
  const base = baseListPanePrefs(surface);
  const width = resizePreview(`pane:${surface}:width`);
  const height = resizePreview(`pane:${surface}:height`);
  const current = resize();
  if (width != null) {
    return width === PANE_HIDDEN_PX ? base : { ...base, widthPx: width };
  }
  if (height != null) {
    return height === PANE_HIDDEN_PX ? base : { ...base, heightPx: height };
  }
  if (current?.key.startsWith(`column:${surface}:`)) {
    const key = current.key.slice(`column:${surface}:`.length);
    return {
      ...base,
      columnWidths: {
        ...(base.columnWidths ?? {}),
        [key]: current.previewValue,
      },
    };
  }
  return base;
}

async function writeListPane(
  surface: string,
  pane: DenListPanePrefs,
): Promise<void> {
  const next = { ...currentPreferences() };
  const listPanes: DenListPanesPrefs = { ...(next.listPanes ?? {}) };
  if (Object.keys(pane).length > 0) listPanes[surface] = pane;
  else delete listPanes[surface];
  if (Object.keys(listPanes).length > 0) next.listPanes = listPanes;
  else delete next.listPanes;
  await commitPreferences(next);
}

/** A pane drag has passed the point where releasing hides the pane. */
export function listPaneHidePreviewed(
  surface: string,
  axis: ListPaneAxis,
): boolean {
  return resizePreview(`pane:${surface}:${axis}`) === PANE_HIDDEN_PX;
}

/** With `onHide`, a drag may preview and commit {@link PANE_HIDDEN_PX}. */
export function beginListPaneResize(
  surface: string,
  axis: ListPaneAxis,
  effectivePx: number,
  bodySize?: number,
  onHide?: () => void,
): ResizeSession {
  const normalize = (value: number) =>
    onHide && value === PANE_HIDDEN_PX
      ? value
      : clampListPaneSizePx(surface, axis, value, bodySize);
  return beginResize(
    `pane:${surface}:${axis}`,
    effectivePx,
    normalize,
    0.5,
    (value) => {
      untrack(() => {
        if (value === PANE_HIDDEN_PX) {
          onHide?.();
          return;
        }
        const pane = baseListPanePrefs(surface);
        void writeListPane(
          surface,
          axis === "width"
            ? { ...pane, widthPx: value }
            : { ...pane, heightPx: value },
        );
      });
    },
  );
}

export function beginListColumnResize(
  surface: string,
  column: string,
  effectivePx: number,
  width: ColumnWidth,
): ResizeSession {
  return beginResize(
    `column:${surface}:${column}`,
    effectivePx,
    (value) => clampColumnWidthPx(width, value),
    0.5,
    (value) => {
      untrack(() => {
        const pane = baseListPanePrefs(surface);
        void writeListPane(surface, {
          ...pane,
          columnWidths: { ...(pane.columnWidths ?? {}), [column]: value },
        });
      });
    },
  );
}

export async function resetListPaneSize(
  surface: string,
  axis: ListPaneAxis,
): Promise<void> {
  const pane = { ...baseListPanePrefs(surface) };
  if (axis === "width") delete pane.widthPx;
  else delete pane.heightPx;
  await writeListPane(surface, pane);
}

export async function commitListSort(
  surface: string,
  state: SortState,
): Promise<void> {
  const pane = { ...baseListPanePrefs(surface) };
  if (state) {
    pane.sortKey = state.key;
    pane.sortDir = state.dir;
  } else {
    delete pane.sortKey;
    delete pane.sortDir;
  }
  await writeListPane(surface, pane);
}

export function resetLayoutStoreForTests(): void {
  peerInitialized = false;
  setResize(null);
  setPreferences({
    mode: DEFAULT_PLACEMENT,
    workspaceOrientation: DEFAULT_WORKSPACE_ORIENTATION,
  });
  setViewportWidthPx(
    typeof window === "undefined" ? 0 : Math.round(window.innerWidth),
  );
  setSplitHostWidth(0);
  setSplitProjectIdSignal(null);
  setSplitFocusRegionSignal(SPLIT_COLUMN_DEFAULT);
  resetShellLayoutBusyForTests();
}
