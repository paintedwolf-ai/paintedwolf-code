import { withdrawNotice } from "../../notices/notice-store.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { type ShellProps } from "./shell-scope.ts";
import { createProjectRemovalReview } from "./ProjectRemovalDialog.tsx";
import { createProjectRemoval } from "./project-removal.ts";
import { createSignal } from "solid-js";
import { invalidateContributionFrame } from "../../contributions/contribution-store.ts";
import { forgetProjectThumbnail } from "../../home/thumbnail-store.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { getEntityRetire, getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { prepareFilesRootDetach, dropFilesBuffersForRoot } from "../../files/documents/project-files-buffers.ts";
import { type Project } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { pickProjectFolder } from "../../platform/files/folder.ts";
import {
  formatBackgroundCommandWithChat,
  sameRunningCommandList,
  type RunningCommandSummary,
} from "../../session/confirm-chat-delete.ts";
import { type createProjectsStore } from "../../store/projects-store.ts";

export function extractRootBusyBackgroundProcesses(
  details: Record<string, unknown> | undefined,
): RunningCommandSummary[] {
  const raw = details?.background_processes;
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((item): item is Record<string, unknown> => typeof item === "object" && item !== null)
    .map((item) => ({
      process_id:
        typeof item.process_id === "string"
          ? item.process_id
          : typeof item.handle === "string"
            ? item.handle
            : undefined,
      handle: typeof item.handle === "string" ? item.handle : undefined,
      session_id: typeof item.session_id === "string" ? item.session_id : undefined,
      session_title: typeof item.session_title === "string" ? item.session_title : undefined,
      session_archived: Boolean(item.session_archived),
      command: typeof item.command === "string" ? item.command : undefined,
      root_id: typeof item.root_id === "string" ? item.root_id : undefined,
      ports: Array.isArray(item.ports)
        ? item.ports.filter((p): p is string => typeof p === "string")
        : undefined,
    }));
}

export function formatForcedLifecycleMessage(
  details: Record<string, unknown> | undefined,
): string {
  const record = details ?? {};
  const count = (key: string) => {
    const value = record[key];
    return Array.isArray(value) ? value.length : 0;
  };
  const bgProcesses = extractRootBusyBackgroundProcesses(record);
  const parts = [
    [count("documents"), "open or unsaved file"],
    [count("sessions"), "active turn"],
    [count("workers"), "worker"],
    [count("overlays"), "unmerged worker result"],
    [bgProcesses.length, "background command"],
  ]
    .filter(([amount]) => Number(amount) > 0)
    .map(([amount, label]) => `${amount} ${label}${amount === 1 ? "" : "s"}`);
  const blockers = parts.length > 0 ? parts.join(", ") : "in-flight project work";
  const summary = `${blockers} will be discarded or stopped. Any edits made before removal completes may also be lost. This cannot be undone.`;
  if (bgProcesses.length === 0) {
    return summary;
  }
  const lines = [
    summary,
    bgProcesses.length === 1
      ? "1 background command bound to this folder will stop:"
      : `${bgProcesses.length} background commands bound to this folder will stop:`,
    ...bgProcesses.map((proc) => `• ${formatBackgroundCommandWithChat(proc)}`),
  ];
  return lines.join("\n");
}

export function createProjectLifecycle(options: {
  projects: Pick<ReturnType<typeof createProjectsStore>, "byId" | "upsert" | "applyPatch">;
  activeProjectId: () => string | null | undefined;
  activeProject: () => Project | null | undefined;
  report: (projectId: string, error: unknown) => void;
  onOpenRunning?: (projectId: string) => void;
}) {
  const [moveTarget, setMoveTarget] = createSignal<string | null>(null);
  const [moveBusy, setMoveBusy] = createSignal(false);
  const [moveError, setMoveError] = createSignal<string | null>(null);
  const patchProjectRow = async (
    projectId: string,
    patch: { starred: boolean } | { name: string },
  ) => {
    const client = getLycaonClient();
    if (!client) return;
    try {
      await options.projects.applyPatch(projectId, patch, () =>
        client.updateProject(projectId, patch),
      );
    } catch (err) {
      options.report(projectId, err);
    }
  };

  const toggleStar = (projectId: string, starred: boolean) =>
    patchProjectRow(projectId, { starred });

  const renameProject = (projectId: string, name: string) =>
    patchProjectRow(projectId, { name });

  const dialogError = (err: unknown): string => {
    if (err instanceof LycaonApiError) {
      const parts = [err.message.trim(), err.suggestedAction?.trim()].filter(Boolean);
      if (parts.length > 0) return parts.join(" ");
    }
    return err instanceof Error && err.message.trim() ? err.message : "Something went wrong.";
  };

  const openMoveDialog = (projectId: string) => {
    setMoveError(null);
    setMoveTarget(projectId);
  };

  const submitMove = async (args: { rootPath: string; initGit: boolean }) => {
    const projectId = moveTarget();
    const client = getLycaonClient();
    if (!projectId || !client) return;
    setMoveBusy(true);
    setMoveError(null);
    try {
      const updated = await client.promoteProject(projectId, {
        root_path: args.rootPath,
        init_git: args.initGit,
      });
      options.projects.upsert(updated);
      setMoveTarget(null);
    } catch (err) {
      setMoveError(dialogError(err));
    } finally {
      setMoveBusy(false);
    }
  };

  const activeProjectRoots = () => options.activeProject()?.roots ?? [];

  function confirmForcedLifecycle(
    title: string,
    okLabel: string,
    error: LycaonApiError,
    projectId: string,
  ): Promise<boolean> {
    const hasBg = extractRootBusyBackgroundProcesses(error.details).length > 0;
    return confirmDestructive({
      title,
      message: formatForcedLifecycleMessage(error.details),
      okLabel,
      onOpenRunning:
        hasBg && options.onOpenRunning
          ? () => options.onOpenRunning?.(projectId)
          : undefined,
    });
  }

  const retryActiveProjectPromotion = async () => {
    const current = options.activeProject();
    const promotion = current?.promotion;
    const client = getLycaonClient();
    if (!current || !promotion || !client) return;
    try {
      const updated = await client.promoteProject(current.id, {
        root_path: promotion.destination_path,
        init_git: promotion.init_git,
      });
      options.projects.upsert(updated);
    } catch (err) {
      options.report(current.id, err);
    }
  };

  const cancelActiveProjectPromotion = async () => {
    const current = options.activeProject();
    const client = getLycaonClient();
    if (!current?.promotion || !client) return;
    try {
      options.projects.upsert(await client.cancelProjectPromotion(current.id));
    } catch (err) {
      options.report(current.id, err);
    }
  };

  let projectRootMutation = Promise.resolve();
  const mutateProjectRoots = async (operation: () => Promise<Project>) => {
    const pending = projectRootMutation.then(operation, operation);
    projectRootMutation = pending.then(
      () => undefined,
      () => undefined,
    );
    return pending;
  };

  const addFolderToActiveProject = async () => {
    const projectId = options.activeProjectId();
    const client = getLycaonClient();
    if (!projectId) return;
    if (!client) {
      options.report(projectId,
        new Error("Can't add a folder while the app is reconnecting."),
      );
      return;
    }
    try {
      const path = await pickProjectFolder();
      if (!path) return;
      const updated = await mutateProjectRoots(() =>
        client.attachProjectRoot(projectId, { path }),
      );
      options.projects.upsert(updated);
    } catch (err) {
      options.report(projectId, err);
    }
  };

  const detachFolderFromActiveProject = async (rootId: string) => {
    const projectId = options.activeProjectId();
    const client = getLycaonClient();
    if (!projectId || !client) return;
    const root = options.projects
      .byId(projectId)
      ?.roots.find((candidate) => candidate.id === rootId);
    if (
      !(await confirmDestructive({
        title: "Remove folder",
        message: `Remove ${root?.label ?? "this folder"} from the project? Files on disk are not deleted.`,
        okLabel: "Remove folder",
      }))
    ) {
      return;
    }
    try {
      await detachProjectRoot(projectId, rootId);
    } catch (err) {
      options.report(projectId, err);
    }
  };

  async function detachProjectRoot(projectId: string, rootId: string) {
    const client = getLycaonClient();
    if (!client) return;
    await prepareFilesRootDetach(projectId, rootId);
    let updated: Project;
    try {
      updated = await mutateProjectRoots(() =>
        client.detachProjectRoot(projectId, rootId),
      );
    } catch (err) {
      if (!(err instanceof LycaonApiError) || err.code !== "root_busy") throw err;
      let busyError: LycaonApiError = err;
      for (;;) {
        if (!(await confirmForcedLifecycle("Remove folder", "Remove anyway", busyError, projectId))) {
          return;
        }
        if (typeof client.listProjectBackgroundProcesses === "function") {
          const currentBg = extractRootBusyBackgroundProcesses(busyError.details);
          let liveForRoot: RunningCommandSummary[] | null = null;
          try {
            const resp = await client.listProjectBackgroundProcesses(projectId);
            liveForRoot = (resp.processes ?? []).filter(
              (p) =>
                p.running &&
                (p.root_id === rootId ||
                  currentBg.some((c) => (c.process_id ?? c.handle) === p.process_id)),
            );
          } catch {
            liveForRoot = null;
          }
          if (liveForRoot !== null && !sameRunningCommandList(currentBg, liveForRoot)) {
            busyError = new LycaonApiError(
              busyError.message,
              busyError.status,
              busyError.code,
              {
                suggestedAction: busyError.suggestedAction,
                details: { ...(busyError.details ?? {}), background_processes: liveForRoot },
              },
            );
            continue;
          }
        }
        break;
      }
      updated = await mutateProjectRoots(() =>
        client.detachProjectRoot(projectId, rootId, { force: true }),
      );
    }
    dropFilesBuffersForRoot(projectId, rootId);
    options.projects.upsert(updated);
  }

  return {
    toggleStar, renameProject, dialogError, moveTarget, setMoveTarget, moveBusy,
    moveError, setMoveError, openMoveDialog, submitMove, activeProjectRoots,
    retryActiveProjectPromotion, cancelActiveProjectPromotion, addFolderToActiveProject,
    detachFolderFromActiveProject, detachProjectRoot,
  };
}

export function createWorkspaceProjectRemoval(
  props: Pick<ShellProps, "projects" | "shell" | "recents">,
  onOpenRunning?: (projectId: string) => void,
) {
  const projectRemovalReview = createProjectRemovalReview();
  const deleteProject = createProjectRemoval({
    review: (assessment, projectId) =>
      projectRemovalReview.show(
        assessment,
        props.projects.summary(projectId)?.displayName ?? "this project",
        onOpenRunning ? () => onOpenRunning(projectId) : undefined,
      ),
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
    report: (error) => noticeReporterFor(APP_SCOPE).reportError(error),
    notify: (notice) => noticeReporterFor(APP_SCOPE).publish(notice),
    progress: (projectId, progress) => {
      if (!progress) {
        withdrawNotice("project_removal_running");
        return;
      }
      const phase = {
        preparing: "Preparing deletion",
        chats: `Deleting chats: ${progress.completed} of ${progress.total} complete`,
        project_data: "Deleting project data",
        files: "Removing managed project files",
        extensions: "Removing selected extensions",
      }[progress.phase];
      noticeReporterFor(APP_SCOPE).publish({
        code: "project_removal_running",
        title: CLIENT_NOTICES.project_removal_running.title,
        message: `${props.projects.summary(projectId)?.displayName ?? "Project"}: ${phase}. You can keep using other projects.`,
        severity: "info",
      });
    },
    extensionsChanged: () => { void invalidateContributionFrame(); },
  });

  return { projectRemovalReview, deleteProject };
}
