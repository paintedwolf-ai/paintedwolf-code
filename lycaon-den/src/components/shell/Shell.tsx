import { registerShellCommands, createShellFocusRequest } from "./shell-commands.ts";
import { registerShellDismissCommand } from "./shell-dismiss-command.ts";
import { createShellWorkspaceContext } from "./shell-workspace-context.ts";
import { createShellResidency } from "./shell-residency.ts";
import { createShellNavigationState, type Nav } from "./shell-navigation-state.ts";
import { createShellOnboarding, ShellOnboardingGate } from "./shell-onboarding.tsx";
import { createShellProjectNudges } from "./shell-project-nudges.ts";
import { createShellWorkerDrawer } from "./shell-worker-drawer.ts";
import { installShellNavigationSinks } from "./shell-navigation-sinks.ts";
import { createShellSearchNavigation } from "./shell-search-navigation.ts";
import { createShellSearchState } from "./shell-search-state.ts";
import { createShellStagePlacement, createStageAvailability } from "./shell-stage-placement.ts";
import { createShellPaneVisibility } from "./shell-pane-visibility.ts";
import {
  createShellWorkspacePresentation,
  createWorkspaceIdentity,
} from "./shell-workspace-presentation.ts";
import { type ShellScope, type ShellProps } from "./shell-scope.ts";
import { createShellChatActions } from "./shell-chat-actions.ts";
import { createShellPeerWindows } from "./shell-peer-windows.ts";
import { createShellContributions } from "./shell-contributions.ts";
import { createSessionNavigation } from "./session-navigation.ts";
import { createProjectLifecycle, createWorkspaceProjectRemoval } from "./project-lifecycle.ts";
import { Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { isAnySessionActivityLive } from "../../chat/session/session-activity.ts";
import { contributionFrameReady } from "../../contributions/contribution-store.ts";
import { APP_SCOPE, projectScope } from "../../notices/notice-scope.ts";
import { noticeReporterFor, onCLIOpenEvent } from "../../platform/connection/app-connection.ts";
import { focusAppWindow, windowFocused } from "../../platform/windows/window-chrome.ts";
import { windowSubject } from "../../platform/windows/window-subject.ts";
import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import {
  DEFAULT_PROJECT_CONTEXT_SECTION,
  type ProjectContextSection,
} from "../../settings/settings-nav-model.ts";
import {
  beginChatWidthResize,
  hiddenSplitPanePref,
  companionPref,
  companionStageId,
  effectiveChatWidthPx,
  layoutViewportWidthPx,
  narrowSurvivorPref,
  resetChatWidthPx,
  setSplitFocusRegion,
  setSplitHostWidth,
  setSplitProjectId,
  splitHostWidthPx,
  stagePlacementMode,
  workspaceOrientationPref,
} from "../../shell/workspace-layout.ts";
import {
  resolveSplitCompanion,
  resolveStageColumn,
  splitColumnsOnScreen,
} from "../../shell/stage-placement.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import { NotificationStack } from "../NotificationStack.tsx";
import { createProjectOpening } from "./project-opening.ts";
import { ProjectLoadingStage } from "./ProjectLoadingStage.tsx";
import { SplitDivider } from "./SplitDivider.tsx";
import { StageSplitHost } from "./StageSplitHost.tsx";
import { ShellChatColumn, ShellStageColumn } from "./ShellColumns.tsx";
import { WorkspaceOpeningVeil } from "./WorkspaceOpeningVeil.tsx";
import { ChatWorkspace } from "./ChatWorkspace.tsx";
import { WorkspaceStage } from "./WorkspaceStage.tsx";
import { WorkspaceSidebar } from "./WorkspaceSidebar.tsx";
import { ProjectDialogs } from "./ProjectDialogs.tsx";
import { createShellAttention } from "./shell-attention.ts";
import { WorkspaceOverlays } from "./WorkspaceOverlays.tsx";

export function Shell(props: ShellProps) {
  const itemSubject = windowSubject();
  const { activeProject, activeChat, stageScope, readyChat, presentedChat, chatPresence, homeSummaries } =
    createWorkspaceIdentity(props);
  /** App-scoped reporter for shell conditions that name no project or chat. */
  const shellAppReporter = () => noticeReporterFor(APP_SCOPE);
  const noticeIndex = () => props.notices.index();

  const navigation = createShellNavigationState(
    () => props.shell.state.activeProjectId,
    () => { if (hiddenSplitPanePref() === "stage") void panes.showContext(); },
  );
  const {
    homeSection,
    setHomeSection,
    nav,
    setNav,
    navigationIntent,
    routedReturn,
    routeToStage,
    selectSettingsSection,
    openSettings,
    openContextSection,
  } = navigation;

  const { securityScannersFeatureOn, stageFeatureFlags, stageAvailable, setSecurityScannersFeature } =
    createStageAvailability({ appStore: props.appStore, shell: props.shell, nav, setNav });
  const splitLive = () => stageColumn().splitLive;

  const [launcherOpen, setLauncherOpen] = createSignal(false);
  const [helpOpen, setHelpOpen] = createSignal(false);
  let recentSwitcher: ReturnType<ShellScope["recentSwitcher"]>;
  let peerViewSwitcher: ReturnType<ShellScope["peerViewSwitcher"]>;

  const scope: ShellScope = {
    shell: props.shell, appStore: props.appStore, projects: props.projects, recents: props.recents,
    projectSessions: props.projectSessions, activeProjectId: () => props.shell.state.activeProjectId,
    activeProject, activeChat, readyChat, stageScope, nav, setNav, showProjects: () => setNav("projects"),
    stageAvailable, splitLive, reportError: (error) => shellAppReporter().reportError(error),
    isPeerWindow: itemSubject != null, helpOpen, setHelpOpen, launcherOpen, setLauncherOpen,
    recentSwitcher: () => recentSwitcher, peerViewSwitcher: () => peerViewSwitcher,
  };

  const panes = createShellPaneVisibility({
    windowSubject: itemSubject, splitLive,
    presentedSplitLive: () => presentedSplitLive(),
    presentedStageId: () => presentedStageColumn().stageId,
  });
  const { windowNavUserCollapsed, windowNavCollapsed, navAutomaticallyCollapsed, navSlotWidthCss, showConversation, hideConversation, contextHidden, revealConversation, splitFitClaim, conversationHidden, stageIsLeading, stageHeaderLeadsWindow, stageEdges} = panes;

  let layoutTriggerEl: HTMLButtonElement | undefined;
  const setPendingRegionFocus = createShellFocusRequest();
  const search = createShellSearchState({
    ...scope, showSearch: () => setNav("search"), closeLauncher: () => setLauncherOpen(false),
    routeToSearch: (open) => routeToStage("search", open),
    searchIsSplitCompanion: () => companionStageId() === "search" && splitLive(),
  });
  const { searchArgs,
    closeSearch, openSearch, closeCrossbar, openCrossbar} = search;
  const [foldersPanelProjectId, setFoldersPanelProjectId] = createSignal<
    string | null
  >(null);
  const { dismissDraftPromoteBanner, dismissNoFolderBanner,
    observePresentation: observeProjectNudges } = createShellProjectNudges({
    ...scope, identityReady: () => identityReady(), windowNavCollapsed,
  });
  const workerDrawer = createShellWorkerDrawer({
    ...scope, revealConversation, resumeSession: (row, options) => resumeSession(row, options),
    navigateFromSearch: (target) => navigateFromSearch(target),
    revealFromSearch: (reveal, projectId, sessionId) => revealFromSearch(reveal, projectId, sessionId),
  });
  const { observeActiveChat,
    openWorkers, closeWorkers, openNotificationSession, revealInTranscript} = workerDrawer;
  const { modelsChecked, showOnboardingGate, suppressShellForFirstRun,
    providerGap, showNoProviderBanner, observeProviders, observeBootFallback } =
    createShellOnboarding(props.appStore, props.projects, props.settingsStore);

  installShellNavigationSinks({
    ...scope, openStageWithPlacement: (stage, arrival, open) => openStageWithPlacement(stage, arrival, open),
    openSearch, openNotificationSession, revealConversation,
    resumeSession: (row, options) => resumeSession(row, options), openWorkers,
    closeSearch, closeCrossbar, setNav, openSettings,
    selectContextSection: (section) => selectContextSection(section), showNoProviderBanner,
  });

  const {
    projectPresentation,
    identityReady,
    workspaceMounted,
    workspaceOpen,
    workspacePending,
    workspaceRevealed,
    workspaceVeilKey,
    workspaceVeilLabel,
    workspaceVeilNotice,
    sidebarChatsPublished,
    railStatusProject,
    showHome,
  } = createShellWorkspacePresentation(scope);

  const projectDirForChat = () =>
    props.appStore.state.currentSession?.workspace_path?.trim() ?? "";

  const { attentionIndex, recentChatEntries } = createShellAttention({
    shell: props.shell, appStore: props.appStore, attention: props.attention,
    projects: props.projects, recents: props.recents, presentedChat, workspaceMounted,
    conversationShown: () => conversationShown(),
  });

  const { navigateFromSearch, revealFromSearch, crossbarGotoTargets, navigateFromCrossbarGoto } = createShellSearchNavigation({
    ...scope, closeSearch, closeLauncher: () => setLauncherOpen(false),
    openProject: (id) => openProject(id), resumeSession: (row, options) => resumeSession(row, options),
    openWorkers, attentionIndex, securityScannersFeatureOn, routeToStage, selectSettingsSection, openSettings,
    openStageWithPlacement: (stage, arrival) => openStageWithPlacement(stage, arrival),
    openConfiguration: () => openConfiguration(), goHome: () => goHome(),
  });

  const sessionNavigation = createSessionNavigation({
    ...scope,
    conversationInLayout: () => conversationInLayout(), revealConversation, closeSearch, closeCrossbar,
    closeLauncher: () => setLauncherOpen(false),
    focusComposer: () => setPendingRegionFocus("composer"),
  });
  const {
    onFreshSession,
    sidebarSections,
    renameSession,
    openProject,
    newProject,
    goHome,
    sidebarRowAsRecent,
    resumeSession,
    beginWorkspaceOpening,
    abandonWorkspaceOpening,
    startEmptySession,
    startNewChat,
  } = sessionNavigation;

  const {
    sendPrompt,
    resumeSpendLimitedChat,
    stopChat,
  } = createShellChatActions({ ...scope, projectDirForChat });

  const openBudgetsSettings = () =>
    routeToStage("settings", () => openSettings("debug"));

  createEffect(() => {
    const status = props.appStore.state.sidecarStatus;
    if (status === "connected") {
      props.shell.setConnection("connected");
    } else if (status === "disconnected" || status === "reconnecting") {
      props.shell.setConnection("offline");
    }
  });

  observeProviders();

  const openProvidersSettings = () => openSettings("providers");

  observeBootFallback();

  observeActiveChat();

  onMount(() => {
    onCleanup(registerShellDismissCommand({
      ...scope, ...search, ...navigation, ...workerDrawer, layoutTrigger: () => layoutTriggerEl,
    }));
    registerShellCommands({
      ...scope, ...search, ...panes, ...sessionNavigation, openSettings, stopChat, applyStagePlacement, stageColumnStageId,
      openFocusedSubjectInNewWindow, sidebarSections, layoutTargetStage, conversationInLayout,
      setPendingRegionFocus, goToContextStage, showChats: () => setNav("chats"),
      focusWindowByNumber: (n) => peerWindows.focusWindowByNumber(n),
      cycleNextPeerWindow: () => peerWindows.cycleNextPeerWindow(),
      raiseSessionWindow: (id, v) => peerWindows.raiseSessionWindow(id, v),
      raiseFileWindow: (path, rootId) => peerWindows.raiseFileWindow(path, rootId),
      sessionPeer: (id, v) => peerWindows.sessionPeer(id, v),
      isThisSessionWindow: (id) => peerWindows.isThisSessionWindow(id),
      filePeer: (path, rootId) => peerWindows.filePeer(path, rootId),
    });
  });

  const openKeyboardSettings = () => {
    setHelpOpen(false);
    closeCrossbar();
    closeSearch();
    openSettings("general", { general: "keyboard" });
  };

  const openProjectTrustSettings = () => {
    closeSearch();
    openSettings("debug", { advanced: "project_trust" });
  };

  const openExtensionsSettings = () => {
    closeSearch();
    openSettings("extensions");
  };

  const showChatTabRail = () =>
    nav() === "projects" && workspaceMounted() && presentedChat() != null;

  const placeableStageId = (): ContextNavItemId | null => {
    const n = nav();
    return isContextNavItemId(n) ? n : null;
  };

  const stageColumn = createMemo(() => {
    const projectId = props.shell.state.activeProjectId;
    const splitMode = projectId != null && stagePlacementMode() === "split";
    return resolveStageColumn({
      navStage: placeableStageId(),
      foregroundIsChat: nav() === "projects",
      hasChat: chatPresence().present,
      companion: splitMode
        ? resolveSplitCompanion(companionPref(), stageAvailable)
        : null,
      splitMode,
    });
  });

  /** This window's layout holds a conversation column, shown or hidden. */
  const conversationInLayout = () => nav() === "projects" || splitLive();

  const stageColumnStageId = (): ContextNavItemId | null =>
    stageColumn().stageId;

  const stageEntryActive = (id: ContextNavItemId) =>
    stageColumnStageId() === id;

  const { chatStack, stageStack, presentedChatCurrent, stageHandoff, stageOpeningOverChat,
    presentedSplitLive, showStageColumn,
    presentedStageColumn, markStageSurfaceReady, stageColDual, chatColDual } = createShellResidency({
    ...scope, showHome, placeableStageId, stageColumn, showChatTabRail, presentedChat,
    workspaceMounted, projectPresentation,
  });

  createEffect(() => {
    setSplitProjectId(props.shell.state.activeProjectId);
  });

  const publishedChatWidthPx = () => effectiveChatWidthPx(splitHostWidthPx());

  const conversationAttention = () => {
    const chat = activeChat();
    return chat ? attentionIndex().get(chat.sessionId) : undefined;
  };

  const splitColumns = createMemo(
    () =>
      splitColumnsOnScreen({
        splitLive: presentedSplitLive(),
        hidden: hiddenSplitPanePref(),
        hostWidthPx: splitHostWidthPx(),
        narrowSurvivor: narrowSurvivorPref(),
      }),
    undefined,
    {
      equals: (a, b) => a.conversation === b.conversation && a.stage === b.stage,
    },
  );
  /** The conversation is legible in this window, not merely part of its layout. */
  const conversationShown = () =>
    conversationInLayout() && splitColumns().conversation;

  const layoutTrayStageId = (): ContextNavItemId | null => {
    const projectId = props.shell.state.activeProjectId;
    const stageId = layoutTargetStage();
    if (!projectId || !stageId) return null;
    return stageId;
  };

  /** Full-stage views take the chat's place; opening one closes any search. */
  const showStage = (stage: Exclude<Nav, "projects" | "search">) => {
    closeSearch();
    setNav(stage);
  };
  const stageOpeners: Record<ContextNavItemId, () => void> = {
    search: () =>
      openSearch({ originProjectId: props.shell.state.activeProjectId }),
    files: () => showStage("files"),
    security: () => showStage("security"),
    cost: () => showStage("cost"),
    artifacts: () => showStage("artifacts"),
    blueprints: () => showStage("blueprints"),
    extensions: () => showStage("extensions"),
  };

  const { detachStageToWindow, openFocusedSubjectInNewWindow, openFileWindow,
    openFileInNewWindow, beginFileWindowDrag, closeStageWindows, focusedPeerSubject, ...peerWindows } = createShellPeerWindows({
      ...scope, itemSubject, stageColumnStageId, placeableStageId, splitLive, openStage: (stage) => stageOpeners[stage](),
    });
  const { focusedPeerViews } = peerWindows;

  const { crossbarActions } = createShellContributions({
    shell: props.shell, appStore: props.appStore, activeChat, readyChat, nav,
    windowNavCollapsed, contextHidden, splitLive, stageColumnStageId,
    stageFeatureFlags, focusedPeerSubject, focusedPeerViews,
    openStageWithPlacement: (stage, arrival) => openStageWithPlacement(stage, arrival),
    openCommand: (commandId) => openCrossbar({ originProjectId: props.shell.state.activeProjectId, commandId }),
    windowFocused,
  });

  const layoutTargetStage = (): ContextNavItemId | null => {
    if (!props.shell.state.activeProjectId) return null;
    return resolveSplitCompanion(
      placeableStageId() ?? companionPref(),
      stageAvailable,
    );
  };

  const { claimStageChrome, applyStagePlacement, stageChromeDeficitPx } = createShellStagePlacement({
    ...scope, ...search, windowNavUserCollapsed, navigationIntent, startNewChat,
    sidebarSections,
    resumeConversation: (row) => resumeSession(sidebarRowAsRecent(row)),
    openStage: (stage) => stageOpeners[stage](),
    suppressShellForFirstRun, chatReady: () => stageScope().phase === "ready",
  });

  /** Context navigation is always workspace-local; peer windows are explicit. */
  const goToContextStage = (stage: ContextNavItemId) => openStageWithPlacement(stage, "deliberate");

  const openStageWithPlacement = (
    stage: ContextNavItemId,
    arrival: "routed" | "deliberate",
    open: () => void = stageOpeners[stage],
  ) => {
    if (hiddenSplitPanePref() === "stage") void panes.showContext();
    else void claimStageChrome(stagePlacementMode() === "split", stage);
    // Companion navigation preserves the split layout.
    if (props.shell.state.activeProjectId && stagePlacementMode() === "split" && nav() === stage) return;
    if (arrival === "routed") routeToStage(stage, open);
    else open();
  };

  const settingsProjectDir = () =>
    projectDirForChat() ||
    activeProject()?.roots.find((r) => r.is_primary)?.path ||
    activeProject()?.roots[0]?.path ||
    undefined;

  const { backFor, sourceBack, titlebarBack, stageBack } = createShellWorkspaceContext({
    ...scope, routedReturn, showConversation, splitFitClaim, closeWorkers, presentedChat,
    projectPresentation, conversationShown, splitColumns, stageColumnStageId, stageHandoff,
    reopenFiles: () => openStageWithPlacement("files", "routed"),
  });

  const openConfiguration = () => {
    closeSearch();
    if (nav() === "context") {
      setNav("projects");
      return;
    }
    openContextSection(DEFAULT_PROJECT_CONTEXT_SECTION);
  };

  const selectContextSection = (section: ProjectContextSection) => {
    closeSearch();
    openContextSection(section);
  };

  const { toggleStar, renameProject, dialogError, moveTarget, setMoveTarget, moveBusy, moveError, setMoveError, openMoveDialog, submitMove, activeProjectRoots, retryActiveProjectPromotion, cancelActiveProjectPromotion, addFolderToActiveProject, detachFolderFromActiveProject, detachProjectRoot } = createProjectLifecycle({
    projects: props.projects,
    activeProjectId: () => props.shell.state.activeProjectId,
    activeProject,
    report: (projectId, error) => noticeReporterFor(projectScope(projectId)).reportError(error),
  });

  const { cloneOpen, cloneBusy, cloneError, setCloneError, setCloneOpen,
    openFolderPath, openFolderDetect, openFolderBusy,
    startOpenFolder, proposeFolderAsProject, confirmOpenFolder, closeOpenFolder, submitClone, openCloneDialog,
  } = createProjectOpening({
    formatError: dialogError,
    shell: props.shell, projects: props.projects, showProjects: () => setNav("projects"),
    beginWorkspaceOpening, abandonWorkspaceOpening,
    reportError: (error) => shellAppReporter().reportError(error), startSession: startEmptySession,
  });

  // External opens use the folder-open path.
  onMount(() => {
    onCleanup(
      onCLIOpenEvent((ev) => {
        void focusAppWindow();
        if (ev.action === "open" && ev.project_id) {
          openProject(ev.project_id);
          return;
        }
        if (ev.action === "create") {
          void proposeFolderAsProject(ev.path);
        }
      }),
    );
  });

  const { showPromoteNudge, showNoFolderBanner } = observeProjectNudges();

  const { projectRemovalReview, deleteProject } = createWorkspaceProjectRemoval(props);

  /** Notifications appear on Home and in the chat dock. */
  const notificationStack = () => (
    <NotificationStack
      index={noticeIndex()}
      activeProjectId={props.shell.state.activeProjectId ?? undefined}
      projectName={(projectId) => props.projects.summary(projectId)?.displayName}
      onOpenProject={openProject}
      onDismiss={(id) => props.notices.dismiss(id)}
      announceLive={!isAnySessionActivityLive(props.appStore)}
    />
  );

  const railStatusSessionId = (projectId: string): string => {
    const chat = presentedChat();
    return chat?.projectId === projectId ? chat.sessionId : "";
  };


  return (
    <ShellOnboardingGate
      appStore={props.appStore} settingsStore={props.settingsStore}
      gate={{ modelsChecked, showOnboardingGate, suppressShellForFirstRun }}
    >
    <div
      class="den-shell"
      classList={{
        "den-shell-nav-collapsed": windowNavCollapsed(),
        "den-shell-nav-collapsed--automatic": navAutomaticallyCollapsed(),
        "den-shell--orientation-mirrored":
          workspaceOrientationPref() === "mirrored",
      }}
      data-testid="shell"
      data-sidecar-status={props.appStore.state.sidecarStatus}
      data-contributions-ready={contributionFrameReady() ? "true" : "false"}
      data-workspace-pending={workspacePending()}
      data-workspace-phase={stageScope().phase}
      data-workspace-open={workspaceOpen()}
      data-workspace-revealed={workspaceRevealed()}
    >
      <WorkspaceSidebar
        {...scope} {...navigation} {...panes} {...sessionNavigation}
        settingsStore={props.settingsStore} costStore={props.costStore}
        notices={props.notices} peerWindows={peerWindows}
        projectLifecycle={{ activeProjectRoots, addFolderToActiveProject, detachFolderFromActiveProject,
          retryActiveProjectPromotion, cancelActiveProjectPromotion, openMoveDialog }}
        homeSection={homeSection} setHomeSection={setHomeSection} homeSummaries={homeSummaries}
        search={search} sidebarSections={sidebarSections} sidebarChatsPublished={sidebarChatsPublished}
        attentionIndex={attentionIndex} identityReady={identityReady} workspaceRevealed={workspaceRevealed}
        onFreshSession={onFreshSession} renameSession={renameSession}
        showStage={showStage} goToContextStage={goToContextStage} stageEntryActive={stageEntryActive}
        detachStageToWindow={detachStageToWindow} openConfiguration={openConfiguration}
        conversationInLayout={conversationInLayout} railStatusProject={railStatusProject}
        railStatusSessionId={railStatusSessionId} layoutTrayStageId={layoutTrayStageId}
        stageColumnStageId={stageColumnStageId} stageChromeDeficitPx={stageChromeDeficitPx}
        applyStagePlacement={applyStagePlacement} closeStageWindows={closeStageWindows}
        presentedSplitLive={presentedSplitLive} layoutTriggerRef={(el) => (layoutTriggerEl = el)}
      />
      {/* Home alone uses the stage wash. */}
      <StageSplitHost
        splitLive={presentedSplitLive()}
        stageColumnOccupied={showStageColumn()}
        chatColumnOccupied={
          presentedChatCurrent() != null || chatStack.pending() != null
        }
        stageOnLeft={stageIsLeading()}
        conversationHidden={conversationHidden()}
        contextHidden={contextHidden()}
        narrowSurvivor={narrowSurvivorPref()}
        chatWidthPx={publishedChatWidthPx()}
        washed={showHome()}
        stageColDual={stageColDual()}
        chatColDual={chatColDual()}
        onStageEnter={() => setSplitFocusRegion("stage")}
        onChatEnter={() => setSplitFocusRegion("chat")}
        onWidth={setSplitHostWidth}
        divider={
          <SplitDivider
            availableWidthPx={layoutViewportWidthPx}
            chatWidthPx={publishedChatWidthPx}
            onBegin={() =>
              beginChatWidthResize(layoutViewportWidthPx(), hideConversation)
            }
            onReset={() => {
              void resetChatWidthPx();
            }}
            stageOnLeft={stageIsLeading}
          />
        }
        stageColumn={
          <ShellStageColumn
            occupied={showStageColumn()}
            stageId={stageColumnStageId()}
            stack={stageStack}
            headerLeadsWindow={stageHeaderLeadsWindow()}
            back={titlebarBack()}
            edges={stageEdges(conversationAttention())}
            notifications={showHome() ? notificationStack() : null}
            waitingVisible={!stageOpeningOverChat()}
            preparation={props.shell.state.activeProjectId ? workspacePreparation(props.shell.state.activeProjectId) : undefined}
            onReady={markStageSurfaceReady}
          >
            {(key) => <WorkspaceStage surfaceKey={key}
              shell={props.shell} appStore={props.appStore} projects={props.projects} recents={props.recents}
              settingsStore={props.settingsStore} costStore={props.costStore} attention={props.attention}
              activeChat={activeChat} homeSection={homeSection} setHomeSection={setHomeSection}
              homeSummaries={homeSummaries} attentionIndex={attentionIndex} navigation={navigation}
              searchArgs={searchArgs} navigateFromSearch={navigateFromSearch} sessionNavigation={sessionNavigation}
              projectLifecycle={{ toggleStar, renameProject, openMoveDialog }}
              peerWindows={{ openFileInNewWindow, openFileWindow, beginFileWindowDrag, detachStageToWindow }}
              workspaceContext={{ backFor, stageBack }} providerGap={providerGap}
              showConversation={showConversation} startOpenFolder={startOpenFolder} openCloneDialog={openCloneDialog}
              deleteProject={deleteProject} setFoldersPanelProjectId={setFoldersPanelProjectId}
              settingsProjectDir={settingsProjectDir} setSecurityScannersFeature={setSecurityScannersFeature}
              selectContextSection={selectContextSection} openProjectTrustSettings={openProjectTrustSettings}
              openExtensionsSettings={openExtensionsSettings} openStageWithPlacement={openStageWithPlacement}
              openSearch={openSearch} revealInTranscript={revealInTranscript}
            />}
          </ShellStageColumn>
        }
        chatColumn={
          <ShellChatColumn
            stack={chatStack}
            sourceBack={!showStageColumn() ? sourceBack() : null}
            stageOpening={stageOpeningOverChat()}
            preparation={workspacePreparation}
          >
            {(chat, presence) => (
              <ChatWorkspace
                shell={props.shell} appStore={props.appStore} recents={props.recents} projects={props.projects}
                settingsStore={props.settingsStore} costStore={props.costStore} notices={props.notices}
                workerDrawer={workerDrawer} panes={panes}
                actions={{ sendPrompt, stopChat, resumeSpendLimitedChat }}
                onboarding={{ showNoProviderBanner, providerGap }}
                projectDirForChat={projectDirForChat} nav={nav} splitLive={splitLive}
                notificationStack={notificationStack} showPromoteNudge={showPromoteNudge}
                showNoFolderBanner={showNoFolderBanner} onPromote={openMoveDialog}
                dismissDraftPromoteBanner={dismissDraftPromoteBanner}
                dismissNoFolderBanner={dismissNoFolderBanner} addFolderToActiveProject={addFolderToActiveProject}
                onOpenBudgets={openBudgetsSettings} onOpenProviders={openProvidersSettings}
                onOpenTests={() => routeToStage("context", () => selectContextSection("tests"))}
                onOpenFiles={stageOpeners.files}
                chat={chat}
                pending={() => presence() === "pending"}
                surfaceActive={() => presence() !== "idle"}
              />
            )}
          </ShellChatColumn>
        }
      />
      <Show when={workspaceVeilKey()} keyed>
        <WorkspaceOpeningVeil visible={!workspaceRevealed()}>
          <ProjectLoadingStage
            label={workspaceVeilLabel()}
            hint={workspaceVeilNotice()?.message}
            tone={workspaceVeilNotice()?.tone}
          />
        </WorkspaceOpeningVeil>
      </Show>
      <WorkspaceOverlays
        shell={props.shell} appStore={props.appStore} projects={props.projects}
        launcherOpen={launcherOpen} setLauncherOpen={setLauncherOpen}
        helpOpen={helpOpen} setHelpOpen={setHelpOpen}
        openProject={openProject} newProject={newProject} goHome={goHome} setHomeSection={setHomeSection}
        search={search} crossbarGotoTargets={crossbarGotoTargets} crossbarActions={crossbarActions}
        navigateFromCrossbarGoto={navigateFromCrossbarGoto} navigateFromSearch={navigateFromSearch}
        proposeFolderAsProject={proposeFolderAsProject} recentChatEntries={recentChatEntries}
        recentSwitcherRef={(handle) => (recentSwitcher = handle)}
        peerViewSwitcherRef={(handle) => (peerViewSwitcher = handle)} focusedPeerViews={focusedPeerViews}
        openKeyboardSettings={openKeyboardSettings} navSlotWidthCss={navSlotWidthCss}
      />
      <ProjectDialogs
        projects={props.projects} activeProject={activeProject}
        review={projectRemovalReview}
        lifecycle={{ moveTarget, moveBusy, moveError, setMoveError, submitMove, setMoveTarget, dialogError, detachProjectRoot }}
        opening={{ cloneOpen, cloneBusy, cloneError, setCloneError, setCloneOpen, submitClone,
          openFolderPath, openFolderDetect, openFolderBusy, confirmOpenFolder, closeOpenFolder }}
        foldersPanelProjectId={foldersPanelProjectId} setFoldersPanelProjectId={setFoldersPanelProjectId}
      />

    </div>
    </ShellOnboardingGate>
  );
}
