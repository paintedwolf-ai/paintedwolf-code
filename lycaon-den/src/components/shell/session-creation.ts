import { beginSessionSnapshotRead } from "../../api/session-snapshot-refresh.ts";
import { batch } from "solid-js";
import type { WorkflowSummary } from "../../api/types.ts";
import {
  revokeComposerAttachment,
  type ComposerPendingAttachment,
} from "../../chat/composer/composer-attachments.ts";
import { stageComposerMutation } from "../../chat/composer/composer-document-store.ts";
import { prepareProjectScope } from "../../chat/session/prepare-project.ts";
import {
  bindCreatedSession,
  createSessionForProject,
  enrichCreatedSession,
  recordCreatedSessionInRecents,
  refreshCreatedSessionBoard,
  sendChatPrompt
} from "../../chat/session/session-lifecycle.ts";
import { isPendingSessionId, type SessionScope } from "../../chat/session/session-scope.ts";
import {
  runCreateSession,
  shellSessionSwitchGeneration,
  type SessionSwitchDeps
} from "../../chat/session/session-switch.ts";
import { requestArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import {
  deliverStagedIdea,
  stageHomeIdeaAttachments,
  type HomeIdeaAttachment,
} from "../../home/idea-attachments.ts";
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
      prompt?: string;
      /** Home idea buffer; staged once the draft project exists. */
      ideaAttachments?: readonly HomeIdeaAttachment[];
      armWorkflow?: WorkflowSummary;
      switchKind?: SwitchKind;
      stageSwitchDone?: boolean;
    },
  ) => {
    const materializingHomeDraft =
      target.kind === "new-project" && options.shell.state.materializingDraft;
    const creationGeneration = shellSessionSwitchGeneration.next();
    // Fence responses until the new project has a navigation target.
    let openingToken: number | null = null;
    const creationIsCurrent = () =>
      shellSessionSwitchGeneration.isLatest(creationGeneration) &&
      (!materializingHomeDraft || options.shell.state.materializingDraft) &&
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
      } else if (!materializingHomeDraft) {
        // Home ideas retain Home while the project is created.
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
    // Release staged attachments if another navigation supersedes creation.
    let stagingAttachments: Promise<ComposerPendingAttachment[]> | null = null;
    const discardStagedAttachments = () => {
      void stagingAttachments?.then((staged) => {
        for (const attachment of staged) revokeComposerAttachment(attachment);
      });
    };
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
        if ((opts?.ideaAttachments?.length ?? 0) > 0) {
          // Uploads need only the project, so they overlap session creation.
          stagingAttachments = stageHomeIdeaAttachments(
            activeClient,
            projectId,
            opts?.ideaAttachments ?? [],
          );
        }
      }

      if (!options.conversationInLayout()) options.showProjects();
      let materializedScope: SessionScope | null = null;
      const baseDeps = options.sessionSwitchDeps();
      const holdHome = materializingHomeDraft;
      const created = await runCreateSession({
        generation: shellSessionSwitchGeneration,
        projectId,
        deps: {
          ...baseDeps,
          openSession: holdHome
            ? (scope) => {
              if (!isPendingSessionId(scope.sessionId)) {
                materializedScope = scope;
              }
            }
            : baseDeps.openSession,
          completeChatSessionHydration: holdHome
            ? () => {
              baseDeps.completeChatSessionHydration();
              if (!materializedScope) {
                throw new Error("Created chat did not publish its scope.");
              }
              baseDeps.openSession(materializedScope);
            }
            : baseDeps.completeChatSessionHydration,
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
          bindCreatedSession(options.appStore, session);
          // The sidebar shows the new chat with the workspace, not on the next list refresh.
          options.projectSessions?.insertRow(sessionSummaryFromSession(session));
          const scope = {
            projectId: session.project_id?.trim() || projectId,
            sessionId: session.id,
            ...(opts?.prompt?.trim() || stagingAttachments
              ? { initialPrompt: true as const }
              : {}),
          };
          if (opts?.armWorkflow) {
            requestArmWorkflow({
              sessionId: session.id,
              workflow: opts.armWorkflow,
            });
          }
          return scope;
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
      if (!created) {
        discardStagedAttachments();
        return;
      }
      const out = created.scope;
      const text = opts?.prompt?.trim() ?? "";
      const staged = stagingAttachments ? await stagingAttachments : [];
      if (!text && staged.length === 0) return;
      if (!created.shouldApply()) {
        discardStagedAttachments();
        return;
      }
      let promptClient = getLycaonClient();
      if (!promptClient) {
        try {
          promptClient = await connectAppBackend(options.appStore, { reportFailure: true });
        } catch {
          discardStagedAttachments();
          return;
        }
      }
      if (!created.shouldApply()) {
        discardStagedAttachments();
        return;
      }
      const sendClient = promptClient;
      const sendBackend = beginSessionSnapshotRead(options.appStore, out.sessionId);
      sendBackend.finish();
      const delivery = await deliverStagedIdea({
        text,
        staged,
        send: async (promptText, parts, attachmentLabels) => {
          await sendChatPrompt(
            options.appStore,
            options.recents,
            async () => sendClient,
            out.sessionId,
            out.projectId,
            projectDirForScope(options.projects, options.appStore, out),
            options.projects.state.projects,
            promptText,
            parts.attachments,
            parts.references,
            undefined,
            {
              attachmentLabels: attachmentLabels.length > 0 ? attachmentLabels : undefined,
              shouldSend: (client) => created.shouldApply() && getLycaonClient() === client && sendBackend.isSameBackend(),
            },
          );
        },
        stageFallback: (chips, draftText) =>
          stageComposerMutation(
            { projectId: out.projectId, sessionId: out.sessionId },
            chips,
            draftText || undefined,
          ),
      });
      if (delivery.mode === "failed") {
        noticeReporterFor(projectScope(out.projectId)).reportError(
          new Error(delivery.reason),
        );
      }
      return delivery.mode !== "failed";
    } catch (err) {
      discardStagedAttachments();
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
