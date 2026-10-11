import { type Nav, type ShellNavigation } from "./shell-navigation-state.ts";
import { type ShellScope } from "./shell-scope.ts";
import { createProjectLifecycle } from "./project-lifecycle.ts";
import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { type Project } from "../../api/types.ts";
import { moveSessionPin, setSessionPinned } from "../../chat/session/session-retirement.ts";
import { draftCount, type HomeSection } from "../../home/home-model.ts";
import { type NoticeStore } from "../../notices/notice-store.ts";
import { closeItemWindow, focusItemWindow } from "../../platform/windows/item-windows.ts";
import { type ProjectSummary } from "../../project/project-summary.ts";
import { saveChatListSort } from "../../settings/chat/chat-list-prefs.ts";
import { dismissLayoutDockOnOutsidePress } from "../../shell/layout-dock-dismiss.ts";
import { hiddenSplitPanePref, effectiveNavWidthPx, resetChatWidthPx, stagePlacementMode, toggleWorkspaceOrientation, swapSplitColumns, workspaceOrientationPref } from "../../shell/layout-store.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import { isRegionEntry, regionEntryTarget, registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { type AttentionStore } from "../../store/attention-store.ts";
import { type CostStore } from "../../store/cost-store.ts";
import { sidebarChatSections } from "../../store/projects-sidebar-model.ts";
import { type SettingsStore } from "../../store/settings-store.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { ComposerStatusChips } from "../chatview/ComposerStatusChips.tsx";
import { HomeNav } from "../home/HomeNav.tsx";
import { FocusedProjectNav } from "../nav/FocusedProjectNav.tsx";
import { NavBrandIdentity } from "../nav/NavBrandIdentity.tsx";
import { NavFold } from "../nav/NavFold.tsx";
import { NavResizeHandle } from "../nav/NavResizeHandle.tsx";
import { NavSidebarCollapseButton } from "../nav/NavSidebarToggle.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { SettingsNavSidebar } from "../settings/SettingsNavSidebar.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { ShellNavRail } from "./ShellNavRail.tsx";
import { LayoutDockButton, LayoutDockTray } from "./LayoutDock.tsx";
import { type ShellPaneVisibility } from "./shell-pane-visibility.ts";
import { type SessionNavigation } from "./session-navigation.ts";
import { type ShellPeerWindows } from "./shell-peer-windows.ts";
import { type ShellSearchState } from "./shell-search-state.ts";
import { type ShellStagePlacement } from "./shell-stage-placement.ts";

/** Entering the sidebar lands on the current chat, then any chat, then any rail control. */
const SIDEBAR_ENTRY_PRIORITY = [
  '.focused-session-list__row[aria-current="true"]',
  ".focused-session-list__row",
  ".den-shell-aside button",
] as const;

function projectPromotionPending(
  project: Pick<Project, "promotion"> | null | undefined,
): boolean {
  return project?.promotion != null && project.promotion.phase !== "committed";
}

type Props = Pick<ShellScope, "shell" | "appStore" | "projects" | "projectSessions" | "activeProject" | "activeChat" | "readyChat" | "nav" | "setNav" | "stageAvailable" | "splitLive" | "setLauncherOpen"> &
  Pick<ShellNavigation, "dockTray" | "closeLayoutTray" | "settingsSection" | "selectSettingsSection" | "closeSettingsNav" | "openSettings" | "toggleLayoutTray"> &
  Pick<ShellPaneVisibility, "windowNavCollapsed" | "navSlotWidthCss" | "hideWindowNav" | "toggleConversation" | "contextHidden" | "toggleContext"> &
  Pick<SessionNavigation, "goHome" | "startNewChat" | "selectSidebarSession" | "sessionRetirementDeps" | "sidebarRowAsRecent" | "archiveSidebarSession" | "deleteSidebarSession" | "openSessionInNewWindow"> & {
  settingsStore: SettingsStore;
  costStore: CostStore;
  notices: NoticeStore;
  peerWindows: Pick<ShellPeerWindows, "sessionWindowIds" | "sessionWindowCounts" | "sessionWindowViewNumbers" | "raiseSessionWindow" | "closeSessionWindow" | "detachedStageIds" | "contextWindowViewNumbers" | "projectPeerViews" | "raiseStageWindow" | "closeStageWindow" | "stageInPeerWindow">;
  projectLifecycle: Pick<ReturnType<typeof createProjectLifecycle>, "activeProjectRoots" | "addFolderToActiveProject" | "detachFolderFromActiveProject" | "retryActiveProjectPromotion" | "cancelActiveProjectPromotion" | "openMoveDialog">;
  homeSection: () => HomeSection;
  setHomeSection: (section: HomeSection) => void;
  homeSummaries: () => ProjectSummary[];
  search: Pick<ShellSearchState, "searchOpen" | "crossbarOpen" | "closeSearch" | "closeCrossbar" | "openSearch">;
  sidebarSections: () => ReturnType<typeof sidebarChatSections>;
  sidebarChatsPublished: () => boolean;
  attentionIndex: () => ReturnType<AttentionStore["index"]>;
  identityReady: () => boolean;
  workspaceRevealed: () => boolean;
  onFreshSession: () => boolean;
  renameSession: (row: { projectId: string; sessionId: string }, title: string) => Promise<void>;
  showStage: (stage: Exclude<Nav, "projects" | "search">) => void;
  goToContextStage: (stage: ContextNavItemId) => void;
  stageEntryActive: (stage: ContextNavItemId) => boolean;
  detachStageToWindow: ShellPeerWindows["detachStageToWindow"];
  closeStageWindows: ShellPeerWindows["closeStageWindows"];
  openConfiguration: () => void;
  conversationInLayout: () => boolean;
  railStatusProject: () => string | null;
  railStatusSessionId: (projectId: string) => string;
  layoutTrayStageId: () => ContextNavItemId | null;
  stageColumnStageId: () => ContextNavItemId | null;
  stageChromeDeficitPx: ShellStagePlacement["stageChromeDeficitPx"];
  applyStagePlacement: ShellStagePlacement["applyStagePlacement"];
  presentedSplitLive: () => boolean;
  layoutTriggerRef: (el: HTMLButtonElement) => void;
};

export function WorkspaceSidebar(props: Props) {
  const { activeProject, activeChat, readyChat, nav, setNav, stageAvailable, splitLive, setLauncherOpen, dockTray, closeLayoutTray, settingsSection, selectSettingsSection, closeSettingsNav, openSettings, toggleLayoutTray, windowNavCollapsed, navSlotWidthCss, hideWindowNav, toggleConversation, contextHidden, toggleContext, goHome, startNewChat, selectSidebarSession, sessionRetirementDeps, sidebarRowAsRecent, archiveSidebarSession, deleteSidebarSession, openSessionInNewWindow, homeSection, setHomeSection, homeSummaries, sidebarSections, sidebarChatsPublished, attentionIndex, identityReady, workspaceRevealed, onFreshSession, renameSession, showStage, goToContextStage, stageEntryActive, detachStageToWindow, closeStageWindows, openConfiguration, conversationInLayout, railStatusProject, railStatusSessionId, layoutTrayStageId, stageColumnStageId, stageChromeDeficitPx, applyStagePlacement, presentedSplitLive, peerWindows } = props;
  const { searchOpen, crossbarOpen, closeSearch, closeCrossbar, openSearch } = props.search;
  const { activeProjectRoots, addFolderToActiveProject, detachFolderFromActiveProject,
    retryActiveProjectPromotion, cancelActiveProjectPromotion, openMoveDialog } = props.projectLifecycle;
  const noticeIndex = () => props.notices.index();
  const sidebarFocusClaim = {};
  const [sidebarEl, setSidebarEl] = createSignal<HTMLElement | null>(null);
  createEffect(() => {
    const el = sidebarEl();
    if (el && !windowNavCollapsed()) {
      el.tabIndex = -1;
      registerFocusRegion("sidebar", el, sidebarFocusClaim);
      const onFocus = () => {
        if (!isRegionEntry(el)) return;
        regionEntryTarget(el, SIDEBAR_ENTRY_PRIORITY)?.focus();
      };
      el.addEventListener("focus", onFocus);
      onCleanup(() => el.removeEventListener("focus", onFocus));
    } else {
      releaseFocusRegion("sidebar", sidebarFocusClaim);
    }
  });
  onCleanup(() => releaseFocusRegion("sidebar", sidebarFocusClaim));
  let navDockEl: HTMLDivElement | undefined;

  dismissLayoutDockOnOutsidePress({
    open: () => dockTray() === "layout",
    dock: () => navDockEl,
    onDismiss: closeLayoutTray,
  });

  return (
      <aside
        class="den-shell-aside"
        style={{ "--den-nav-slot-width": navSlotWidthCss() }}
        {...chromeProps()}
        aria-hidden={windowNavCollapsed()}
        inert={windowNavCollapsed() ? true : undefined}
        ref={setSidebarEl}
      >
        <div class="den-shell-aside-main">
          <div class="den-shell-aside-titlebar">
            <ChromeDragSurface />
            <NavSidebarCollapseButton
              side={
                workspaceOrientationPref() === "mirrored" ? "right" : "left"
              }
              onClick={hideWindowNav}
            />
          </div>
          <NavBrandIdentity
            onClick={() => {
              setHomeSection("recents");
              goHome();
            }}
          />
          <Show
            when={props.shell.state.activeProjectId}
            fallback={
              <HomeNav
                section={homeSection()}
                searchActive={searchOpen() || crossbarOpen()}
                onSectionChange={(section) => {
                  closeSearch();
                  closeCrossbar();
                  setHomeSection(section);
                  setNav("projects");
                }}
                onOpenSearch={() => openSearch({ originProjectId: null })}
                draftCount={draftCount(homeSummaries())}
              />
            }
          >
            <ShellNavRail>
              <FocusedProjectNav
                activeProject={
                  props.shell.state.activeProjectId
                    ? props.projects.summary(props.shell.state.activeProjectId) ??
                      null
                    : null
                }
                roots={activeProjectRoots()}
                sessions={sidebarSections()}
                chatSort={props.projectSessions.state.sort}
                chatSortSettled={props.projectSessions.state.rowsSort === props.projectSessions.state.sort}
                onChatSortChange={(sort) => void saveChatListSort(sort)}
                sessionsLoaded={sidebarChatsPublished()}
                sessionsError={props.projectSessions.state.error}
                onRetrySessions={() => void props.projectSessions.refresh()}
                totalChats={props.projectSessions.state.total}
                attention={attentionIndex()}
                noticeIndex={noticeIndex}
                identityLoaded={identityReady()}
                statusRevealed={workspaceRevealed()}
                activeChat={activeChat()}
                newChatActive={onFreshSession()}
                onNewChat={(projectId) => void startNewChat(projectId)}
                onSelectSession={(row) => void selectSidebarSession(row)}
                onRenameSession={(row, title) =>
                  void renameSession(
                    { projectId: row.projectId, sessionId: row.sessionId },
                    title,
                  )
                }
                onTogglePin={(row, pinned) =>
                  void setSessionPinned(
                    sessionRetirementDeps(),
                    sidebarRowAsRecent(row),
                    pinned,
                  )
                }
                onMovePin={(row, position) =>
                  void moveSessionPin(
                    sessionRetirementDeps(),
                    sidebarRowAsRecent(row),
                    position,
                  )
                }
                onArchiveSession={(row) => void archiveSidebarSession(row)}
                onDeleteSession={(row) => void deleteSidebarSession(row)}
                sessionWindowIds={peerWindows.sessionWindowIds()}
                sessionWindowCounts={peerWindows.sessionWindowCounts()}
                sessionWindowViewNumbers={peerWindows.sessionWindowViewNumbers()}
                onOpenSessionInNewWindow={openSessionInNewWindow}
                onRaiseSessionWindow={(row, viewNumber) => peerWindows.raiseSessionWindow(row.sessionId, viewNumber)}
                onCloseSessionWindow={(row, viewNumber) => peerWindows.closeSessionWindow(row.sessionId, viewNumber)}
                onOpenAllChats={() => showStage("chats")}
                allChatsActive={nav() === "chats"}
                onOpenLauncher={() => setLauncherOpen(true)}
                onAddFolder={() => void addFolderToActiveProject()}
                onDetachFolder={(rootId) => void detachFolderFromActiveProject(rootId)}
                onOpenStage={goToContextStage}
                isStageActive={(id) =>
                  crossbarOpen() ? id === "search" : stageEntryActive(id)
                }
                isStageAvailable={(id) =>
                  stageAvailable(id)
                }
                detachedStages={peerWindows.detachedStageIds()}
                contextWindowViewNumbers={peerWindows.contextWindowViewNumbers()}
                projectViews={peerWindows.projectPeerViews()}
                onOpenInNewWindow={(stageId) => void detachStageToWindow(stageId)}
                onRaiseWindow={peerWindows.raiseStageWindow}
                onCloseContextWindow={peerWindows.closeStageWindow}
                onFocusProjectView={(label) => void focusItemWindow(label)}
                onCloseProjectView={(label) => void closeItemWindow(label)}
                splitLive={splitLive()}
                onOpenConfiguration={openConfiguration}
                configActive={nav() === "context"}
                chatFocused={conversationInLayout()}
                isDraft={activeProject()?.is_draft}
                promotePending={projectPromotionPending(activeProject())}
                promotionPhase={activeProject()?.promotion?.phase}
                promotionError={activeProject()?.promotion?.last_error}
                onSaveDraft={() => {
                  const projectId = props.shell.state.activeProjectId;
                  if (projectId) openMoveDialog(projectId);
                }}
                onRetryPromotion={() => void retryActiveProjectPromotion()}
                onCancelPromotion={() => void cancelActiveProjectPromotion()}
                statusChips={
                  <Show when={railStatusProject()} keyed>
                    {(projectId) => (
                      <PresentationProvider preparation={workspacePreparation(projectId)}>
                      <ComposerStatusChips
                        appStore={props.appStore}
                        settingsStore={props.settingsStore}
                        costStore={props.costStore}
                        sessionId={railStatusSessionId(projectId)}
                        projectId={projectId}
                        roots={props.projects.byId(projectId)?.roots}
                      />
                      </PresentationProvider>
                    )}
                  </Show>
                }
              />
            </ShellNavRail>
          </Show>
          {/* Settings and Layout share one dock tray. */}
          <div
            ref={(el) => (navDockEl = el)}
            class="den-shell-nav-dock"
            data-testid="shell-nav-dock"
          >
            <NavFold open={dockTray() === "settings"}>
              <SettingsNavSidebar
                section={settingsSection()}
                onSectionChange={(section) => selectSettingsSection(section)}
              />
            </NavFold>
            <NavFold open={dockTray() === "layout"}>
              <LayoutDockTray
                stageId={layoutTrayStageId()}
                placement={stagePlacementMode()}
                splitLive={
                  splitLive() &&
                  stageColumnStageId() === layoutTrayStageId()
                }
                widensFor={(placement) => {
                  const stageId = layoutTrayStageId();
                  return (
                    stageId != null &&
                    stageChromeDeficitPx(placement === "split", stageId) > 0
                  );
                }}
                hasConversation={readyChat() != null}
                onPlacement={(placement) => {
                  const stageId = layoutTrayStageId();
                  if (stageId) void applyStagePlacement(stageId, placement);
                }}
                stageInWindow={peerWindows.stageInPeerWindow(layoutTrayStageId())}
                onStageWindowChange={
                  layoutTrayStageId()
                    ? (inWindow) => {
                        const stageId = layoutTrayStageId();
                        if (!stageId) return;
                        if (inWindow) void detachStageToWindow(stageId);
                        else closeStageWindows(stageId);
                      }
                    : undefined
                }
                onToggleOrientation={() => void toggleWorkspaceOrientation()}
                onSwapColumns={() => void swapSplitColumns()}
                onResetSplitSize={() => {
                  void resetChatWidthPx();
                }}
                conversationHidden={(hiddenSplitPanePref() === "conversation")}
                onToggleConversation={() => toggleConversation()}
                contextHidden={contextHidden()}
                onToggleContext={toggleContext}
              />
            </NavFold>
            <div class="den-shell-nav-dock-row">
              <button
                type="button"
                class="den-shell-dock-link"
                data-testid="nav-settings"
                classList={{ "den-shell-dock-link-active": nav() === "settings" }}
                aria-expanded={nav() === "settings"}
                onClick={() => {
                  closeSearch();
                  if (nav() === "settings") {
                    closeSettingsNav();
                    return;
                  }
                  openSettings();
                }}
              >
                <ThemeIcon
                  slot="settings"
                  class="den-shell-dock-link__icon"
                  size={15}
                />
                <span class="den-shell-dock-link__label">Settings</span>
              </button>
              <LayoutDockButton
                elementRef={props.layoutTriggerRef}
                open={dockTray() === "layout"}
                splitLive={presentedSplitLive()}
                stageId={layoutTrayStageId()}
                onToggle={toggleLayoutTray}
              />
            </div>
          </div>
        </div>
        <NavResizeHandle
          onHide={hideWindowNav}
          width={effectiveNavWidthPx()}
          side={workspaceOrientationPref() === "mirrored" ? "right" : "left"}
        />
      </aside>
  );
}
