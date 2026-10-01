import { chatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { contributionCommandAvailable, nativeCommandId } from "../../contributions/dispatch.ts";
import { tryConsumeEscapeLadder } from "../../find/escape-ladder.ts";
import { currentSplitFocusRegion } from "../../shell/layout-store.ts";
import { invokeCommand, registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { isWalking, leaveWalk } from "../../files/walk/walk-store.ts";
import { tryDismissOnboardingWelcome } from "../onboarding/onboarding-welcome-dismiss.ts";
import type { ShellNavigation } from "./shell-navigation-state.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchState } from "./shell-search-state.ts";
import type { ShellWorkerDrawer } from "./shell-worker-drawer.ts";

type Dependencies = Pick<ShellScope,
  "recentSwitcher" | "peerViewSwitcher" | "helpOpen" | "setHelpOpen" | "launcherOpen" | "setLauncherOpen" | "splitLive" |
  "activeProjectId">
  & Pick<ShellSearchState, "crossbarOpen" | "closeCrossbar" | "searchOpen" | "closeSearch">
  & Pick<ShellNavigation, "dockTray" | "closeLayoutTray" | "isSettingsNav" | "closeSettingsNav">
  & Pick<ShellWorkerDrawer, "workersOpen" | "closeWorkers"> & {
  layoutTrigger: () => HTMLElement | undefined;
};

export function registerShellDismissCommand({
  recentSwitcher, peerViewSwitcher, helpOpen, setHelpOpen, crossbarOpen, closeCrossbar,
  searchOpen, closeSearch, launcherOpen, setLauncherOpen, dockTray, closeLayoutTray,
  layoutTrigger, workersOpen, closeWorkers, isSettingsNav, splitLive, closeSettingsNav, activeProjectId,
}: Dependencies) {
  return registerCommandHandler("overlay.dismiss", () => {
    if (tryConsumeEscapeLadder()) return;
    if (tryDismissOnboardingWelcome()) return;
    // Escape cancels the transient switcher first.
    if (recentSwitcher()?.isOpen()) {
      recentSwitcher()?.cancel();
      return;
    }
    if (peerViewSwitcher()?.isOpen()) {
      peerViewSwitcher()?.cancel();
      return;
    }
    if (helpOpen()) {
      setHelpOpen(false);
      return;
    }
    if (crossbarOpen()) {
      closeCrossbar();
      return;
    }
    if (searchOpen()) {
      closeSearch();
      return;
    }
    if (launcherOpen()) {
      setLauncherOpen(false);
      return;
    }
    // Escape closes the layout tray and restores its trigger.
    if (dockTray() === "layout") {
      closeLayoutTray();
      queueMicrotask(() => layoutTrigger()?.focus());
      return;
    }
    const rail = chatTabRailBindings();
    if (rail?.worklogOpen()) {
      rail.onCloseWorklog();
      return;
    }
    if (workersOpen()) {
      closeWorkers();
      return;
    }
    if (isSettingsNav()) {
      // The focused split column handles Escape.
      if (splitLive() && currentSplitFocusRegion() === "chat") return;
      closeSettingsNav();
      return;
    }
    // The floor: with nothing open, Escape closes a walk, then leaves a past file version.
    const projectId = activeProjectId();
    const walkToggle = nativeCommandId("files.walkToggle");
    if (projectId && walkToggle && contributionCommandAvailable(walkToggle) && isWalking(projectId)) {
      leaveWalk(projectId);
      return;
    }
    const currentVersion = nativeCommandId("files.currentVersion");
    if (currentVersion && contributionCommandAvailable(currentVersion)) {
      invokeCommand("files.currentVersion");
    }
  });
}
