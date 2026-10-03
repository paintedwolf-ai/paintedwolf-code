import { registerShellCommands } from "./shell-commands.ts";
import { registerShellDismissCommand } from "./shell-dismiss-command.ts";
import { createShellWorkspaceContext } from "./shell-workspace-context.ts";
import { createShellResidency } from "./shell-residency.ts";
import { createShellNavigationState, type Nav } from "./shell-navigation-state.ts";
import { createShellOnboarding } from "./shell-onboarding.ts";
import { createShellProjectNudges } from "./shell-project-nudges.ts";
import { createShellWorkerDrawer } from "./shell-worker-drawer.ts";
import { installShellNavigationSinks } from "./shell-navigation-sinks.ts";
import { createShellSearchNavigation } from "./shell-search-navigation.ts";
import { createShellSearchState } from "./shell-search-state.ts";
import { shellSearchTargets } from "./shell-search-targets.ts";
import { createShellStagePlacement } from "./shell-stage-placement.ts";
import { createShellPaneVisibility } from "./shell-pane-visibility.ts";
import { createShellWorkspacePresentation } from "./shell-workspace-presentation.ts";
import type { ShellScope } from "./shell-scope.ts";
import { createShellChatActions } from "./shell-chat-actions.ts";
import { createShellPeerWindows } from "./shell-peer-windows.ts";
import { createShellContributions } from "./shell-contributions.ts";
import { pickProjectFolder } from "../../platform/files/folder.ts";
import { ProjectRemovalDialog, createProjectRemovalReview } from "./ProjectRemovalDialog.tsx";
import { createSessionNavigation } from "./session-navigation.ts";
import { createProjectLifecycle } from "./project-lifecycle.ts";
import { createProjectRemoval } from "./project-removal.ts";
import type { LycaonClient } from "../../api/client.ts";
import { Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { CONTEXT_NAV_CATALOG, type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { beginSessionSnapshotRead } from "../../api/session-snapshot-refresh.ts";
import type { Project } from "../../api/types.ts";
import { withoutReadFinish } from "../../attention/attention-model.ts";
import { createSeenMarker } from "../../attention/seen-marker.ts";
import { captureUnreadBoundary } from "../../attention/unread-boundary.ts";

import { registerComposerAttachmentSink } from "../../chat/composer/add-to-chat.ts";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";

import { composerComposeBlockReason } from "../../chat/composer/composer-rules.ts";
import { coordinatorModelVisionSupport } from "../../chat/composer/composer-vision.ts";
import { hasSpendRecoveryDraft } from "../../chat/recovery/spend-ceiling-recovery.ts";
import { buildMruEntries, createMruTracker } from "../../chat/session/mru-switcher-model.ts";

import { isAnySessionActivityLive, isChatActivityLive } from "../../chat/session/session-activity.ts";
import { rememberSessionChatFromStore, restoreCachedSessionChat } from "../../chat/session/session-chat-cache.ts";
import { recordCreatedSessionInRecents } from "../../chat/session/session-lifecycle.ts";
import { deleteSession, moveSessionPin, setSessionArchived, setSessionPinned } from "../../chat/session/session-retirement.ts";
import { isPendingSessionId } from "../../chat/session/session-scope.ts";
import { type SessionSwitchDeps } from "../../chat/session/session-switch.ts";

import { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import { completeChatHydrationAndReplay } from "../../chat/transcript/projection/message-events.ts";
import { hasDenTranscriptMessages } from "../../chat/transcript/projection/message-transcript.ts";
import { contributionFrame, contributionFrameReady, invalidateContributionFrame } from "../../contributions/contribution-store.ts";

import { type ShellFactState } from "../../contributions/shell-facts.ts";
import { filesCommandContext } from "../../files/commands/files-command-context.ts";
import { mountedFilesEditorView } from "../../files/editor/files-editor-host.ts";
import { EditorCommandBar } from "../../find/EditorCommandBar.tsx";

import { editorCommandBarPlacement } from "../../find/find-controller.ts";
import { DEFAULT_HOME_SECTION, draftCount, retainedHomeSummaries, type HomeSection } from "../../home/home-model.ts";
import { forgetProjectThumbnail } from "../../home/thumbnail-store.ts";
import { setShellNoticeActionSinks } from "../../notices/notice-actions.ts";
import { APP_SCOPE, projectScope, sessionScope } from "../../notices/notice-scope.ts";
import { hasSharedNotices, selectSessionNotices } from "../../notices/notice-select.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { getEntityRetire, getLycaonClient, noticeReporterFor, onCLIOpenEvent } from "../../platform/connection/app-connection.ts";

import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { closeDiffViewer, diffViewerRequest } from "../../platform/navigation/in-app-diff.ts";
import { cancelItemWindowDrag, closeCurrentPeerWindow, closeItemWindow, finishItemWindowDrag, focusItemWindow, moveItemWindowDrag } from "../../platform/windows/item-windows.ts";
import { resolveSourceRequest } from "../../platform/navigation/open-source.ts";
import { isTauriRuntime, tauriDragRegionProps } from "../../platform/runtime.ts";

import { focusAppWindow, windowFocused } from "../../platform/windows/window-chrome.ts";
import { windowSubject } from "../../platform/windows/window-subject.ts";
import { focusEditorWindow } from "../../platform/windows/editor-windows.ts";
import { setCurrentWorkspaceContexts } from "../../platform/windows/workspace-view-registry.ts";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { searchHitToNavTarget } from "../../search/search-hit-nav.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import { type CrossbarGotoTarget } from "../../search/crossbar-model.ts";

import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import { chatListSortPref, saveChatListSort } from "../../settings/chat/chat-list-prefs.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";
import { filesTreeCollapsed } from "../../files/tree/files-tree-window-state.ts";
import { DEFAULT_PROJECT_CONTEXT_SECTION, type ProjectContextSection } from "../../settings/settings-nav-model.ts";
import { findSettingDefinition } from "../../settings/settings-registry.ts";
import { requestSettingReveal } from "../../settings/settings-reveal.ts";

import { createVerifyTestSuggestion } from "../../settings/extensions/verify-test-suggestion.ts";
import { dismissLayoutDockOnOutsidePress } from "../../shell/layout-dock-dismiss.ts";
import { beginChatWidthResize, hiddenSplitPanePref, companionPref, companionStageId, currentSplitFocusRegion, effectiveNavWidthPx, effectiveChatWidthPx, narrowSurvivorPref, resetChatWidthPx, setSplitFocusRegion, setSplitHostWidth, setSplitProjectId, splitHostWidthPx, stagePlacementMode, toggleWorkspaceOrientation, swapSplitColumns, workspaceOrientationPref } from "../../shell/layout-store.ts";
import { isHomePresentation } from "../../shell/project-presentation.ts";

import { resolveSplitCompanion, resolveStageColumn, splitColumnsOnScreen } from "../../shell/stage-placement.ts";
import { deriveStageScope, holdChatPresence, holdPresentedChat, stabilizeActiveChat, type ActiveChat, type ChatPresence } from "../../shell/stage-scope.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import { attachDispatcher, invokeCommand, isComposerFocused, setFilesStageActive } from "../../shortcuts/dispatcher.ts";
import { focusRegion, focusRegionsVersion, isFocusRegionMounted, isRegionEntry, mountedFocusRegions, regionEntryTarget, registerFocusRegion, releaseFocusRegion, type FocusRegionId } from "../../shortcuts/focus-region.ts";
import { resolvePeerOpenSubject, workspaceKindOf, type PeerOpenSubject } from "../../shortcuts/peer-view-subject.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { AttentionStore } from "../../store/attention-store.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { sessionSummaryFromSession, type ProjectSessionsStore } from "../../store/project-sessions-store.ts";
import { sidebarChatSections, sidebarNavigationOrder } from "../../store/projects-sidebar-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { createRecentsStore } from "../../store/recents-store.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { createShellStore } from "../../store/shell-store.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { SURFACE_HOME, SURFACE_SETTINGS, parseStageSurfaceKey } from "../../ui/resident-surfaces.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { ChatView } from "../chatview/ChatView.tsx";
import { ComposerStatusChips } from "../chatview/ComposerStatusChips.tsx";
import { FirstTimeTipsLayer } from "../first-time-tips/FirstTimeTipsLayer.tsx";
import { CloneRepoDialog } from "../home/CloneRepoDialog.tsx";
import { DraftPromoteBanner } from "../home/DraftPromoteBanner.tsx";
import { HomeNav } from "../home/HomeNav.tsx";
import { HomeView } from "../home/HomeView.tsx";
import { MoveToProjectDialog } from "../home/MoveToProjectDialog.tsx";
import { NoFolderBanner } from "../home/NoFolderBanner.tsx";
import { OpenFolderConfirm } from "../home/OpenFolderConfirm.tsx";
import { ChatTabRail } from "../nav/ChatTabRail.tsx";
import { ChatTopChromeStack } from "../nav/ChatTopChromeStack.tsx";
import { FocusedProjectNav } from "../nav/FocusedProjectNav.tsx";
import { NavBrandIdentity } from "../nav/NavBrandIdentity.tsx";
import { NavFold } from "../nav/NavFold.tsx";
import { NavResizeHandle } from "../nav/NavResizeHandle.tsx";
import { NavSidebarCollapseButton } from "../nav/NavSidebarToggle.tsx";
import { NoProviderCard } from "../NoProviderCard.tsx";
import { NoticeRail } from "../NoticeRail.tsx";
import { NotificationStack } from "../NotificationStack.tsx";

import { OnboardingGate } from "../onboarding/OnboardingGate.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { clearFilesHotExitSuppress, suppressFilesHotExitWrite } from "../../files/documents/files-hot-exit.ts";
import { selectedFileVersion } from "../../files/history/files-version-selection.ts";
import { prepareFilesRootDetach } from "../../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";

import { ProjectChatsView } from "../project/ProjectChatsView.tsx";
import { ProjectContextView } from "../project/ProjectContextView.tsx";
import { ProjectLauncher } from "../project/ProjectLauncher.tsx";
import { Crossbar } from "../search/Crossbar.tsx";
import { SettingsNavSidebar } from "../settings/SettingsNavSidebar.tsx";
import { SettingsView } from "../settings/SettingsView.tsx";
import { ShortcutHelpOverlay } from "../shortcuts/ShortcutHelpOverlay.tsx";
import { DiffViewerDrawer } from "../source/diff/DiffViewerDrawer.tsx";
import { SpendCeilingApproachingNudge, spendCeilingApproachingVisible } from "../SpendCeilingApproachingNudge.tsx";
import { SpendCeilingReachedNudge, spendCeilingReachedNotice } from "../SpendCeilingReachedNudge.tsx";
import { stageDefinition, stageFeatureEnabled, stageLabelFor, type StageFeatureFlags } from "../stage/stage-registry.tsx";
import { VerifyTestNudge } from "../VerifyTestNudge.tsx";
import { WorkspaceFoldersPanel } from "../WorkspaceFoldersPanel.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { ContextDrawerHost } from "./ContextDrawer.tsx";
import { ShellNavRail } from "./ShellNavRail.tsx";
import { LayoutDockButton, LayoutDockTray } from "./LayoutDock.tsx";
import { PeerViewSwitcher, type PeerViewSwitcherHandle } from "./PeerViewSwitcher.tsx";
import { createProjectOpening } from "./project-opening.ts";
import { ProjectLoadingStage } from "./ProjectLoadingStage.tsx";
import { RecentChatSwitcher, type RecentChatSwitcherHandle } from "./RecentChatSwitcher.tsx";
import {
  SessionProtectionBanners,
  sessionProtectionPresent,
  type ProtectionBannerId,
} from "./SessionProtectionBanners.tsx";
import { SplitDivider } from "./SplitDivider.tsx";
import { StageErrorBoundary } from "./StageErrorBoundary.tsx";
import { StageSplitHost } from "./StageSplitHost.tsx";
import { ShellChatColumn, ShellStageColumn } from "./ShellColumns.tsx";
import { WorkspaceOpeningVeil } from "./WorkspaceOpeningVeil.tsx";

/** Entering the sidebar lands on the current chat, then any chat, then any rail control. */
const SIDEBAR_ENTRY_PRIORITY = [
  '.focused-session-list__row[aria-current="true"]',
  ".focused-session-list__row",
  ".den-shell-aside button",
] as const;

type ProjectsStore = ReturnType<typeof createProjectsStore>;
type RecentsStore = ReturnType<typeof createRecentsStore>;
type ShellStore = ReturnType<typeof createShellStore>;

function projectPromotionPending(
  project: Pick<Project, "promotion"> | null | undefined,
): boolean {
  return project?.promotion != null && project.promotion.phase !== "committed";
}

type Props = {
  shell: ShellStore;
  recents: RecentsStore;
  projects: ProjectsStore;
  /** Attention across all projects. */
  attention: AttentionStore;
  /** Host chat inventory for the active project. */
  projectSessions: ProjectSessionsStore;
  appStore: AppStore;
  /** Scope-keyed notice state. */
  notices: NoticeStore;
  settingsStore: SettingsStore;
  costStore: CostStore;
};

export function Shell(props: Props) {
  const itemSubject = windowSubject();
  /** App-scoped reporter for shell conditions that name no project or chat. */
  const shellAppReporter = () => noticeReporterFor(APP_SCOPE);
  const noticeIndex = () => props.notices.index();

  const navigation = createShellNavigationState(
    () => props.shell.state.activeProjectId,
    () => { if (hiddenSplitPanePref() === "stage") void panes.showContext(); },
  );
  const { nav, setNav, navigationIntent, dockTray, closeLayoutTray,
    toggleLayoutTray, closeSettingsNav, routedReturn, routeToStage,
    settingsSection, generalInitialTab, advancedInitialTab, contextSection,
    selectSettingsSection, openSettings, openContextSection } = navigation;

  const activeProject = createMemo(() => {
    const id = props.shell.state.activeProjectId;
    return id ? props.projects.byId(id) : undefined;
  });

  const stageScope = createMemo(() =>
    deriveStageScope(props.shell.state, props.appStore.state),
  );

  const activeChat = (): ActiveChat | null => {
    const fg = props.shell.state.foreground;
    if (!fg?.projectId || !fg.sessionId || isPendingSessionId(fg.sessionId)) {
      return null;
    }
    return { projectId: fg.projectId, sessionId: fg.sessionId };
  };

  // Stable scope ids retain chat identity.
  const readyChat = createMemo((prev: ActiveChat | null | undefined): ActiveChat | null => {
    const scope = stageScope();
    return stabilizeActiveChat(
      prev,
      scope.project_id,
      scope.session_id,
      scope.phase === "ready" &&
        contributionFrameReady(),
    );
  });

  const [securityScannersFeatureOn, setSecurityScannersFeatureOn] = createSignal(true);
  const stageFeatureFlags = (): StageFeatureFlags => ({
    securityScannersEnabled: securityScannersFeatureOn(),
  });
  const stageAvailable = (id: ContextNavItemId): boolean =>
    stageFeatureEnabled(id, stageFeatureFlags());
  const splitLive = () => stageColumn().splitLive;

  const [launcherOpen, setLauncherOpen] = createSignal(false);
  const [helpOpen, setHelpOpen] = createSignal(false);
  let recentSwitcher: RecentChatSwitcherHandle | undefined;
  let peerViewSwitcher: PeerViewSwitcherHandle | undefined;

  const scope: ShellScope = {
    shell: props.shell, appStore: props.appStore, projects: props.projects, recents: props.recents,
    projectSessions: props.projectSessions, activeProjectId: () => props.shell.state.activeProjectId,
    activeProject, activeChat, readyChat, stageScope, nav, setNav, showProjects: () => setNav("projects"),
    stageAvailable, splitLive, reportError: (error) => shellAppReporter().reportError(error),
    isPeerWindow: itemSubject != null, helpOpen, setHelpOpen, launcherOpen, setLauncherOpen,
    recentSwitcher: () => recentSwitcher, peerViewSwitcher: () => peerViewSwitcher,
  };

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
  const [stageHostEl, setStageHostEl] = createSignal<
    HTMLDivElement | undefined
  >();
  const panes = createShellPaneVisibility({
    windowSubject: itemSubject, splitLive,
    presentedSplitLive: () => presentedSplitLive(),
    presentedStageId: () => presentedStageColumn().stageId,
  });
  const { windowNavUserCollapsed, windowNavCollapsed, navAutomaticallyCollapsed, navSlotWidthCss,
    showWindowNav, hideWindowNav, showConversation, hideConversation,
    toggleConversation, toggleContext, contextHidden, revealConversation, splitFitClaim, conversationHidden, stageIsLeading,
    navTouchesChat, navSide, stageHeaderLeadsWindow, chatHeaderLeadsWindow, stageEdges, conversationSeam } = panes;

  let layoutTriggerEl: HTMLButtonElement | undefined;
  let navDockEl: HTMLDivElement | undefined;

  dismissLayoutDockOnOutsidePress({
    open: () => dockTray() === "layout",
    dock: () => navDockEl,
    onDismiss: closeLayoutTray,
  });

  const [pendingRegionFocus, setPendingRegionFocus] =
    createSignal<FocusRegionId | null>(null);
  const [homeSection, setHomeSection] = createSignal<HomeSection>(DEFAULT_HOME_SECTION);
  const search = createShellSearchState({
    ...scope, showSearch: () => setNav("search"), closeLauncher: () => setLauncherOpen(false),
    routeToSearch: (open) => routeToStage("search", open),
    searchIsSplitCompanion: () => companionStageId() === "search" && splitLive(),
  });
  const { searchArgs, crossbarArgs, searchOpen, crossbarOpen,
    closeSearch, openSearch, closeCrossbar, openCrossbar,
    escalateCrossbarToSearch } = search;
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
  const { workersOpen, selectedWorkerId, workerDrawerFocus, observeActiveChat,
    openWorkers, closeWorkers, openNotificationSession, revealInTranscript,
    acknowledgeDrawerFocus } = workerDrawer;
  const { modelsChecked, showOnboardingGate, suppressShellForFirstRun,
    providerGap, showNoProviderBanner, observeProviders, observeBootFallback } =
    createShellOnboarding(props.appStore, props.projects, props.settingsStore);

  createEffect(() => {
    props.appStore.state.extensionsRevision;
    const client = getLycaonClient();
    if (!client) return;
    void workspacePreparation(props.shell.state.activeProjectId ?? "app").run(
      "project-stage-features",
      () => client.getSecurityScannersSettings(),
    ).then(
      (res) => setSecurityScannersFeatureOn(res.enabled),
      () => undefined,
    );
  });

  // Refresh chat inventory with project and connection state.
  createEffect(() => {
    props.projectSessions.setProject(props.shell.state.activeProjectId ?? null);
  });
  createEffect(() => {
    props.projectSessions.setSort(chatListSortPref());
  });

  const setSecurityScannersFeature = (enabled: boolean) => {
    setSecurityScannersFeatureOn(enabled);
    if (!enabled && nav() === "security") {
      setNav("projects");
    }
  };

  installShellNavigationSinks({
    ...scope, openStageWithPlacement: (stage, arrival, open) => openStageWithPlacement(stage, arrival, open),
    openSearch, openNotificationSession, revealConversation,
    resumeSession: (row, options) => resumeSession(row, options), openWorkers,
  });

  const { projectPresentation, identityReady, workspaceMounted, workspaceOpen, workspacePending,
    workspaceRevealed, workspaceVeilKey, workspaceVeilLabel, workspaceVeilNotice,
    sidebarChatsPublished, railStatusProject } = createShellWorkspacePresentation(scope);

  const presentedChat = createMemo(
    (prev: ActiveChat | null | undefined): ActiveChat | null =>
      holdPresentedChat(prev, readyChat(), {
        currentSessionId: props.appStore.state.currentSession?.id,
        transcriptSessionId: props.appStore.state.transcriptSessionId,
        hydrationLock: props.appStore.state.chatHydrationLock,
        projectId: props.shell.state.activeProjectId,
      }),
  );

  const activeChatScope = createMemo(() => {
    const chat = activeChat();
    return chat ? { projectId: chat.projectId, sessionId: chat.sessionId } : null;
  });

  /** The sidebar's groups; a selected chat outside them is listed last. */
  const sidebarSections = createMemo(() => {
    const chat = activeChat();
    const current = props.appStore.state.currentSession;
    const selected =
      chat && current && current.id === chat.sessionId &&
      current.project_id === props.projectSessions.state.projectId &&
      current.archived_at == null && !current.parent_session_id
        ? { ...sessionSummaryFromSession(current), message_count: props.appStore.state.messages.length }
        : null;
    return sidebarChatSections(
      props.projectSessions.state.rows,
      props.projectSessions.state.sort,
      selected,
    );
  });

  // Internal-only rows leave the chat fresh.
  const onFreshSession = createMemo(
    () =>
      nav() === "projects" &&
      activeChat() != null &&
      !hasDenTranscriptMessages(props.appStore.state.messages),
  );

  const projectDirForChat = () =>
    props.appStore.state.currentSession?.workspace_path?.trim() ?? "";

  const sessionSwitchDeps = (): SessionSwitchDeps => ({
    // Failed session transitions return to Home.
    returnToHome: () => {
      props.shell.clearToHome();
      setNav("projects");
    },
    clearChatForSessionSwitch: (hydrationLock) =>
      props.appStore.actions.clearChatForSessionSwitch(hydrationLock),
    beginSessionResumeSwitch: (hydrationLock) =>
      props.appStore.actions.beginSessionResumeSwitch(hydrationLock),
    completeChatSessionHydration: () =>
      completeChatHydrationAndReplay(props.appStore),
    resetChat: () => props.appStore.actions.resetChatForSessionSwitch(),
    reportError: (err, scope) => noticeReporterFor(scope).reportError(err),
    openSession: (scope) =>
      props.shell.commitStageScope({
        projectId: scope.projectId,
        sessionId: scope.sessionId,
        initialPrompt: scope.initialPrompt,
      }),
    setConnected: () => props.shell.setConnection("connected"),
    snapshotSessionChat: () => rememberSessionChatFromStore(props.appStore),
    restoreCachedSessionChat: (scope) =>
      restoreCachedSessionChat(props.appStore, {
        projectId: scope.projectId,
        sessionId: scope.sessionId,
      }),
  });

  // Layout effects populate this after the attention memos initialize.
  const [readableSession, setReadableSession] = createSignal<string | null>(null);

  /** Attention scoped to this window. */
  const attentionIndex = () =>
    withoutReadFinish(props.attention.index(), readableSession());

  const crossbarGotoTargets = createMemo(() => shellSearchTargets(
    props.projects.state.projects, props.recents.state.recents,
    attentionIndex(), activeProject(), securityScannersFeatureOn,
  ));

  /** MRU visits include chats opened only for reading. */
  const mru = createMruTracker();

  createEffect(() => {
    const fg = props.shell.state.foreground;
    if (fg) mru.visit(fg.projectId, fg.sessionId);
  });

  const recentChatEntries = createMemo(() => {
    const nameById = new Map(
      props.projects.state.projects.map(
        (p) => [p.id, p.name?.trim() || undefined] as const,
      ),
    );
    const attention = attentionIndex();
    const recents = [...props.recents.state.recents].sort(
      (a, b) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0),
    );
    return buildMruEntries(mru, recents, {
      projectName: (projectId) => nameById.get(projectId),
      attention: (sessionId) => attention.get(sessionId),
    });
  });

  /** Opens a setting's section and tab; Settings reveals the row once it renders. */
  const openSettingFromGoto = (settingId: string) => {
    const setting = findSettingDefinition(settingId);
    if (!setting) return;
    routeToStage("settings", () => {
      selectSettingsSection(setting.section, {
        general: setting.section === "general" ? setting.tab : undefined,
        advanced: setting.section === "debug" ? setting.tab : undefined,
      });
      requestSettingReveal(setting);
      setNav("settings");
    });
  };

  const navigateFromCrossbarGoto = (target: CrossbarGotoTarget) => {
    if (target.kind === "session") {
      void navigateFromSearch({
        projectId: target.projectId,
        sessionId: target.sessionId,
      });
      return;
    }
    if (target.kind === "project") {
      openProject(target.projectId);
      return;
    }
    if (target.kind === "setting") {
      openSettingFromGoto(target.settingId);
      return;
    }
    // Go-to jumps retain their routed return target.
    if (isContextNavItemId(target.surface)) {
      openStageWithPlacement(target.surface, "routed");
      return;
    }
    switch (target.surface) {
      case "context":
        routeToStage("context", openConfiguration);
        return;
      case "chats":
        setNav("chats");
        return;
      case "home":
        goHome();
        return;
      case "settings":
        openSettings();
        return;
      default:
        target.surface satisfies never;
        return;
    }
  };

  const { navigateFromSearch, revealFromSearch } = createShellSearchNavigation({
    ...scope, closeSearch, closeLauncher: () => setLauncherOpen(false),
    openProject: (id) => openProject(id), resumeSession: (row, options) => resumeSession(row, options),
    openWorkers,
  });

  const sessionNavigation = createSessionNavigation({
    ...scope, activeChatScope, onFreshSession, sessionSwitchDeps,
    conversationInLayout: () => conversationInLayout(), revealConversation, closeSearch, closeCrossbar,
    closeLauncher: () => setLauncherOpen(false),
    focusComposer: () => setPendingRegionFocus("composer"),
  });
  const { openProject, newProject, goHome, sidebarRowAsRecent, sessionRetirementDeps, archiveSidebarSession,
    deleteSidebarSession, resumeSession, selectSidebarSession, openSessionInNewWindow, beginWorkspaceOpening,
    abandonWorkspaceOpening, startEmptySession, startNewChat } = sessionNavigation;

  const { sendPrompt, resumeSpendLimitedChat, stopChat } = createShellChatActions({ ...scope, projectDirForChat });

  /** Reactive client accessor keyed by connection status. */
  const connectedClient = () =>
    props.appStore.state.sidecarStatus === "connected"
      ? getLycaonClient()
      : null;

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

  // Provider nudges share shell-level navigation.
  const openProvidersSettings = () => openSettings("providers");
  setShellNoticeActionSinks({ openAIProviders: openProvidersSettings });
  onCleanup(() => setShellNoticeActionSinks(null));

  // Status chips route through the current shell.
  setStatusChipNavigationSink((target) => {
    if (target.kind === "project-context") {
      openStageWithPlacement(target.stage, "routed");
      return;
    }
    if (target.kind === "project-configuration") {
      selectContextSection(target.section);
      return;
    }
    openSettings(target.section);
  });
  onCleanup(() => setStatusChipNavigationSink(null));

  // The visible chat is the local add-to-chat target.
  onMount(() => {
    const detach = registerComposerAttachmentSink({
      sessionId: () => activeChat()?.sessionId ?? "",
      blockReason: () =>
        composerComposeBlockReason(
          props.appStore.state.sidecarStatus,
          activeChat()?.sessionId ?? null,
          catalogWorkflowRun(
            props.appStore.state.activeWorkflowRun,
            props.appStore.state.workflowRuns,
          ),
          props.appStore.state.chatHydrationLock,
          showNoProviderBanner(),
          props.appStore.state.currentSession?.status,
        ),
      projectId: () =>
        activeChat()?.projectId ??
        props.shell.state.activeProjectId ??
        "",
      projectRoots: () => {
        const id =
          activeChat()?.projectId ?? props.shell.state.activeProjectId ?? "";
        if (!id) return [];
        return props.projects.byId(id)?.roots ?? [];
      },
      reveal: () => {
        closeCrossbar();
        closeSearch();
        closeDiffViewer();
        setNav("projects");
        void revealConversation();
      },
    });
    onCleanup(detach);
  });

  observeBootFallback();

  observeActiveChat();

  onMount(() => {
    onCleanup(registerShellDismissCommand({
      ...scope, ...search, ...navigation, ...workerDrawer, layoutTrigger: () => layoutTriggerEl,
    }));
    registerShellCommands({
      ...scope, ...search, ...panes, ...sessionNavigation, openSettings, stopChat, applyStagePlacement,
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
    onCleanup(attachDispatcher());
  });

  createEffect(() => {
    // Split Files stages retain their editor shortcut scope.
    setFilesStageActive(stageColumnStageId() === "files");
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

  const showHome = () =>
    nav() === "projects" && isHomePresentation(projectPresentation());

  const showChatTabRail = () =>
    nav() === "projects" && workspaceMounted() && presentedChat() != null;

  const placeableStageId = (): ContextNavItemId | null => {
    const n = nav();
    return isContextNavItemId(n) ? n : null;
  };

  // Same-project switches retain the outgoing chat.
  const chatPresence = createMemo((prev: ChatPresence | undefined) =>
    holdChatPresence(prev, stageScope()),
  );

  const stageColumn = createMemo(() => {
    const projectId = props.shell.state.activeProjectId;
    const splitMode = projectId != null && stagePlacementMode() === "split";
    return resolveStageColumn({
      navStage: placeableStageId(),
      foregroundIsChat: nav() === "projects",
      hasChat: chatPresence().present,
      companion: splitMode
        ? resolveSplitCompanion(companionPref(), (id) =>
            stageFeatureEnabled(id, {
              securityScannersEnabled: securityScannersFeatureOn(),
            }),
          )
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

  // Observer writes do not subscribe to reactive width.
  createEffect(() => {
    const el = stageHostEl();
    if (!el) return;
    let applied: number | undefined;
    let pending: number | undefined;
    let publishFrame: number | undefined;
    const publish = (width: number) => {
      if (width === applied) return;
      applied = width;
      setSplitHostWidth(width);
    };
    const schedulePublish = (width: number) => {
      pending = width;
      if (publishFrame !== undefined) return;
      publishFrame = requestAnimationFrame(() => {
        publishFrame = undefined;
        if (pending === undefined) return;
        const next = pending;
        pending = undefined;
        publish(next);
      });
    };
    const observer = new ResizeObserver((entries) => {
      const box = entries[entries.length - 1]?.contentRect;
      if (!box) return;
      const width = Number.isFinite(box.width) ? Math.round(box.width) : 0;
      schedulePublish(width);
    });
    observer.observe(el);
    onCleanup(() => {
      observer.disconnect();
      if (publishFrame !== undefined) cancelAnimationFrame(publishFrame);
    });
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

  // Seen stamps require a presented conversation in a focused window.
  createEffect(() => {
    const chat = presentedChat();
    const readable =
      chat != null &&
      workspaceMounted() &&
      conversationShown() &&
      windowFocused();
    setReadableSession(readable && chat ? chat.sessionId : null);
  });

  onCleanup(
    createSeenMarker({
      readable: readableSession,
      // Status and transcript changes refresh the visible chat's seen stamp.
      revision: () =>
        `${props.appStore.state.currentSession?.status ?? ""}:${props.appStore.state.messages.length}`,
      mark: async (sessionId) => {
        const session = await getLycaonClient()?.markSessionSeen(sessionId);
        if (session) props.appStore.actions.noteSessionSeen(session);
      },
      // Capture the previous seen stamp before this visit's stamp posts.
      onReadable: (sessionId) => {
        const current = props.appStore.state.currentSession;
        captureUnreadBoundary(
          sessionId,
          current?.id === sessionId ? current.seen_at : undefined,
          Date.now(),
        );
      },
    }),
  );
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

  const activeFileBuffer = () => {
    const projectId = props.shell.state.activeProjectId;
    const filesState = projectId != null ? projectFilesState(projectId) : null;
    const activeKey = filesState?.activeKey ?? null;
    return activeKey && filesState ? filesState.byKey[activeKey] ?? null : null;
  };

  /** Title of the selected chat, which may not be the one bound mid-switch. */
  const selectedChatTitle = (sessionId: string): string => {
    const current = props.appStore.state.currentSession;
    const bound = current?.id === sessionId ? current.title?.trim() : undefined;
    const row = props.projectSessions.state.rows.find((r) => r.id === sessionId);
    return bound || row?.title?.trim() || sessionId;
  };

  const focusedPeerSubject = (): PeerOpenSubject | null => {
    const projectId = props.shell.state.activeProjectId;
    const chat = activeChat();
    const stageId = stageColumnStageId() ?? placeableStageId();
    const activeBuf = activeFileBuffer();
    return resolvePeerOpenSubject({
      windowSubject: itemSubject,
      projectId,
      session: chat
        ? { sessionId: chat.sessionId, title: selectedChatTitle(chat.sessionId) }
        : null,
      contextStage: stageId,
      contextTitle: stageId ? stageLabelFor(stageId) : null,
      activeFile:
        activeBuf && stageId === "files"
          ? {
              rootId: activeBuf.rootId,
              path: activeBuf.path,
              title: activeBuf.name,
            }
          : itemSubject?.kind === "file"
            ? {
                rootId: itemSubject.rootId,
                path: itemSubject.path,
                title: itemSubject.path.split("/").pop() || "File",
              }
            : null,
      filesStageWithoutEditor: stageId === "files" && activeBuf == null,
      splitFocusRegion: splitLive() ? currentSplitFocusRegion() : null,
      splitLive: splitLive(),
    });
  };

  const { detachStageToWindow, openFocusedSubjectInNewWindow, openFileWindow,
    openFileInNewWindow, beginFileWindowDrag, closeStageWindows, ...peerWindows } = createShellPeerWindows({
      ...scope, itemSubject, focusedPeerSubject, openStage: (stage) => stageOpeners[stage](),
    });
  const { focusedPeerViews } = peerWindows;

  // Host-validated destinations run the navigation command of the same name.
  const navigateContributionDestination = (destination: string) => {
    if (destination.startsWith("context.")) {
      invokeCommand(`go.${destination}`);
      return;
    }
    if (destination.startsWith("settings.")) {
      invokeCommand(`settings.section.${destination.slice("settings.".length)}`);
      return;
    }
    switch (destination) {
      case "files":
        openStageWithPlacement("files", "routed");
        return;
      case "home":
        invokeCommand("go.home");
        return;
      case "chat":
        invokeCommand("go.chat");
        return;
      case "composer":
        invokeCommand("go.composer");
        return;
      case "sidebar":
        invokeCommand("go.sidebar");
        return;
      case "all_chats":
        invokeCommand("go.allChats");
        return;
      default:
        return;
    }
  };
  // Command availability uses live shell facts; the host revalidates.
  const contributionShellView = (n: Nav): string => {
    if (n === "projects") return "welcome";
    if (n === "settings") return "settings";
    if (n === "chats") return "chat";
    if (n === "files") return "files";
    return "context";
  };
  const editorCommandTarget = () => {
    const projectId = props.shell.state.activeProjectId;
    const buffer = activeFileBuffer();
    if (!projectId || !buffer || buffer.kind !== "text" || stageColumnStageId() !== "files" ||
      selectedFileVersion(projectId, buffer.key) != null) return null;
    const view = mountedFilesEditorView(projectId, buffer.key);
    return view ? filesCommandContext(buffer, view) : null;
  };

  /** The Files stage shows a past version of its active buffer. */
  const filesVersionHistorical = (): boolean => {
    const projectId = props.shell.state.activeProjectId;
    const buffer = activeFileBuffer();
    return (
      projectId != null &&
      buffer != null &&
      stageColumnStageId() === "files" &&
      selectedFileVersion(projectId, buffer.key) != null
    );
  };

  /** The shell's typed state, in the closed fact vocabulary. */
  const currentShellFacts = (): ShellFactState => {
    const frame = contributionFrame();
    focusRegionsVersion();
    const chat = activeChat();
    const projectId = props.shell.state.activeProjectId;
    const subject = itemSubject;
    const regions = mountedFocusRegions();
    const flags = stageFeatureFlags();
    const activityLive = chat
      ? isChatActivityLive(props.appStore, chat.sessionId)
      : false;
    return {
      activeView: contributionShellView(nav()),
      mountedRegions: regions,
      workspaceKind: workspaceKindOf(subject),
      isPeerWorkspace: subject != null,
      contextFeatures: CONTEXT_NAV_CATALOG.filter((id) =>
        stageFeatureEnabled(id, flags),
      ),
      // Real textarea focus: the chat stage always has a composer mounted.
      composerFocused: isComposerFocused(),
      chatNavigable: subject == null && readyChat() != null,
      contextNavigable:
        subject == null ||
        subject.kind === "context" ||
        regions.includes("context"),
      sidebarExpanded: !windowNavCollapsed(),
      contextCollapsed: contextHidden(),
      conversationCollapsed: (hiddenSplitPanePref() === "conversation"),
      splitLive: splitLive(),
      filesStageActive: stageColumnStageId() === "files",
      filesTreeCollapsed: filesTreeCollapsed(),
      fileSummariesOn: fileSummariesEnabled(),
      fileSummariesKnown: fileSummariesSettingKnown(),
      filesVersionHistorical: filesVersionHistorical(),
      peerOpenAvailable:
        isTauriRuntime() && projectId != null && focusedPeerSubject() != null,
      peerViewsPresent: focusedPeerViews().length > 0,
      projectOpen: projectId != null,
      sessionExists: chat != null,
      sessionIdle: chat != null && !activityLive,
      activityLive,
      editor: editorCommandTarget()?.facts ?? null,
      requirementReady: (id) =>
        frame?.requirements.some((req) => req.id === id && req.ready) ?? false,
      configurationOn: (id) =>
        frame?.configuration.some(
          (property) => property.id === id && property.value === true,
        ) ?? false,
    };
  };

  const { crossbarActions } = createShellContributions({
    projectId: () => props.shell.state.activeProjectId,
    sessionId: () => activeChat()?.sessionId ?? null,
    navigate: navigateContributionDestination,
    editorContext: () => editorCommandTarget()?.context,
    facts: currentShellFacts,
    openCommand: (commandId) => openCrossbar({ originProjectId: props.shell.state.activeProjectId, commandId }),
    windowFocused,
  });

  createEffect(() => {
    const region = pendingRegionFocus();
    if (!region) return;
    focusRegionsVersion();
    if (!isFocusRegionMounted(region) && (region !== "files" || !isFocusRegionMounted("filesTree"))) return;
    // Region commands run after chat mount focus settles.
    queueMicrotask(() => {
      queueMicrotask(() => {
        if (focusRegion(region)) {
          setPendingRegionFocus(null);
        } else if (region === "files" && focusRegion("filesTree")) {
          setPendingRegionFocus(null);
        }
      });
    });
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
    firstConversation: () => sidebarNavigationOrder(sidebarSections())[0] ?? null,
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

  const ChatStageLayer = (layer: {
    chat: ActiveChat;
    pending: () => boolean;
    surfaceActive: () => boolean;
  }) => {
    const dock = chatTopDock(layer.chat);
    return (
    <ChatTabChromeProvider sessionKey={layer.chat.sessionId}>
      <ContextDrawerHost>
        <div
          class="den-shell-stage--chat den-shell-stage"
          classList={{
            "den-stage-boot": layer.pending() === true,
            "den-stage-enter-fade": layer.pending() === true,
          }}
          data-boot={layer.pending() ? "ready" : undefined}
        >
          <header
            class="den-shell-header-chat den-shell-header"
            classList={{
              "den-shell-header--window-leading": chatHeaderLeadsWindow(),
            }}
            {...chromeProps()}
            {...tauriDragRegionProps()}
          >
            <ChromeDragSurface />
            <ChatTabRail
              appStore={props.appStore}
              sessionId={layer.chat.sessionId}
              selectedWorkerId={selectedWorkerId()}
              onOpenWorker={openWorkers}
              navCollapsed={
                windowNavCollapsed() && (navTouchesChat() || splitLive())
              }
              navAutomaticallyCollapsed={navAutomaticallyCollapsed()}
              navSplitFallback={!navTouchesChat() && !contextHidden()}
              navSide={navSide()}
              onNavExpand={() => void showWindowNav()}
              conversationSeam={conversationSeam()}
              dock={dock}
            />
          </header>
          <main class="den-shell-main" aria-label="Chat">
            <StageErrorBoundary stage="chat">
              <ChatStageBody
                chat={layer.chat}
                surfaceActive={layer.surfaceActive}
              />
            </StageErrorBoundary>
          </main>
        </div>
      </ContextDrawerHost>
    </ChatTabChromeProvider>
    );
  };

  const settingsProjectDir = () =>
    projectDirForChat() ||
    activeProject()?.roots.find((r) => r.is_primary)?.path ||
    activeProject()?.roots[0]?.path ||
    undefined;

  const { shownWorkspaceContexts, backFor, sourceBack, titlebarBack, stageBack } = createShellWorkspaceContext({
    ...scope, routedReturn, showConversation, splitFitClaim, closeWorkers, presentedChat,
    projectPresentation, conversationShown, splitColumns, stageColumnStageId, stageHandoff,
    reopenFiles: () => openStageWithPlacement("files", "routed"),
  });

  createEffect(() => {
    setCurrentWorkspaceContexts(shownWorkspaceContexts());
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

  const renameSession = async (
    row: { projectId: string; sessionId: string },
    title: string,
  ) => {
    const client = getLycaonClient();
    if (!client) return;
    const read = beginSessionSnapshotRead(props.appStore, row.sessionId);
    try {
      const updated = await client.updateSession(row.sessionId, { title });
      const nextTitle = updated.title?.trim() || title.trim();
      if (read.isCurrent()) {
        props.appStore.actions.setSessionTitle(row.sessionId, nextTitle);
      }
      await props.recents.registerSession({
        projectId: row.projectId,
        sessionId: row.sessionId,
        title: nextTitle,
      });
      void props.projectSessions.refresh();
    } catch (err) {
      noticeReporterFor(sessionScope(row.projectId, row.sessionId)).reportError(err);
    } finally {
      read.finish();
    }
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

  const projectRemovalReview = createProjectRemovalReview();
  const deleteProject = createProjectRemoval({
    review: (assessment, projectId) => projectRemovalReview.show(assessment, props.projects.summary(projectId)?.displayName ?? "this project"),
    client: getLycaonClient,
    confirm: confirmDestructive,
    prepare: async (projectId) => {
      const roots = props.projects.byId(projectId)?.roots ?? [];
      await Promise.all(roots.map((root) => prepareFilesRootDetach(projectId, root.id)));
    },
    retire: async (projectId) => {
      getEntityRetire()?.project(projectId);
      props.projects.drop(projectId);
      forgetProjectThumbnail(projectId);
      if (props.shell.state.activeProjectId === projectId) props.shell.clearToHome();
      await props.recents.removeRecentsForProject(projectId);
    },
    report: (error) => shellAppReporter().reportError(error),
    notify: (notice) => shellAppReporter().publish(notice),
    extensionsChanged: () => { void invalidateContributionFrame(); },
  });

  /** Notifications appear on Home and in the chat dock. */
  const notificationStack = () => (
    <NotificationStack
      index={noticeIndex()}
      activeProjectId={props.shell.state.activeProjectId ?? undefined}
      projectName={(projectId) => props.projects.summary(projectId)?.displayName}
      onOpenProject={openProject}
      onDismiss={(id) => props.notices.dismiss(id)}
      onDismissApp={() => props.notices.dismissScope(APP_SCOPE)}
      announceLive={!isAnySessionActivityLive(props.appStore)}
    />
  );

  const chatTopDock = (chat: ActiveChat) => {
    const [dismissedProtection, setDismissedProtection] = createSignal<
      Partial<Record<ProtectionBannerId, boolean>>
    >({});
    const [providerDismissed, setProviderDismissed] = createSignal(false);
    const protection = () => {
      const sess = props.appStore.state.currentSession;
      if (!sess || sess.id !== chat.sessionId) return undefined;
      return sess.ui?.protection;
    };
    const spendLimits = () =>
      props.settingsStore.state.effectiveLimits?.projectId === chat.projectId
        ? props.settingsStore.state.effectiveLimits.limits
        : props.settingsStore.state.limits ?? null;
    const updateSpendLimits = (client: LycaonClient, update: import("../../settings/budgets/spend-ceiling-actions.ts").SpendCeilingUpdate) => {
      if (update.scope === "project") {
        props.settingsStore.actions.setEffectiveLimits(chat.projectId, update.limits);
        return;
      }
      props.settingsStore.actions.setLimits(update.limits);
      void client.getLimitsSettings(chat.projectId).then((limits) => {
        props.settingsStore.actions.setEffectiveLimits(chat.projectId, limits);
      });
    };
    const verify = createVerifyTestSuggestion({
      client: connectedClient,
      appStore: props.appStore,
      projectId: () => chat.projectId,
    });
    return (
      <ChatTopChromeStack
        session={{
          present: () =>
            selectSessionNotices(noticeIndex(), chat.sessionId).length > 0,
          children: () => (
            <NoticeRail
              notices={selectSessionNotices(noticeIndex(), chat.sessionId)}
              onDismiss={(id) => props.notices.dismiss(id)}
              onDismissAll={() =>
                props.notices.dismissScope(
                  sessionScope(chat.projectId, chat.sessionId),
                )
              }
              announceLive={!isAnySessionActivityLive(props.appStore)}
            />
          ),
        }}
        spend={{
          present: () =>
            spendCeilingReachedNotice(props.notices, chat.sessionId) != null ||
            spendCeilingApproachingVisible({
              sessionId: chat.sessionId,
              limits: spendLimits(),
              summary: props.costStore.state.session,
            }),
          children: () => (
            <Show when={connectedClient()} keyed>
              {(client) => (
                <>
                  <SpendCeilingReachedNudge
                    client={client}
                    notices={props.notices}
                    sessionId={chat.sessionId}
                    projectId={chat.projectId}
                    spentUsd={
                      props.costStore.state.session?.estimated_nano_usd != null
                        ? props.costStore.state.session.estimated_nano_usd / 1e9
                        : undefined
                    }
                    hasUnsentContent={hasSpendRecoveryDraft(chat.sessionId)}
                    onOpenBudgets={openBudgetsSettings}
                    onResume={() => resumeSpendLimitedChat(chat)}
                    onLimitsUpdated={(update) => updateSpendLimits(client, update)}
                  />
                  <SpendCeilingApproachingNudge
                    client={client}
                    sessionId={chat.sessionId}
                    projectId={chat.projectId}
                    summary={props.costStore.state.session}
                    limits={spendLimits()}
                    onLimitsUpdated={(update) => updateSpendLimits(client, update)}
                    onOpenBudgets={openBudgetsSettings}
                  />
                </>
              )}
            </Show>
          ),
        }}
        notifications={{
          present: () => hasSharedNotices(noticeIndex()),
          children: notificationStack,
        }}
        verify={{
          present: () => verify.suggesting(),
          children: () => (
            <VerifyTestNudge
              suggestion={verify}
              onOpenSettings={() =>
                routeToStage("context", () => selectContextSection("tests"))
              }
            />
          ),
        }}
        protection={{
          present: () =>
            sessionProtectionPresent(protection(), dismissedProtection()),
          children: () => (
            <SessionProtectionBanners
              protection={protection()}
              onDismiss={(id) =>
                setDismissedProtection((prev) => ({ ...prev, [id]: true }))
              }
            />
          ),
        }}
        promote={{
          present: showPromoteNudge,
          children: () => (
            <DraftPromoteBanner
              onPromote={() => {
                const id = props.shell.state.activeProjectId;
                if (id) openMoveDialog(id);
              }}
              onDismiss={() => {
                const projectId = props.shell.state.activeProjectId;
                if (projectId) dismissDraftPromoteBanner(projectId);
              }}
            />
          ),
        }}
        provider={{
          present: () => showNoProviderBanner() && !providerDismissed(),
          children: () => (
            <Show when={providerGap()} keyed>
              {(gap) => (
                <NoProviderCard
                  gap={gap}
                  onOpenProviders={openProvidersSettings}
                  onDismiss={() => setProviderDismissed(true)}
                />
              )}
            </Show>
          ),
        }}
        folder={{
          present: showNoFolderBanner,
          children: () => (
            <NoFolderBanner
              onAddFolder={() => void addFolderToActiveProject()}
              onDismiss={() => {
                const projectId = props.shell.state.activeProjectId;
                if (projectId) dismissNoFolderBanner(projectId);
              }}
            />
          ),
        }}
      />
    );
  };

  const ChatStageBody = (body: {
    chat: ActiveChat;
    surfaceActive: () => boolean;
  }) => (
    <>
      <ChatView
        needsProvider={showNoProviderBanner()}
        appStore={props.appStore}
        recents={props.recents}
        projects={props.projects}
        projectId={body.chat.projectId}
        projectDir={projectDirForChat()}
        sessionId={body.chat.sessionId}
        hasInitialPrompt={
          props.shell.state.foreground?.sessionId === body.chat.sessionId &&
          props.shell.state.foreground.initialPrompt === true
        }
        surfaceActive={body.surfaceActive()}
        selectedWorkerId={selectedWorkerId()}
        workersDrawerOpen={body.surfaceActive() && workersOpen()}
        workerDrawerFocus={workerDrawerFocus()}
        workersBackgroundHydrate={nav() === "projects"}
        onWorkersClose={closeWorkers}
        onWorkerDrawerFocusHandled={acknowledgeDrawerFocus}
        onOpenWorker={openWorkers}
        onOpenFiles={stageOpeners.files}
        onSend={sendPrompt}
        onStop={stopChat}
        visionSupport={coordinatorModelVisionSupport(
          props.settingsStore.state.providers,
          props.settingsStore.state.modelPolicy,
        )}
        providers={props.settingsStore.state.providers}
        providerKinds={props.settingsStore.state.providerKinds}
      />
    </>
  );

  const ProjectStage = (stageProps: { stage: ContextNavItemId }) => {
    const stage = stageProps.stage;
    const projectId = props.shell.state.activeProjectId;
    if (stage !== "search" && !projectId) return null;
    const args = stage === "search" ? searchArgs() : undefined;
    const pid = projectId ?? args?.originProjectId ?? null;
    const definition = stageDefinition(stage);
    return definition.render({
      projectId: pid,
      get projectName() {
        return (pid ? props.projects.byId(pid)?.name?.trim() : undefined) || "Untitled project";
      },
      appStore: props.appStore,
      get roots() {
        return (pid ? props.projects.byId(pid)?.roots : undefined) ?? [];
      },
      get projects() {
        return props.projects.state.projects;
      },
      back: () => stageBack(stage),
      search: {
        get originProjectId() {
          return (
            props.shell.state.activeProjectId ??
            searchArgs()?.originProjectId ??
            null
          );
        },
        get seed() {
          return searchArgs()?.seed;
        },
        get replaceMode() {
          return searchArgs()?.replaceMode;
        },
        get serial() {
          return searchArgs()?.serial ?? 0;
        },
      },
      callbacks: {
        onOpenSession: (target) => void navigateFromSearch(target),
        onOpenCreatedSession: (target) => {
          void (async () => {
            await recordCreatedSessionInRecents(props.recents, target);
            await navigateFromSearch(target);
          })();
        },
        onCreateSupportingWorkflowSession: async ({ projectId, workflow }) => {
          await startEmptySession(
            { kind: "new-session", projectId },
            { armWorkflow: workflow },
          );
        },
        onOpenSettings: (section) => openSettings(section),
        onSearchNavigate: (target) => void navigateFromSearch(target),
        onOpenInNewWindow:
          stage === "files" &&
          pid &&
          isTauriRuntime()
            ? (key, position) =>
                key
                  ? openFileInNewWindow(key, position)
                  : void detachStageToWindow("files")
            : undefined,
        onOpenFileInNewWindow:
          stage === "files" && pid && isTauriRuntime()
            ? (rootId, path, title) =>
                openFileWindow({ rootId, path, title })
            : undefined,
        fileTabWindowDrag:
          stage === "files" && pid && isTauriRuntime()
            ? {
                begin: beginFileWindowDrag,
                move: moveItemWindowDrag,
                finish: finishItemWindowDrag,
                cancel: cancelItemWindowDrag,
            }
            : undefined,
        restoreFilesHotExit: itemSubject?.kind !== "file",
        onFileTabMovedOut:
          stage === "files" && pid && itemSubject?.kind === "file"
            ? () => {
                if (projectFilesState(pid).order.length > 0) return;
                suppressFilesHotExitWrite(pid);
                void closeCurrentPeerWindow().catch((err) => {
                  clearFilesHotExitSuppress(pid);
                  console.debug("[item-window] close empty file window failed", err);
                });
              }
            : undefined,
        onRevealInTranscript: (target) => revealInTranscript(pid, target),
        onOpenSearch: (args) =>
          openStageWithPlacement("search", "routed", () =>
            openSearch({ originProjectId: args.originProjectId, seed: args.seed })),
      },
    });
  };

  /** Home rows and counts hold still while a submitted idea becomes a project. */
  const homeSummaries = createMemo<ProjectSummary[]>((previous) =>
    retainedHomeSummaries(
      previous,
      props.projects.summaries(),
      props.shell.state.materializingDraft,
    ),
  );

  const railStatusSessionId = (projectId: string): string => {
    const chat = presentedChat();
    return chat?.projectId === projectId ? chat.sessionId : "";
  };

  const StageSurface = (surface: { surfaceKey: string }) => {
    const key = surface.surfaceKey;
    if (key === SURFACE_HOME) {
      return (
        <HomeView
          summaries={homeSummaries()}
          section={homeSection()}
          onShowAllProjects={() => setHomeSection("all")}
          registry={props.projects.state.registry}
          providerGap={providerGap()}
          onOpenProviders={openProvidersSettings}
          onOpenDiagnostics={() => openSettings("debug", { advanced: "diagnostics" })}
          onSubmitIdea={async (draft) => {
            if (!props.shell.beginDraftMaterialization()) return false;
            void showConversation();
            return (await startEmptySession(
              { kind: "new-project" },
              { prompt: draft.text, ideaAttachments: draft.attachments },
            )) === true;
          }}
          submitting={props.shell.state.materializingDraft}
          onOpenFolder={() => void startOpenFolder()}
          onCloneRepo={() => openCloneDialog()}
          onOpenProject={(projectId) => openProject(projectId)}
          onToggleStar={(id, starred) => void toggleStar(id, starred)}
          onRename={(id, name) => void renameProject(id, name)}
          onAttachFolder={(id) => {
            if (props.projects.byId(id)?.is_draft) openMoveDialog(id);
            else setFoldersPanelProjectId(id);
          }}
          onPromote={(id) => openMoveDialog(id)}
          onDelete={(id, opts) => void deleteProject(id, opts)}
          attentionRows={props.attention.state.rows}
          onOpenAttention={(row) =>
            void navigateFromSearch({
              projectId: row.project_id,
              sessionId: row.session_id,
            })
          }
        />
      );
    }
    if (key === SURFACE_SETTINGS) {
      return (
        <SettingsView
          back={backFor("settings", "settings-back")}
          section={settingsSection()}
          generalInitialTab={generalInitialTab()}
          advancedInitialTab={advancedInitialTab()}
          appStore={props.appStore}
          settingsStore={props.settingsStore}
          costStore={props.costStore}
          projects={props.projects}
          projectDir={settingsProjectDir()}
          projectName={
            props.projects.summary(
              props.shell.state.activeProjectId ?? "",
            )?.displayName
          }
          onClose={closeSettingsNav}
          onOpenScanners={() => selectSettingsSection("scanners")}
          onOpenProjectSettings={
            props.shell.state.activeProjectId ? openContextSection : undefined
          }
          onOpenProjectTrust={
            props.shell.state.activeProjectId
              ? () => selectContextSection("trust")
              : undefined
          }
          onSecurityScannersFeatureChange={setSecurityScannersFeature}
        />
      );
    }
    if (key.startsWith("chats:")) {
      const projectId = key.slice("chats:".length);
      if (!projectId) return null;
      return (
        <ProjectChatsView
          projectId={projectId}
          appStore={props.appStore}
          back={backFor("chats", "chats-back")}
          attention={attentionIndex()}
          onOpenSession={(sessionId) =>
            void navigateFromSearch({ projectId, sessionId })
          }
          onSetArchived={(row, archived) =>
            setSessionArchived(
              sessionRetirementDeps(),
              { projectId, sessionId: row.id, title: row.title },
              archived,
            )
          }
          onSetPinned={(row, pinned) =>
            setSessionPinned(
              sessionRetirementDeps(),
              { projectId, sessionId: row.id, title: row.title },
              pinned,
            )
          }
          onDeleteSession={(row) =>
            deleteSession(sessionRetirementDeps(), {
              projectId,
              sessionId: row.id,
              title: row.title,
            })
          }
        />
      );
    }
    if (key.startsWith("project-config:")) {
      const projectId = key.slice("project-config:".length);
      if (!projectId) return null;
      return (
        <ProjectContextView
          back={backFor("context", "context-back")}
          section={contextSection()}
          onSectionChange={selectContextSection}
          projectId={projectId}
          sessionId={activeChat()?.projectId === projectId ? activeChat()?.sessionId : undefined}
          projectDir={settingsProjectDir()}
          appStore={props.appStore}
          settingsStore={props.settingsStore}
          projects={props.projects}
          onClose={closeSettingsNav}
          onOpenDeviceTrust={openProjectTrustSettings}
          onOpenExtensionsSettings={openExtensionsSettings}
          onOpenDeviceSettings={(section) => openSettings(section)}
        />
      );
    }
    const stage = parseStageSurfaceKey(key);
    if (!stage || !(CONTEXT_NAV_CATALOG as readonly string[]).includes(stage)) {
      return null;
    }
    const id = stage as ContextNavItemId;
    if (id === "files") {
      return (
        <div class="den-resident-stage-content" data-boot="ready">
          <ProjectStage stage={id} />
        </div>
      );
    }
    return <ProjectStage stage={id} />;
  };

  return (
    <Show
      when={!showOnboardingGate()}
      fallback={
        <Show
          when={contributionFrameReady()}
          fallback={
            <ProjectLoadingStage
              label="Painted Wolf Code"
              hint="Loading commands…"
            />
          }
        >
          <OnboardingGate
            client={
              // Client lookup is not reactive.
              props.appStore.state.sidecarStatus === "connected"
                ? getLycaonClient()
                : null
            }
            settingsStore={props.settingsStore}
            settingsChecked={modelsChecked()}
          />
        </Show>
      }
    >
    <Show
      when={!suppressShellForFirstRun()}
      fallback={
        <div
          class="onboarding-first-run-pending"
          data-testid="onboarding-first-run-pending"
          aria-busy="true"
        >
          <ChromeDragSurface class="onboarding-first-run-pending__chrome-drag" />
        </div>
      }
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
                  stageFeatureEnabled(id, stageFeatureFlags())
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
                elementRef={(el) => (layoutTriggerEl = el)}
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
        hostRef={(el) => {
          setStageHostEl(el);
          // Initial width precedes the first observer callback.
          setSplitHostWidth(el.getBoundingClientRect().width);
        }}
        divider={
          <SplitDivider
            hostWidthPx={splitHostWidthPx}
            chatWidthPx={publishedChatWidthPx}
            onBegin={() =>
              beginChatWidthResize(splitHostWidthPx(), hideConversation)
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
            {(key) => <StageSurface surfaceKey={key} />}
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
              <ChatStageLayer
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
      <Show when={editorCommandBarPlacement() === "shell"}>
        <div
          class="den-find-bar-host"
          data-testid="find-bar-host"
          style={{ "--den-nav-slot-width": navSlotWidthCss() }}
        >
          <EditorCommandBar />
        </div>
      </Show>
      <ProjectLauncher
        open={launcherOpen()}
        summaries={props.projects.summaries()}
        activeProjectId={props.shell.state.activeProjectId}
        onSwitch={(projectId) => openProject(projectId)}
        onManageAll={() => {
          setHomeSection("all");
          goHome();
        }}
        onNewProject={newProject}
        onClose={() => setLauncherOpen(false)}
      />
      <Crossbar
        open={crossbarOpen()}
        originProjectId={crossbarArgs()?.originProjectId ?? null}
        projectRoots={props.projects.byId(crossbarArgs()?.originProjectId ?? "")?.roots}
        originSessionId={
          props.appStore.state.currentSession?.project_id?.trim() ===
          (crossbarArgs()?.originProjectId ?? "").trim()
            ? props.appStore.state.currentSession?.id
            : undefined
        }
        seed={crossbarArgs()?.seed}
        seedSerial={crossbarArgs()?.serial}
        initialMode={crossbarArgs()?.mode}
        initialCommandId={crossbarArgs()?.commandId}
        gotoTargets={crossbarGotoTargets()}
        contributionCommands={crossbarActions()}
        onNavigateTarget={navigateFromCrossbarGoto}
        onOpenFolderAsProject={(path) => void proposeFolderAsProject(path)}
        onClose={() => closeCrossbar()}
        onEscalate={escalateCrossbarToSearch}
        onNavigateHit={(hit, matchText) => {
          openSearchHit(hit, (h) => {
            void navigateFromSearch(searchHitToNavTarget(h, matchText));
          });
        }}
      />
      <RecentChatSwitcher
        ref={(handle) => (recentSwitcher = handle)}
        entries={recentChatEntries()}
        onCommit={(entry) =>
          void navigateFromSearch({
            projectId: entry.projectId,
            sessionId: entry.sessionId,
          })
        }
      />
      <PeerViewSwitcher
        ref={(handle) => (peerViewSwitcher = handle)}
        views={focusedPeerViews()}
        onFocus={(view) => {
          void focusEditorWindow(view);
        }}
        onClose={(view) => {
          if (view.nativeLabel) void closeItemWindow(view.nativeLabel);
        }}
      />
      <ShortcutHelpOverlay
        open={helpOpen()}
        onClose={() => setHelpOpen(false)}
        onOpenKeyboardSettings={openKeyboardSettings}
      />
      <ProjectRemovalDialog review={projectRemovalReview} />
      <MoveToProjectDialog
        open={moveTarget() != null}
        busy={moveBusy() || projectPromotionPending(activeProject())}
        error={moveError()}
        onPickFolder={async () => {
          setMoveError(null);
          try {
            return await pickProjectFolder();
          } catch (err) {
            setMoveError(dialogError(err));
            return null;
          }
        }}
        onSubmit={(args) => void submitMove(args)}
        onClose={() => setMoveTarget(null)}
      />
      <CloneRepoDialog
        open={cloneOpen()}
        busy={cloneBusy()}
        error={cloneError()}
        onPickParent={async () => {
          setCloneError(null);
          try {
            return await pickProjectFolder();
          } catch (err) {
            setCloneError(dialogError(err));
            return null;
          }
        }}
        onSubmit={(args) => void submitClone(args)}
        onClose={() => setCloneOpen(false)}
      />
      <OpenFolderConfirm
        open={openFolderPath() != null}
        path={openFolderPath()}
        detect={openFolderDetect()}
        busy={openFolderBusy()}
        onConfirm={(choice) => void confirmOpenFolder(choice)}
        onClose={closeOpenFolder}
      />
      <Show when={foldersPanelProjectId()} keyed>
        {(projectId) => (
            <WorkspaceFoldersPanel
              open
              projectId={projectId}
              roots={
                props.projects
                  .byId(projectId)
                  ?.roots.filter((root) => root.kind === "attached") ?? []
              }
              onClose={() => setFoldersPanelProjectId(null)}
              onAttach={async (path) => {
                const client = getLycaonClient();
                if (!client) return;
                try {
                  const updated = await client.attachProjectRoot(projectId, { path });
                  props.projects.upsert(updated);
                } catch (err) {
                  noticeReporterFor(projectScope(projectId)).reportError(err);
                }
              }}
              onDetach={(rootId) => detachProjectRoot(projectId, rootId)}
              onSetPrimary={async (rootId) => {
                const client = getLycaonClient();
                if (!client) return;
                const updated = await client.updateProjectRoot(projectId, rootId, {
                  is_primary: true,
                });
                props.projects.upsert(updated);
              }}
              onRenameLabel={async (rootId, label) => {
                const client = getLycaonClient();
                if (!client) return;
                const updated = await client.updateProjectRoot(projectId, rootId, {
                  label,
                });
                props.projects.upsert(updated);
              }}
            />
        )}
      </Show>
      <Show when={diffViewerRequest()} keyed>
        {(req) => {
          const project = req.projectId
            ? props.projects.byId(req.projectId)
            : undefined;
          const rootRefs = (project?.roots ?? []).map((root) => ({
            id: root.id,
            path: root.path,
            is_primary: root.is_primary,
          }));
          let request = req;
          const resolved = resolveSourceRequest({
            projectId: req.projectId ?? "",
            path: req.path,
            intent: "transient",
          });
          if (resolved.status === "resolved") {
            request = {
              ...req,
              absolutePath: resolved.request.absolutePath,
              rootId: resolved.request.rootId,
            };
          }
          return (
            <DiffViewerDrawer
              request={request}
              rootRefs={rootRefs}
            />
          );
        }}
      </Show>
      <FirstTimeTipsLayer />
    </div>
    </Show>
    </Show>
  );
}
