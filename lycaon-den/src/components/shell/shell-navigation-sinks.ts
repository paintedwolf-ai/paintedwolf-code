import { onCleanup } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
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
import type { SessionNavigation } from "./session-navigation.ts";
import type { ShellPaneVisibility } from "./shell-pane-visibility.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchState } from "./shell-search-state.ts";

type NavigationSinkDependencies = Pick<ShellScope, "shell" | "projects" | "appStore" | "activeChat">
  & Pick<ShellSearchState, "openSearch"> & Pick<ShellPaneVisibility, "revealConversation">
  & Pick<SessionNavigation, "resumeSession"> & {
  openStageWithPlacement: (stage: ContextNavItemId, arrival: "routed", open?: () => void) => void;
  openNotificationSession: (sessionId: string) => void;
  openWorkers: (workerId?: string) => void;
};

/** Register before the shell becomes visible; cleanup shares the shell lifetime. */
export function installShellNavigationSinks({ shell, projects, appStore, openStageWithPlacement,
  openSearch, openNotificationSession, revealConversation,
  resumeSession, activeChat, openWorkers }: NavigationSinkDependencies) {
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
