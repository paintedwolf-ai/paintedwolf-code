import { createStore } from "solid-js/store";
import type { SidecarConnectionStatus } from "../platform/connection/backend.ts";
import { persistAppStateInBackground } from "./app-state-background-write.ts";

export type DraftTarget =
  | { kind: "new-project" }
  | { kind: "new-session"; projectId: string };

type ShellForeground = {
  projectId: string;
  sessionId: string;
  initialPrompt?: true;
} | null;

export type SwitchKind = "same-project" | "cross-project";

/** A workspace opening for a project the sidecar has not named yet. */
export type WorkspaceOpening = {
  /** Ordinal of this open; a stale continuation or abandon with an older token is ignored. */
  token: number;
  /** What the veil calls the workspace until the project record exists. */
  label: string;
};

/** A submitted Home idea shown as its chat before the host names one. */
export type LaunchForeground = { launchId: string };

export type ShellStoreState = {
  connection: SidecarConnectionStatus;
  activeProjectId: string | null;
  foreground: ShellForeground;
  /** The foreground conversation is a Home launch that has not handed off yet. */
  launch: LaunchForeground | null;
  /** The veil is up ahead of a project id; Home may not paint until the switch lands or the open is abandoned. */
  opening: WorkspaceOpening | null;
};

function persistLastActiveProject(projectId: string | undefined): void {
  void persistAppStateInBackground({ lastActiveProjectId: projectId });
}

export function createShellStore(initialConnection: SidecarConnectionStatus) {
  const [state, setState] = createStore<ShellStoreState>({
    connection: initialConnection,
    activeProjectId: null,
    foreground: null,
    launch: null,
    opening: null,
  });
  let openingTokens = 0;

  function clearToHome() {
    setState({
      activeProjectId: null,
      foreground: null,
      launch: null,
      opening: null,
    });
    persistLastActiveProject(undefined);
  }

  return {
    state,
    setConnection(connection: SidecarConnectionStatus) {
      setState("connection", connection);
    },
    beginStageSwitch(opts: { projectId: string; kind: SwitchKind }) {
      if (opts.kind === "cross-project") {
        setState({
          activeProjectId: opts.projectId,
          foreground: null,
          launch: null,
          opening: null,
        });
      } else {
        setState({
          activeProjectId: opts.projectId,
          launch: null,
          opening: null,
        });
      }
    },
    /**
     * Leaves the current surface for a workspace whose project does not exist yet.
     * The restore target is untouched, so an open that never lands is not restored.
     */
    beginWorkspaceOpening(opts: { label: string }): number {
      const token = ++openingTokens;
      setState({
        activeProjectId: null,
        foreground: null,
        launch: null,
        opening: { token, label: opts.label },
      });
      return token;
    },
    /** Returns home only if the token matches the current open. */
    abandonWorkspaceOpening(token: number) {
      if (state.opening?.token !== token) return;
      clearToHome();
    },
    /** Shows a Home launch as the foreground conversation. */
    openLaunch(launchId: string) {
      setState({
        activeProjectId: null,
        foreground: null,
        launch: { launchId },
        opening: null,
      });
    },
    /** Leaves a launch for Home unless navigation already moved elsewhere. */
    closeLaunch(launchId: string) {
      if (state.launch?.launchId !== launchId) return;
      setState("launch", null);
    },
    commitStageScope(opts: {
      projectId: string;
      sessionId: string;
      initialPrompt?: true;
    }) {
      setState({
        activeProjectId: opts.projectId,
        foreground: {
          projectId: opts.projectId,
          sessionId: opts.sessionId,
          ...(opts.initialPrompt ? { initialPrompt: true } : {}),
        },
        launch: null,
        opening: null,
      });
      persistLastActiveProject(opts.projectId);
    },
    clearToHome,
    evictConversation(projectId: string, sessionId: string) {
      const fg = state.foreground;
      if (fg?.projectId === projectId && fg.sessionId === sessionId) {
        setState("foreground", null);
      }
    },
    evictProjectConversations(projectId: string) {
      if (state.activeProjectId === projectId) {
        clearToHome();
      }
    },
  };
}

export type ShellStore = ReturnType<typeof createShellStore>;
