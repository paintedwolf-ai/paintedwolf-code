import { batch } from "solid-js";
import type { WorkflowSummary } from "../../api/types.ts";
import { prepareProjectScope } from "../../chat/session/prepare-project.ts";
import {
  bindCreatedSession,
  createSessionForProject,
  enrichCreatedSession,
  recordCreatedSessionInRecents,
  refreshCreatedSessionBoard,
} from "../../chat/session/session-lifecycle.ts";
import type { SessionScope } from "../../chat/session/session-scope.ts";
import {
  runCreateSession,
  shellSessionSwitchGeneration,
  type SessionSwitchDeps
} from "../../chat/session/session-switch.ts";
import { requestArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { projectScope } from "../../notices/notice-scope.ts";
import { sessionSummaryFromSession, type ProjectSessionsStore } from "../../store/project-sessions-store.ts";
import {
  connectAppBackend,
  getLycaonClient,
  noticeReporterFor
} from "../../platform/connection/app-connection.ts";
import { UNTITLED_PROJECT_NAME } from "../../project/project-summary.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { createRecentsStore } from "../../store/recents-store.ts";
import type { DraftTarget, SwitchKind, createShellStore } from "../../store/shell-store.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;
type RecentsStore = ReturnType<typeof createRecentsStore>;
type ShellStore = ReturnType<typeof createShellStore>;

export function stageSwitchKind(
  shell: ShellStore,
  targetProjectId: string,
): SwitchKind {
  const current = shell.state.activeProjectId;
  if (!current || current === targetProjectId) return "same-project";
  return "cross-project";
}

export function projectDirForScope(
  projects: ProjectsStore,
  appStore: AppStore,
  scope: SessionScope,
): string {
  const project = projects.byId(scope.projectId);
  return (
    project?.roots.find((r) => r.is_primary)?.path ??
    project?.roots[0]?.path ??
    appStore.state.currentSession?.workspace_path?.trim() ??
    ""
  );
}

export function assertHydratedForeground(
  appStore: AppStore,
  sessionId: string,
  shouldApply: () => boolean,
): void {
  if (
    shouldApply() &&
    appStore.state.currentSession?.id?.trim() !== sessionId
  ) {
    throw new Error("Session hydrate did not bind the foreground session.");
  }
}

export function createSessionCreation(options: {
  shell: ShellStore;
  appStore: AppStore;
  projects: ProjectsStore;
  projectSessions?: ProjectSessionsStore;
  recents: RecentsStore;
  showProjects: () => void;
  beginWorkspaceOpening: (label: string) => number;
  conversationInLayout: () => boolean;
  sessionSwitchDeps: () => SessionSwitchDeps;
}) {
  const startEmptySession = async (
    target: DraftTarget,
    opts?: {
      armWorkflow?: WorkflowSummary;
      switchKind?: SwitchKind;
      stageSwitchDone?: boolean;
    },
  ) => {
    const creationGeneration = shellSessionSwitchGeneration.next();
    // Fence responses until the new project has a navigation target.
    let openingToken: number | null = null;
    const creationIsCurrent = () =>
      shellSessionSwitchGeneration.isLatest(creationGeneration) &&
      (openingToken == null || options.shell.state.opening?.token === openingToken);
    // Capture switch kind before active project changes.
    const switchKind: SwitchKind =
      opts?.switchKind ??
      (target.kind === "new-project"
        ? "cross-project"
        : stageSwitchKind(options.shell, target.projectId));

    if (!opts?.stageSwitchDone) {
      if (target.kind === "new-session") {
        options.shell.beginStageSwitch({
          projectId: target.projectId,
          kind: switchKind,
        });
      } else {
        openingToken = options.beginWorkspaceOpening(UNTITLED_PROJECT_NAME);
      }
    }
    let client = getLycaonClient();
    if (!client) {
      client = await connectAppBackend(options.appStore, { reportFailure: true }).catch(
        () => null,
      );
    }
    if (!creationIsCurrent()) return;
    if (!client) {
      options.appStore.actions.resetChatForSessionSwitch();
      options.shell.clearToHome();
      options.showProjects();
      return;
    }
    const activeClient = client;
    let projectId = target.kind === "new-session" ? target.projectId : "";
    try {
      if (target.kind === "new-project") {
        const project = await activeClient.createProject({ roots: [], draft: true });
        if (!creationIsCurrent()) return;
        projectId = project.id;
        // Publish the project and navigation target in one reactive update.
        batch(() => {
          options.projects.upsert(project);
          if (openingToken != null) {
            options.shell.beginStageSwitch({
              projectId: project.id,
              kind: "cross-project",
            });
          }
        });
      }

      if (!options.conversationInLayout()) options.showProjects();
      const baseDeps = options.sessionSwitchDeps();
      await runCreateSession({
        generation: shellSessionSwitchGeneration,
        projectId,
        deps: {
          ...baseDeps,
          prepareProject: (id) =>
            prepareProjectScope(options.appStore, options.projects, id),
        },
        hasClient: true,
        create: async ({ shouldApply }) => {
          const session = await createSessionForProject(activeClient, projectId);
          if (!shouldApply()) {
            return {
              projectId: session.project_id?.trim() || projectId,
              sessionId: session.id,
            };
          }
          if (opts?.armWorkflow) {
            requestArmWorkflow({
              sessionId: session.id,
              workflow: opts.armWorkflow,
            });
          }
          bindCreatedSession(options.appStore, session);
          // The sidebar shows the new chat with the workspace, not on the next list refresh.
          options.projectSessions?.insertRow(sessionSummaryFromSession(session));
          return {
            projectId: session.project_id?.trim() || projectId,
            sessionId: session.id,
          };
        },
        enrich: async (scope, { shouldApply }) => {
          await workspacePreparation(scope.projectId).run(
            "session-enrichment",
            async () => {
              await enrichCreatedSession(
                options.appStore,
                activeClient,
                scope.sessionId,
                options.projects.state.projects,
                { projectId: scope.projectId, shouldApply },
                options.recents,
              );
              assertHydratedForeground(
                options.appStore,
                scope.sessionId,
                shouldApply,
              );
            },
          );
        },
        afterEnrich: (hydratedScope) => {
          const projectDir = projectDirForScope(
            options.projects,
            options.appStore,
            hydratedScope,
          );
          if (!projectDir) return;
          // The board and its git read land with the workspace.
          void workspacePreparation(hydratedScope.projectId).run(
            "session-board",
            () => refreshCreatedSessionBoard(
              options.appStore, activeClient, options.projects, projectDir, hydratedScope.sessionId,
            ),
          );
        },
        onOpened: (scope) => recordCreatedSessionInRecents(options.recents, scope),
      });
    } catch (err) {
      if (!shellSessionSwitchGeneration.isLatest(creationGeneration)) return;
      options.appStore.actions.resetChatForSessionSwitch();
      options.shell.clearToHome();
      options.showProjects();
      const errorProjectId = projectId ||
        (target.kind === "new-session" ? target.projectId : "");
      noticeReporterFor(projectScope(errorProjectId)).reportError(err);
    }
  };

  return startEmptySession;
}
