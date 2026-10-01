import { createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import { contributionFrameReady } from "../../contributions/contribution-store.ts";
import { deriveProjectPresentation, retainMountedProjectPresentation, type ProjectPresentation } from "../../shell/project-presentation.ts";
import { recordWorkspaceOpen } from "../../shell/workspace-open-diagnostic.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import { isResolved } from "../../store/load-state.ts";
import { useSurfaceReveal } from "../../ui/surface-reveal.ts";
import type { ShellScope } from "./shell-scope.ts";

type WorkspacePresentationDependencies =
  Pick<ShellScope, "shell" | "appStore" | "projects" | "recents" | "projectSessions" | "stageScope" | "nav">;

/** Keeps the workspace covered until its regions are ready. */
export function createShellWorkspacePresentation({ shell, appStore, projects, recents,
  projectSessions, stageScope, nav }: WorkspacePresentationDependencies) {
  const sessionListLoaded = (projectId: string) =>
    projectSessions.state.projectId === projectId && projectSessions.state.loaded;

  const workspaceContentSettled = () => {
    const projectId = shell.state.activeProjectId;
    if (!projectId || !sessionListLoaded(projectId)) return false;
    if (appStore.state.chatHydrationLock != null) return false;
    return workspacePreparation(projectId).ready() && workspacePreparation("app").ready();
  };

  const workspacePending = () => {
    const projectId = shell.state.activeProjectId;
    if (!projectId) return "project";
    return [
      ...(!sessionListLoaded(projectId) ? ["session-list"] : []),
      ...(appStore.state.chatHydrationLock != null ? ["chat-hydration"] : []),
      ...workspacePreparation(projectId).pending(),
      ...workspacePreparation("app").pending().map((name) => `app:${name}`),
    ].join(",");
  };

  const projectPresentation = createMemo<ProjectPresentation>((previous) => {
    const scope = stageScope();
    return retainMountedProjectPresentation(
      previous,
      deriveProjectPresentation({
        projectId: shell.state.activeProjectId,
        projectsLoaded: isResolved(projects.state.registry),
        recentsLoaded: recents.state.loaded,
        contributionsReady: contributionFrameReady(),
        contentSettled: workspaceContentSettled(),
        scope,
      }),
      scope,
    );
  });
  const identityReady = () => projectPresentation().ready.identity;
  const workspaceMounted = () => projectPresentation().ready.mounted;
  const workspaceOpen = () => projectPresentation().ready.open;

  /** A new key mounts an opaque cover for each destination. */
  const workspaceVeilKey = (): string | null => {
    if (nav() === "settings") return null;
    const projectId = shell.state.activeProjectId;
    if (projectId) return `project:${projectId}`;
    const opening = shell.state.opening;
    return opening ? `opening:${opening.token}` : null;
  };
  const workspaceVeilLabel = () => {
    const projectId = shell.state.activeProjectId;
    if (projectId) return projects.summary(projectId)?.displayName?.trim() || "Project";
    return shell.state.opening?.label;
  };
  const workspaceVeilNotice = () => {
    const projectId = shell.state.activeProjectId;
    return projectId ? workspacePreparation(projectId).notice() : undefined;
  };

  const workspaceReveal = useSurfaceReveal({
    ready: workspaceOpen,
    deferInitialReveal: true,
  });
  const [revealedProjectId, setRevealedProjectId] =
    createSignal<string | null>(null);
  createEffect((previousProjectId: string | null | undefined) => {
    const projectId = shell.state.activeProjectId;
    if (previousProjectId !== undefined && previousProjectId !== projectId) {
      workspaceReveal.reset();
    }
    if (!projectId) setRevealedProjectId(null);
    return projectId;
  });
  createEffect(() => {
    const projectId = shell.state.activeProjectId;
    if (projectId && workspaceReveal.ready()) {
      setRevealedProjectId(projectId);
    }
  });
  const workspaceRevealed = () => {
    const projectId = shell.state.activeProjectId;
    return Boolean(
      projectId &&
      revealedProjectId() === projectId &&
      workspaceReveal.ready(),
    );
  };
  // Navigation ends the current diagnostic recording.
  createEffect(() => {
    const key = workspaceVeilKey();
    if (!key) return;
    onCleanup(recordWorkspaceOpen(key, workspaceRevealed));
  });
  /** The rail's chat list paints on the workspace's frame. */
  const sidebarChatsPublished = () =>
    workspaceRevealed() &&
    projectSessions.state.projectId === shell.state.activeProjectId &&
    projectSessions.state.loaded;
  /** Status chips prepare after the project mounts. */
  const railStatusProject = createMemo((): string | null =>
    workspaceMounted() ? projectPresentation().projectId : null,
  );

  return {
    projectPresentation, identityReady, workspaceMounted, workspaceOpen, workspacePending,
    workspaceRevealed, workspaceVeilKey, workspaceVeilLabel, workspaceVeilNotice,
    sidebarChatsPublished, railStatusProject,
  };
}
