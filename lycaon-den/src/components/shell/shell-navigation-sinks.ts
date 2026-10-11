import { type ShellScope } from "./shell-scope.ts";
import { onCleanup, onMount } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { registerComposerAttachmentSink } from "../../chat/composer/add-to-chat.ts";
import { composerComposeBlockReason } from "../../chat/composer/composer-rules.ts";
import { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import { setShellNoticeActionSinks } from "../../notices/notice-actions.ts";
import { closeDiffViewer } from "../../platform/navigation/in-app-diff.ts";
import { type ProjectContextSection } from "../../settings/settings-nav-model.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { startWindowIdentityPaint } from "../../platform/windows/window-identity-paint.ts";
import { installWindowResizeEpoch } from "../../shell/window-resize.ts";
import { installWalkTranscriptMark } from "../../chat/transcript/presentation/walk-transcript-mark.ts";
import { startItemWindowViewMirror } from "../../platform/windows/item-windows.ts";
import { startWorkspaceViewPresentationMirror } from "../../platform/windows/workspace-view-registry.ts";
import { registerOpenInSearchSink } from "../../search/search-nav.ts";
import { registerOpenSourceSink, registerOpenSourceProjectLookup } from "../../platform/navigation/open-source.ts";
import { setProjectFilesRevealRequest } from "../../files/tree/project-files-reveal.ts";
import { openSourceInFilesStage } from "../../files/components/project-files-open.ts";
import { applyOpenFilesSurfaceRequest, registerOpenFilesSurfaceSink } from "../../platform/navigation/open-files-surface.ts";
import { registerOpenFindingSink, setFindingFocusRequest } from "../../platform/navigation/open-finding.ts";
import { selectSecurityFolder } from "../../platform/files/security-folder-selection.ts";
import { registerNotificationSessionOpenSink } from "../../notifications/notification-open.ts";
import { registerChatDestinationOpenSink } from "../../chat/composer/chat-destination.ts";
import { registerEscapeLadderLayer } from "../../find/escape-ladder.ts";
import { revealHighlightActive, clearRevealHighlight } from "../../chat/transcript/presentation/reveal-highlight.ts";
import { registerApprovalRevealSink } from "../../chat/checkpoint/approval-reveal.ts";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { revealChicklet } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { type SessionNavigation } from "./session-navigation.ts";
import { type ShellPaneVisibility } from "./shell-pane-visibility.ts";
import { type ShellSearchState } from "./shell-search-state.ts";
import { type ShellNavigation } from "./shell-navigation-state.ts";

type NavigationSinkDependencies = Pick<ShellScope, "shell" | "projects" | "appStore" | "activeChat" | "setNav">
  & Pick<ShellSearchState, "openSearch" | "closeSearch" | "closeCrossbar"> & Pick<ShellPaneVisibility, "revealConversation">
  & Pick<SessionNavigation, "resumeSession"> & {
  openStageWithPlacement: (stage: ContextNavItemId, arrival: "routed", open?: () => void) => void;
  openNotificationSession: (sessionId: string) => void;
  openWorkers: (workerId?: string) => void;
  openSettings: ShellNavigation["openSettings"];
  selectContextSection: (section: ProjectContextSection) => void;
  showNoProviderBanner: () => boolean;
};

/** Register before the shell becomes visible; cleanup shares the shell lifetime. */
export function installShellNavigationSinks({ shell, projects, appStore, openStageWithPlacement,
  openSearch, openNotificationSession, revealConversation,
  resumeSession, activeChat, openWorkers, closeSearch, closeCrossbar, setNav, openSettings,
  selectContextSection, showNoProviderBanner }: NavigationSinkDependencies) {
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
          appStore.state.sidecarStatus,
          activeChat()?.sessionId ?? null,
          catalogWorkflowRun(
            appStore.state.activeWorkflowRun,
            appStore.state.workflowRuns,
          ),
          appStore.state.chatHydrationLock,
          showNoProviderBanner(),
          appStore.state.currentSession?.status,
        ),
      projectId: () =>
        activeChat()?.projectId ??
        shell.state.activeProjectId ??
        "",
      projectRoots: () => {
        const id =
          activeChat()?.projectId ?? shell.state.activeProjectId ?? "";
        if (!id) return [];
        return projects.byId(id)?.roots ?? [];
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

  const stopWindowResize = installWindowResizeEpoch();
  // Chat marking follows the Walk playhead.
  const stopWalkMark = installWalkTranscriptMark();
  onCleanup(stopWalkMark);
  let mirrorsDisposed = false;
  let stopItemWindowMirror: (() => void) | undefined;
  let stopWorkspaceViewMirror: (() => void) | undefined;
  onCleanup(startWindowIdentityPaint());
  void startItemWindowViewMirror()
    .then((stop) => {
      if (mirrorsDisposed) stop();
      else stopItemWindowMirror = stop;
    })
    .catch((err) => console.debug("[item-window] registry mirror unavailable", err));
  void startWorkspaceViewPresentationMirror()
    .then((stop) => {
      if (mirrorsDisposed) stop();
      else stopWorkspaceViewMirror = stop;
    })
    .catch((err) =>
      console.debug("[workspace-views] presentation mirror unavailable", err),
    );

  const unregisterOpenInSearch = registerOpenInSearchSink((req) => {
    // Detached Search requests open locally.
    openStageWithPlacement("search", "routed", () =>
      openSearch({ originProjectId: req.originProjectId, seed: req.query }));
  });
  // Source requests target this workspace's Files stage.
  const unregisterOpenSource = registerOpenSourceSink((req) => {
    if (shell.state.activeProjectId !== req.projectId) {
      shell.beginStageSwitch({ projectId: req.projectId, kind: "cross-project" });
    }
    if ("action" in req) {
      setProjectFilesRevealRequest({ projectId: req.projectId, rootId: req.rootId, path: req.path, isDir: req.entryKind === "folder" });
      openStageWithPlacement("files", "routed");
      return;
    }
    const project = projects.byId(req.projectId);
    openSourceInFilesStage({
      request: req,
      rootLabelFor: (rootId) => {
        const root = project?.roots.find((r) => r.id === rootId);
        return root?.label ?? rootId;
      },
      navigate: () => openStageWithPlacement("files", "routed"),
    });
  });
  const unregisterOpenFilesSurface = registerOpenFilesSurfaceSink((req) => {
    applyOpenFilesSurfaceRequest(req, projects.byId(req.projectId)?.roots ?? []);
    openStageWithPlacement("files", "routed");
  });
  const unregisterOpenFinding = registerOpenFindingSink((req) => {
    if (req.rootId) selectSecurityFolder(req.projectId, req.rootId);
    setFindingFocusRequest(req);
    openStageWithPlacement("security", "routed");
  });
  const unregisterProjectLookup = registerOpenSourceProjectLookup((id) => {
    const project = projects.byId(id);
    return project ? { roots: project.roots } : undefined;
  });
  const unregisterNotificationOpen = registerNotificationSessionOpenSink(
    (sessionId) => openNotificationSession(sessionId),
  );
  const unregisterChatDestinationOpen = registerChatDestinationOpenSink(async (destination) => {
    void revealConversation();
    await resumeSession({ ...destination, title: "" });
  });
  // Escape ends the marking a reveal left on a row.
  const unregisterRevealHighlight = registerEscapeLadderLayer(
    "revealHighlight",
    {
      isOpen: revealHighlightActive,
      dismiss: () => void clearRevealHighlight(),
    },
  );
  const unregisterApprovalReveal = registerApprovalRevealSink((req) => {
    const chat = activeChat();
    const workers = chat
      ? sessionWorkers(appStore, chat.sessionId)
      : [];
    const worker = workers.find(
      (row) => row.child_session_id?.trim() === req.sessionId,
    );
    if (worker) {
      openWorkers(worker.id);
    }
    void revealChicklet(
      () =>
        document.querySelector<HTMLElement>(".den-worker-transcript-pane") ??
        document.querySelector<HTMLElement>(".den-chat-stream"),
      {
        sessionId: req.sessionId,
        anchor: { chicklet: "tool", anchorId: req.toolCallId },
      },
    );
  });

  onCleanup(() => {
    mirrorsDisposed = true;
    stopItemWindowMirror?.();
    stopWorkspaceViewMirror?.();
    stopWindowResize();
    unregisterOpenInSearch();
    unregisterOpenSource();
    unregisterOpenFilesSurface();
    unregisterOpenFinding();
    unregisterProjectLookup();
    unregisterNotificationOpen();
    unregisterChatDestinationOpen();
    unregisterRevealHighlight();
    unregisterApprovalReveal();
  });
}
