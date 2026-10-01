import { createEffect } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { widenWindowBy } from "../../platform/windows/window-chrome.ts";
import { companionPref, preferredChatWidthPx, layoutViewportWidthPx, preferredNavWidthPx, saveStagePlacementMode, stagePlacementMode, startupCompanionPref } from "../../shell/layout-store.ts";
import { stageOpenWindowDeficitPx, workspaceStageFloorPx } from "../../shell/responsive-collapse.ts";
import { runShellLayoutTransaction } from "../../shell/shell-layout-busy.ts";
import { resolveSplitCompanion, startupPlacementDecision } from "../../shell/stage-placement.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import type { SidebarChatRow } from "../../store/projects-sidebar-model.ts";
import type { SessionNavigation } from "./session-navigation.ts";
import type { ShellNavigation } from "./shell-navigation-state.ts";
import type { ShellPaneVisibility } from "./shell-pane-visibility.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchState } from "./shell-search-state.ts";

type StagePlacementDependencies = Pick<ShellScope, "isPeerWindow" | "activeProjectId" | "readyChat" | "nav" | "stageAvailable">
  & Pick<ShellPaneVisibility, "windowNavUserCollapsed"> & Pick<ShellNavigation, "navigationIntent">
  & Pick<ShellSearchState, "ensureSearch"> & Pick<SessionNavigation, "startNewChat"> & {
  firstConversation: () => SidebarChatRow | null;
  resumeConversation: (row: SidebarChatRow) => Promise<void>;
  openStage: (stage: ContextNavItemId) => void;
  suppressShellForFirstRun: () => boolean;
  chatReady: () => boolean;
};

export type ShellStagePlacement = ReturnType<typeof createShellStagePlacement>;

export function createShellStagePlacement({ isPeerWindow, activeProjectId, windowNavUserCollapsed,
  readyChat, firstConversation, resumeConversation, startNewChat, nav, openStage,
  ensureSearch, navigationIntent, suppressShellForFirstRun, chatReady, stageAvailable }: StagePlacementDependencies) {
  /** Peer windows keep the size the person set. */
  const stageChromeDeficitPx = (
    splitLive: boolean,
    stageId: ContextNavItemId,
  ) => {
    if (isPeerWindow) return 0;
    return stageOpenWindowDeficitPx({
      viewportWidthPx: layoutViewportWidthPx(),
      navWidthPx: preferredNavWidthPx(),
      // Opening a stage restores chrome the width folded, but not a person's hide.
      countNavWidth: !windowNavUserCollapsed(),
      stageMinWidthPx: workspaceStageFloorPx({ splitLive, stageId }),
      // Choosing a placement shows both split columns.
      splitColumns: splitLive,
      chatWidthPx: preferredChatWidthPx(),
    });
  };

  const claimStageChrome = (
    splitLive: boolean,
    stageId: ContextNavItemId,
  ) => widenWindowBy(stageChromeDeficitPx(splitLive, stageId));

  const applyStagePlacement = async (
    stageId: ContextNavItemId,
    placement: "inline" | "split",
  ) => {
    const projectId = activeProjectId();
    if (!projectId) return;
    const intent = navigationIntent.begin();
    const current = () => intent.current() && projectId === activeProjectId();
    const activeConversation = readyChat();
    const conversation = activeConversation
      ? null
      : firstConversation();
    await runShellLayoutTransaction(async () => {
      // Pending width prevents automatic collapse; immediate placement keeps repeated presses consistent.
      const widening = claimStageChrome(placement === "split", stageId);
      await Promise.all([
        saveStagePlacementMode(
          placement === "split"
            ? { mode: "split", companion: stageId }
            : { mode: "inline" },
        ),
        widening,
      ]);
    });
    if (!current() || placement !== "split") return;
    if (stageId === "search") ensureSearch(projectId);
    if (conversation) {
      await resumeConversation(conversation);
      return;
    }
    // Split needs a conversation in the other column.
    if (!activeConversation) {
      await startNewChat(projectId);
      return;
    }
    if (nav() !== stageId && nav() !== "projects") openStage(stageId);
  };

  // Startup width is claimed before the workspace becomes visible.
  let startupWidthClaimed = isPeerWindow;
  createEffect(() => {
    if (startupWidthClaimed) return;
    const projectId = activeProjectId();
    if (!projectId || suppressShellForFirstRun()) return;
    const launch = startupCompanionPref();
    if (!launch && stagePlacementMode() !== "split") {
      startupWidthClaimed = true;
      return;
    }
    const stage = resolveSplitCompanion(launch ?? companionPref(), stageAvailable);
    if (!stage) return;
    startupWidthClaimed = true;
    void workspacePreparation(projectId).run("launch-layout-width", () => claimStageChrome(true, stage));
  });

  // Peer views open inline; the primary window uses its launch preference.
  let startupPlacementSettled = isPeerWindow;
  createEffect(() => {
    if (startupPlacementSettled) return;
    const launch = startupCompanionPref();
    const decision = startupPlacementDecision({
      firstRunPending: suppressShellForFirstRun(),
      chatLive: chatReady(),
      launchCompanion: launch,
      splitMode: stagePlacementMode() === "split",
      persistedCompanion: companionPref(),
      isAvailable: stageAvailable,
    });
    if (!decision.settled) return;
    startupPlacementSettled = true;
    const projectId = activeProjectId();
    const stage = decision.openStage;
    if (stage && projectId) {
      // The window claim for the launch layout lands under the veil.
      void workspacePreparation(projectId).run("launch-layout", () =>
        applyStagePlacement(stage, "split"),
      );
    }
  });

  return { claimStageChrome, applyStagePlacement, stageChromeDeficitPx };
}
