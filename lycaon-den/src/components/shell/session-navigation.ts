import { createEffect, createMemo } from "solid-js";
import { chatListSortPref } from "../../settings/chat/chat-list-prefs.ts";
import { type ShellScope } from "./shell-scope.ts";
import { beginSessionSnapshotRead } from "../../api/session-snapshot-refresh.ts";
import { rememberSessionChatFromStore, restoreCachedSessionChat } from "../../chat/session/session-chat-cache.ts";
import { isPendingSessionId, type SessionScope } from "../../chat/session/session-scope.ts";
import { type SessionSwitchDeps, runResumeSession, shellSessionSwitchGeneration } from "../../chat/session/session-switch.ts";
import { completeChatHydrationAndReplay } from "../../chat/transcript/projection/message-events.ts";
import { hasDenTranscriptMessages } from "../../chat/transcript/projection/message-transcript.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { getEntityRetire, getLycaonClient, noticeReporterFor, connectAppBackend } from "../../platform/connection/app-connection.ts";
import { sessionSummaryFromSession } from "../../store/project-sessions-store.ts";
import { sidebarChatSections, type SidebarChatRow } from "../../store/projects-sidebar-model.ts";
import { type RecentSession } from "../../../shared/app-state-types.ts";
import { prepareProjectScope } from "../../chat/session/prepare-project.ts";
import { resumeChatSession } from "../../chat/session/session-lifecycle.ts";
import { deleteSession, retireSessionLocally, setSessionArchived, type LocalRetirementDeps, type SessionRetirementDeps } from "../../chat/session/session-retirement.ts";
import { resolveSidebarPick } from "../../chat/session/sidebar-pick.ts";
import { focusItemWindow, itemWindowViews, openItemWindow } from "../../platform/windows/item-windows.ts";
import { workspaceContextIsOpen } from "../../platform/windows/workspace-view-registry.ts";
import { confirmChatDelete } from "../../session/confirm-chat-delete.ts";
import { type SwitchKind } from "../../store/shell-store.ts";
import { assertHydratedForeground, createSessionCreation, stageSwitchKind } from "./session-creation.ts";
import { type ShellPaneVisibility } from "./shell-pane-visibility.ts";
import { type ShellSearchState } from "./shell-search-state.ts";

export type SessionNavigation = ReturnType<typeof createSessionNavigation>;

export function createSessionNavigation(options: Pick<ShellScope,
  "shell" | "appStore" | "projects" | "recents" | "projectSessions" | "activeChat" | "showProjects" | "nav">
  & Pick<ShellPaneVisibility, "revealConversation"> & Pick<ShellSearchState, "closeSearch" | "closeCrossbar"> & {
  conversationInLayout: () => boolean;
  closeLauncher: () => void;
  focusComposer: () => void;
}) {
  // Refresh chat inventory with project and connection state.
  createEffect(() => {
    options.projectSessions.setProject(options.shell.state.activeProjectId ?? null);
  });
  createEffect(() => {
    options.projectSessions.setSort(chatListSortPref());
  });

  const activeChatScope = createMemo(() => {
    const chat = options.activeChat();
    return chat ? { projectId: chat.projectId, sessionId: chat.sessionId } : null;
  });

  /** The sidebar's groups; a selected chat outside them is listed last. */
  const sidebarSections = createMemo(() => {
    const chat = options.activeChat();
    const current = options.appStore.state.currentSession;
    const selected =
      chat && current && current.id === chat.sessionId &&
      current.project_id === options.projectSessions.state.projectId &&
      current.archived_at == null && !current.parent_session_id
        ? { ...sessionSummaryFromSession(current), message_count: options.appStore.state.messages.length }
        : null;
    return sidebarChatSections(
      options.projectSessions.state.rows,
      options.projectSessions.state.sort,
      selected,
    );
  });

  // Internal-only rows leave the chat fresh.
  const onFreshSession = createMemo(
    () =>
      options.nav() === "projects" &&
      options.activeChat() != null &&
      !hasDenTranscriptMessages(options.appStore.state.messages),
  );

  const sessionSwitchDeps = (): SessionSwitchDeps => ({
    // Failed session transitions return to Home.
    returnToHome: () => {
      options.shell.clearToHome();
      options.showProjects();
    },
    clearChatForSessionSwitch: (hydrationLock) =>
      options.appStore.actions.clearChatForSessionSwitch(hydrationLock),
    beginSessionResumeSwitch: (hydrationLock) =>
      options.appStore.actions.beginSessionResumeSwitch(hydrationLock),
    completeChatSessionHydration: () =>
      completeChatHydrationAndReplay(options.appStore),
    resetChat: () => options.appStore.actions.resetChatForSessionSwitch(),
    reportError: (err, scope) => noticeReporterFor(scope).reportError(err),
    openSession: (scope) =>
      options.shell.commitStageScope({
        projectId: scope.projectId,
        sessionId: scope.sessionId,
        initialPrompt: scope.initialPrompt,
      }),
    setConnected: () => options.shell.setConnection("connected"),
    snapshotSessionChat: () => rememberSessionChatFromStore(options.appStore),
    restoreCachedSessionChat: (scope) =>
      restoreCachedSessionChat(options.appStore, {
        projectId: scope.projectId,
        sessionId: scope.sessionId,
      }),
  });

  const renameSession = async (
    row: { projectId: string; sessionId: string },
    title: string,
  ) => {
    const client = getLycaonClient();
    if (!client) return;
    const read = beginSessionSnapshotRead(options.appStore, row.sessionId);
    try {
      const updated = await client.updateSession(row.sessionId, { title });
      const nextTitle = updated.title?.trim() || title.trim();
      if (read.isCurrent()) {
        options.appStore.actions.setSessionTitle(row.sessionId, nextTitle);
      }
      await options.recents.registerSession({
        projectId: row.projectId,
        sessionId: row.sessionId,
        title: nextTitle,
      });
      void options.projectSessions.refresh();
    } catch (err) {
      noticeReporterFor(sessionScope(row.projectId, row.sessionId)).reportError(err);
    } finally {
      read.finish();
    }
  };

  const openProject = (projectId: string) => {
    options.closeSearch();
    const kind = stageSwitchKind(options.shell, projectId);
    enterProject(projectId, kind, { stageSwitchDone: true });
  };

  const enterProject = (
    projectId: string,
    kind: SwitchKind,
    opts?: { stageSwitchDone?: boolean; recent?: RecentSession },
  ) => {
    options.shell.beginStageSwitch({
      projectId,
      kind,
    });
    options.closeLauncher();
    options.showProjects();
    const recent =
      opts?.recent ??
      options.recents.state.recents.find((r) => r.projectId === projectId);
    if (recent) {
      void resumeSession(recent, { kind, stageSwitchDone: opts?.stageSwitchDone });
    } else {
      void startEmptySession(
        { kind: "new-session", projectId },
        { switchKind: kind, stageSwitchDone: opts?.stageSwitchDone },
      );
    }
  };

  const newProject = () => {
    options.closeSearch();
    options.closeCrossbar();
    options.closeLauncher();
    options.showProjects();
    void startEmptySession({ kind: "new-project" });
  };

  const goHome = () => {
    options.closeSearch();
    options.shell.clearToHome();
    options.closeLauncher();
    options.showProjects();
  };

  const localRetirementDeps = (): LocalRetirementDeps => ({
    activeChat: activeChatScope,
    recents: options.recents,
    retireEntity: (scope) => getEntityRetire()?.session(scope),
    resumeSession: (next, options) => resumeSession(next, options),
    evictConversation: (projectId, sessionId) =>
      options.shell.evictConversation(projectId, sessionId),
  });

  const sidebarRowAsRecent = (row: SidebarChatRow): RecentSession => ({
    projectId: row.projectId,
    sessionId: row.sessionId,
    title: row.title,
  });

  const sessionRetirementDeps = (): SessionRetirementDeps => ({
    client: getLycaonClient,
    reportError: (error, target) =>
      noticeReporterFor(sessionScope(target.projectId, target.sessionId)).reportError(error),
    removeProjectRow: options.projectSessions.removeRow,
    applyProjectRowPatches: options.projectSessions.applyRowPatches,
    nextPinRank: options.projectSessions.nextPinRank,
    pinnedIds: options.projectSessions.pinnedIds,
    refreshProjectRows: () => void options.projectSessions.refresh(),
    retireLocally: (target) => retireSessionLocally(localRetirementDeps(), target),
  });

  const archiveSidebarSession = async (row: SidebarChatRow) => {
    await setSessionArchived(sessionRetirementDeps(), sidebarRowAsRecent(row), true);
  };

  const deleteSidebarSession = async (row: SidebarChatRow) => {
    if (!(await confirmChatDelete())) return;
    await deleteSession(sessionRetirementDeps(), sidebarRowAsRecent(row));
  };

  const resumeSession = async (
    row: RecentSession,
    opts?: { kind?: SwitchKind; stageSwitchDone?: boolean; keepStage?: boolean },
  ) => {
    const navigationGeneration = shellSessionSwitchGeneration.next();
    // A conversation column keeps its split companion during navigation.
    const keepStage = opts?.keepStage ?? options.conversationInLayout();
    if (!keepStage) {
      options.closeSearch();
      options.showProjects();
    }
    const kind = opts?.kind ?? stageSwitchKind(options.shell, row.projectId);
    if (!opts?.stageSwitchDone) {
      options.shell.beginStageSwitch({ projectId: row.projectId, kind });
    }
    // The current session needs no rehydration.
    const fg = activeChatScope();
    const currentId = options.appStore.state.currentSession?.id?.trim();
    if (
      fg &&
      fg.projectId === row.projectId &&
      fg.sessionId === row.sessionId &&
      !options.appStore.state.chatHydrationLock &&
      currentId === row.sessionId
    ) {
      return;
    }
    let client = getLycaonClient();
    if (!client) {
      try {
        client = await connectAppBackend(options.appStore);
      } catch {
        client = null;
      }
    }
    if (!shellSessionSwitchGeneration.isLatest(navigationGeneration)) return;
    const hadClient = Boolean(client);
    const scope: SessionScope = {
      projectId: row.projectId,
      sessionId: row.sessionId,
    };
    await runResumeSession({
      generation: shellSessionSwitchGeneration,
      scope,
      kind,
      deps: {
        ...sessionSwitchDeps(),
        prepareProject: (projectId) =>
          prepareProjectScope(options.appStore, options.projects, projectId),
      },
      hasClient: hadClient,
      hydrate: async ({ shouldApply, signal }) => {
        const activeClient = getLycaonClient();
        if (!activeClient) throw new Error("The app is still starting. Try again in a moment.");
        await resumeChatSession(
          options.appStore,
          activeClient,
          scope.sessionId,
          options.projects.state.projects,
          options.recents,
          { projectId: scope.projectId, shouldApply, signal },
        );
        assertHydratedForeground(options.appStore, scope.sessionId, shouldApply);
      },
      onSessionNotFound: (gone) => {
        getEntityRetire()?.session(gone);
        options.shell.evictConversation(gone.projectId, gone.sessionId);
      },
    });
    if (!hadClient && !keepStage) {
      options.showProjects();
    }
  };

  /** Resolve a sidebar pick from local and peer visibility. */
  const selectSidebarSession = async (row: SidebarChatRow) => {
    void options.revealConversation();
    const subject = activeChatScope();
    const peer = itemWindowViews().find(
      (view) => view.kind === "session" && view.sessionId === row.sessionId,
    );
    const outcome = resolveSidebarPick({
      onScreen: workspaceContextIsOpen({
        kind: "session",
        projectId: row.projectId,
        sessionId: row.sessionId,
      }),
      conversationInLayout: options.conversationInLayout(),
      hasPeerWindow: peer != null,
      isSubject:
        subject?.projectId === row.projectId &&
        subject?.sessionId === row.sessionId,
    });
    switch (outcome) {
      case "reveal":
        await resumeSession(sidebarRowAsRecent(row));
        return;
      case "raise-peer":
        if (peer) void focusItemWindow(peer.label);
        await resumeSession(sidebarRowAsRecent(row), { keepStage: true });
        return;
      case "show-subject":
        options.showProjects();
        return;
      case "select-only":
        await resumeSession(sidebarRowAsRecent(row), { keepStage: true });
        return;
    }
  };

  const openSessionInNewWindow = (row: SidebarChatRow) => {
    void openItemWindow({
      kind: "session",
      projectId: row.projectId,
      sessionId: row.sessionId,
      title: row.title || "Chat",
    }).catch((err) => console.debug("[item-window] open session failed", err));
  };

  /** The outgoing chat stays cached until its replacement is addressable. */
  const beginWorkspaceOpening = (label: string): number => {
    if (options.shell.state.activeProjectId) {
      rememberSessionChatFromStore(options.appStore);
      options.appStore.actions.clearChatForSessionSwitch("*");
    }
    return options.shell.beginWorkspaceOpening({ label });
  };

  /** Failed opens release only their own navigation token. */
  const abandonWorkspaceOpening = (token: number) => {
    if (options.shell.state.opening?.token !== token) return;
    options.appStore.actions.resetChatForSessionSwitch();
    options.shell.abandonWorkspaceOpening(token);
    options.showProjects();
  };

  const startEmptySession = createSessionCreation({
    shell: options.shell, appStore: options.appStore, projects: options.projects, recents: options.recents,
    projectSessions: options.projectSessions,
    showProjects: () => options.showProjects(), beginWorkspaceOpening,
    conversationInLayout: () => options.conversationInLayout(), sessionSwitchDeps: sessionSwitchDeps,
  });

  const startNewChat = async (projectId: string) => {
    options.closeSearch();
    void options.revealConversation();
    if (!options.conversationInLayout()) options.showProjects();
    if (onFreshSession() && options.activeChat()?.projectId === projectId) {
      options.focusComposer();
      return;
    }
    // Repeated opens share pending session creation.
    const fg = options.shell.state.foreground;
    if (
      fg?.projectId === projectId &&
      fg.sessionId &&
      isPendingSessionId(fg.sessionId)
    ) {
      options.focusComposer();
      return;
    }
    await startEmptySession({ kind: "new-session", projectId });
    void options.projectSessions.refresh();
    options.focusComposer();
  };

  return {
    onFreshSession, sidebarSections, renameSession, openProject, newProject, goHome, sidebarRowAsRecent, sessionRetirementDeps,
    archiveSidebarSession, deleteSidebarSession, resumeSession, selectSidebarSession,
    openSessionInNewWindow, beginWorkspaceOpening, abandonWorkspaceOpening,
    startEmptySession, startNewChat,
  };
}
