import { createSignal } from "solid-js";
import type { Project } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { pickProjectFolder } from "../../platform/files/folder.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import { prepareFilesRootDetach, dropFilesBuffersForRoot } from "../../files/documents/project-files-buffers.ts";

export function createProjectLifecycle(options: {
  projects: Pick<ReturnType<typeof createProjectsStore>, "byId" | "upsert" | "applyPatch">;
  activeProjectId: () => string | null | undefined;
  activeProject: () => Project | null | undefined;
  report: (projectId: string, error: unknown) => void;
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
  ): Promise<boolean> {
    const details = error.details ?? {};
    const count = (key: string) => {
      const value = details[key];
      return Array.isArray(value) ? value.length : 0;
    };
    const parts = [
      [count("documents"), "open or unsaved file"],
      [count("sessions"), "active turn"],
      [count("workers"), "worker"],
      [count("overlays"), "unmerged worker result"],
    ]
      .filter(([amount]) => Number(amount) > 0)
      .map(([amount, label]) => `${amount} ${label}${amount === 1 ? "" : "s"}`);
    const blockers = parts.length > 0 ? parts.join(", ") : "in-flight project work";
    return confirmDestructive({
      title,
      message: `${blockers} will be discarded or stopped. Any edits made before removal completes may also be lost. This cannot be undone.`,
      okLabel,
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
      if (!(await confirmForcedLifecycle("Remove folder", "Remove anyway", err))) {
        return;
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
