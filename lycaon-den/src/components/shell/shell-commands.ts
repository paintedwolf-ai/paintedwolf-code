import { onCleanup } from "solid-js";
import { CONTEXT_NAV_CATALOG, type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { isChatActivityLive } from "../../chat/session/session-activity.ts";
import { findEverywhereSeed, findNext, findPrev, selectAllFindMatches, toggleFind, toggleFindReplace } from "../../find/find-controller.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { installShellCommand } from "../../platform/desktop/shell-command.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { closeCurrentPeerWindow } from "../../platform/windows/item-windows.ts";
import { fileSummariesEnabled, saveFileSummariesEnabled } from "../../settings/editor/file-summary-settings.ts";
import { APP_SETTINGS } from "../../settings/settings-nav-model.ts";
import { pendingReplaceArm } from "../../search/replace-arm.ts";
import { projectScopedCodeQuery } from "../../search/search-query-model.ts";
import { hiddenSplitPanePref, resetChatWidthPx, setSplitFocusRegion, stagePlacementMode, toggleWorkspaceOrientation, swapSplitColumns } from "../../shell/layout-store.ts";
import { registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { focusRegion, cycleFocusRegion, type FocusRegionId } from "../../shortcuts/focus-region.ts";
import { adjacentSession, sidebarChatSections, sidebarNavigationOrder } from "../../store/projects-sidebar-model.ts";
import type { SessionNavigation } from "./session-navigation.ts";
import type { ShellChatActions } from "./shell-chat-actions.ts";
import type { ShellNavigation } from "./shell-navigation-state.ts";
import type { ShellPaneVisibility } from "./shell-pane-visibility.ts";
import type { ShellPeerWindows } from "./shell-peer-windows.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchState } from "./shell-search-state.ts";
import type { ShellStagePlacement } from "./shell-stage-placement.ts";

type Dependencies = Pick<ShellScope,
  "activeProjectId" | "activeChat" | "readyChat" | "appStore" | "nav" | "setNav" | "showProjects" | "stageAvailable"
  | "splitLive" | "isPeerWindow" | "reportError" | "helpOpen" | "setHelpOpen" | "launcherOpen" | "setLauncherOpen"
  | "recentSwitcher" | "peerViewSwitcher">
  & Pick<ShellNavigation, "openSettings">
  & Pick<SessionNavigation, "newProject" | "resumeSession" | "sidebarRowAsRecent" | "startNewChat" | "goHome">
  & Pick<ShellSearchState,
    "openCrossbar" | "searchOpen" | "searchArgs" | "openSearch" | "closeSearch" | "crossbarOpen" | "closeCrossbar">
  & Pick<ShellPaneVisibility, "revealConversation" | "toggleWindowNav" | "toggleConversation" | "toggleContext" | "windowNavCollapsed" | "showWindowNav">
  & Pick<ShellStagePlacement, "applyStagePlacement"> & Pick<ShellChatActions, "stopChat">
  & Pick<ShellPeerWindows, "openFocusedSubjectInNewWindow" | "focusWindowByNumber" | "cycleNextPeerWindow" | "raiseSessionWindow" | "raiseFileWindow" | "sessionPeer" | "isThisSessionWindow" | "filePeer"> & {
  sidebarSections: () => ReturnType<typeof sidebarChatSections>;
  layoutTargetStage: () => ContextNavItemId | null;
  conversationInLayout: () => boolean;
  setPendingRegionFocus: (region: FocusRegionId | null) => void;
  goToContextStage: (stage: ContextNavItemId) => void;
  showChats: () => void;
};

/**
 * Registers the shell's search, navigation, app, window, settings, layout, and session
 * commands until the calling component unmounts.
 */
export function registerShellCommands(deps: Dependencies) {
  const on = (id: string, handler: () => void) => onCleanup(registerCommandHandler(id, handler));

  on("go.nextRegion", () => cycleFocusRegion(1));
  on("go.previousRegion", () => cycleFocusRegion(-1));
  on("search.openFiles", () => deps.openCrossbar({ originProjectId: deps.activeProjectId(), mode: "files" }));
  on("search.openActions", () => deps.openCrossbar({ originProjectId: deps.activeProjectId(), mode: "actions" }));
  on("search.open", () => {
    deps.openCrossbar({
      originProjectId: deps.activeProjectId(),
      seed: deps.searchOpen() ? deps.searchArgs()?.seed : undefined,
    });
  });
  on("find.inView", () => {
    toggleFind();
  });
  on("find.replace", () => {
    toggleFindReplace();
  });
  on("find.next", () => {
    findNext();
  });
  on("find.prev", () => {
    findPrev();
  });
  on("find.selectAllMatches", () => {
    selectAllFindMatches();
  });
  on("find.everywhere", () => {
    if (deps.searchOpen()) {
      const origin = deps.activeProjectId();
      const seed = findEverywhereSeed();
      deps.openSearch({
        originProjectId: origin,
        seed: seed || undefined,
      });
      return;
    }
    deps.openCrossbar({
      originProjectId: deps.activeProjectId(),
      seed: findEverywhereSeed() || undefined,
      // Find everywhere opens the evidence search mode.
      mode: "evidence",
    });
  });
  on("search.replaceInProject", () => {
    // Pending arms provide query, scope, and flags.
    const arm = pendingReplaceArm();
    const origin =
      arm?.originProjectId ?? deps.activeProjectId();
    const seed = arm?.query.trim() || "";
    deps.openSearch({
      originProjectId: origin,
      seed: seed
        ? origin
          ? projectScopedCodeQuery(seed)
          : seed
        : undefined,
      replaceMode: true,
    });
  });

  const goFocus = (targetRegion: "chat" | "context" | "sidebar" | "composer" | "files") => {
    if (targetRegion === "chat") {
      const active = deps.activeChat();
      if (
        active?.sessionId &&
        !deps.isThisSessionWindow(active.sessionId) &&
        deps.sessionPeer(active.sessionId)
      ) {
        deps.raiseSessionWindow?.(active.sessionId);
        return;
      }
    }
    const region: "context" | "sidebar" | "composer" | "files" =
      targetRegion === "chat" ? "composer" : targetRegion;

    if (
      region === "composer" &&
      deps.splitLive() &&
      (hiddenSplitPanePref() === "conversation")
    ) {
      // Focus follows paint while the conversation remains visible.
      void deps.revealConversation().then((opened) => {
        if (opened) goFocus(targetRegion);
      });
      return;
    }
    if (region === "composer" && deps.splitLive()) setSplitFocusRegion("chat");
    if (region === "context" && deps.splitLive()) setSplitFocusRegion("stage");
    if (
      region === "composer" &&
      !deps.conversationInLayout() &&
      !deps.isPeerWindow &&
      deps.readyChat() != null
    ) {
      // Focus lands once the chat column mounts.
      deps.setPendingRegionFocus(region);
      deps.showProjects();
      return;
    }
    if (region === "files") {
      if (deps.splitLive()) setSplitFocusRegion("stage");
      if (deps.nav() !== "files") {
        deps.goToContextStage("files");
        deps.setPendingRegionFocus("files");
      }
      if (!focusRegion("files")) {
        if (!focusRegion("filesTree")) {
          focusRegion("context");
        }
      }
      return;
    }
    focusRegion(region);
  };
  on("go.chat", () => {
    goFocus("chat");
  });
  on("go.sidebar", () => {
    if (deps.windowNavCollapsed()) {
      // Focus follows paint while the rail remains visible.
      void deps.showWindowNav().then((opened) => {
        if (opened) goFocus("sidebar");
      });
      return;
    }
    goFocus("sidebar");
  });
  on("go.composer", () => {
    goFocus("composer");
  });
  on("go.files", () => {
    goFocus("files");
  });
  for (const stage of CONTEXT_NAV_CATALOG) {
    // registerCommandHandler rejects ids outside the frame inventory.
    on(`go.context.${stage}`, () => {
      if (!deps.stageAvailable(stage)) return;
      deps.closeCrossbar();
      deps.goToContextStage(stage);
      queueMicrotask(() => {
        if (stage === "files") {
          goFocus("files");
        } else {
          goFocus("context");
        }
      });
    });
  }
  on("go.allChats", () => {
    deps.closeCrossbar();
    deps.showChats();
  });
  on("go.home", () => {
    deps.closeCrossbar();
    deps.goHome();
  });

  on("shell.installPw", () => {
    void (async () => {
      try {
        await installShellCommand();
      } catch (e) {
        const detail = e instanceof Error ? e.message : String(e);
        window.alert(`Could not install pw command.\n\n${detail}`);
      }
    })();
  });
  on("nav.toggle", () => deps.toggleWindowNav());
  on("files.toggleSummaries", () => {
    const client = getLycaonClient();
    if (!client) return;
    void saveFileSummariesEnabled(client, !fileSummariesEnabled()).catch(
      (err) => deps.reportError(err),
    );
  });
  on("launcher.open", () => {
    deps.setLauncherOpen(true);
  });
  on("help.shortcuts", () => {
    deps.setHelpOpen(true);
  });

  on("session.recent", () => {
    deps.recentSwitcher()?.step(1);
  });
  on("session.recentBack", () => {
    deps.recentSwitcher()?.step(-1);
  });
  on("view.openInNewWindow", () => {
    void deps.openFocusedSubjectInNewWindow();
  });
  for (let i = 1; i <= 9; i++) {
    on(`view.jump${i}`, () => {
      void deps.focusWindowByNumber?.(i);
    });
  }
  on("view.switch", () => {
    void deps.cycleNextPeerWindow?.();
  });
  on("view.close", () => {
    if (!deps.isPeerWindow) return;
    void closeCurrentPeerWindow();
  });
  on("window.close", () => {
    if (!isTauriRuntime()) return;
    void import("@tauri-apps/api/window").then(({ getCurrentWindow }) =>
      getCurrentWindow().close(),
    );
  });

  for (const section of APP_SETTINGS) {
    on(`settings.section.${section.id}`, () => {
      deps.closeSearch();
      deps.openSettings(section.id);
    });
  }
  on("settings.advanced.host-resources", () => {
    deps.closeSearch();
    deps.openSettings("debug", { advanced: "host_resources" });
  });
  on("settings.open", () => {
    deps.closeSearch();
    deps.openSettings();
  });

  on("layout.toggleSplit", () => {
    const stageId = deps.layoutTargetStage();
    const projectId = deps.activeProjectId();
    if (!stageId || !projectId) return;
    void deps.applyStagePlacement(
      stageId,
      stagePlacementMode() === "split" ? "inline" : "split",
    );
  });
  on("layout.swapColumns", () => {
    if (deps.splitLive()) void swapSplitColumns();
  });
  on("layout.toggleOrientation", () => {
    void toggleWorkspaceOrientation();
  });
  on("layout.resetSplitSize", () => {
    void resetChatWidthPx();
  });
  on("layout.toggleContext", () => {
    if (deps.splitLive()) deps.toggleContext();
  });
  on("layout.toggleConversation", () => {
    if (deps.splitLive()) deps.toggleConversation();
  });

  on("session.new", () => {
    const projectId = deps.activeProjectId();
    if (!projectId) return;
    void deps.startNewChat(projectId);
  });
  on("project.new", () => {
    deps.newProject();
  });
  const cycleSession = (dir: 1 | -1) => {
    if (deps.launcherOpen() || deps.searchOpen() || deps.helpOpen() || deps.crossbarOpen()) return;
    if (deps.nav() !== "projects") return;
    const projectId = deps.activeProjectId();
    const chat = deps.activeChat();
    if (!projectId || !chat || chat.projectId !== projectId) return;
    const next = adjacentSession(
      sidebarNavigationOrder(deps.sidebarSections()),
      chat.sessionId,
      dir,
    );
    if (!next) return;
    void deps.revealConversation();
    void deps.resumeSession(deps.sidebarRowAsRecent(next));
  };
  on("session.prev", () => {
    cycleSession(-1);
  });
  on("session.next", () => {
    cycleSession(1);
  });
  on("session.stop", () => {
    const chat = deps.activeChat();
    if (!chat) return;
    if (!isChatActivityLive(deps.appStore, chat.sessionId)) return;
    void deps.stopChat();
  });
}
