import { createSignal, onCleanup } from "solid-js";
import type { FolderDetect } from "../../api/types.ts";
import { invalidateContributionFrame } from "../../contributions/contribution-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { pickProjectFolder } from "../../platform/files/folder.ts";
import { folderWorkspaceLabel, repoWorkspaceLabel } from "../../project/workspace-label.ts";
import { hasInstallableExtensionSuggestions } from "../../settings/extensions/extension-suggestions-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { createShellStore } from "../../store/shell-store.ts";
import type { CloneRepoSubmit } from "../home/CloneRepoDialog.tsx";
import {
  defaultOpenFolderProjectId,
  detectedFolderProjects,
  type OpenFolderChoice,
} from "../home/OpenFolderConfirm.tsx";

import type { createSessionCreation } from "./session-creation.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;
type ShellStore = ReturnType<typeof createShellStore>;

export function createProjectOpening(options: {
  shell: ShellStore;
  projects: Pick<ProjectsStore, "byId" | "upsert" | "summary">;
  showProjects: () => void;
  beginWorkspaceOpening: (label: string) => number;
  abandonWorkspaceOpening: (token: number) => void;
  reportError: (error: unknown) => void;
  formatError: (error: unknown) => string;
  startSession: ReturnType<typeof createSessionCreation>;
}) {
  let disposed = false;
  let folderRequest = 0;
  let pickingFolder = false;
  let pendingClonedProjectId: string | null = null;
  onCleanup(() => { disposed = true; folderRequest += 1; });
  const [cloneOpen, setCloneOpen] = createSignal(false);
  const [cloneBusy, setCloneBusy] = createSignal(false);
  const [cloneError, setCloneError] = createSignal<string | null>(null);
  const [openFolderPath, setOpenFolderPath] = createSignal<string | null>(null);
  const [openFolderDetect, setOpenFolderDetect] = createSignal<FolderDetect | null>(null);
  const [openFolderBusy, setOpenFolderBusy] = createSignal(false);
  const applyOpenFolderChoice = async (projectId: string, choice: OpenFolderChoice) => {
    const client = getLycaonClient();
    if (!client || choice.installIds.length === 0) return;
    const suggestions = openFolderDetect()?.extension_suggestions;
    if (!suggestions) return;
    const catalog = await client.getExtensionsCatalog();
    await client.acceptExtensionSuggestions(projectId, {
      pack_ids: choice.installIds,
      expected_revision: catalog.revision,
      expected_suggestion_revision: suggestions.suggestion_revision,
    });
    void invalidateContributionFrame();
  };

  const openFolderAsProject = async (
    path: string,
    choice: OpenFolderChoice | null,
    onProjectReady?: () => void,
  ) => {
    const request = folderRequest;
    const client = getLycaonClient();
    if (!client) return;
    const selectedId =
      choice === null
        ? null
        : choice.projectId !== undefined
          ? choice.projectId
          : defaultOpenFolderProjectId(openFolderDetect());
    const matchedName = selectedId
      ? options.projects.summary(selectedId)?.displayName ??
        detectedFolderProjects(openFolderDetect()).find((p) => p.id === selectedId)?.name
      : undefined;
    // Cover the outgoing workspace before the project-created event arrives.
    const token = options.beginWorkspaceOpening(
      matchedName || folderWorkspaceLabel(path),
    );
    const current = () => !disposed && request === folderRequest && options.shell.state.opening?.token === token;
    try {
      let project = selectedId
        ? await client.getProject(selectedId)
        : await client.createProject({ roots: [{ path }] });
      if (!current()) return;
      options.projects.upsert(project);
      if (choice) {
        await applyOpenFolderChoice(project.id, choice);
        if (!current()) return;
      }
      project = options.projects.byId(project.id) ?? project;
      options.showProjects();
      onProjectReady?.();
      await options.startSession(
        { kind: "new-session", projectId: project.id },
        { switchKind: "cross-project" },
      );
    } catch (err) {
      if (!current()) return;
      options.abandonWorkspaceOpening(token);
      // Preserve the structured write-root error.
      options.reportError(err);
      throw err;
    }
  };

  const startOpenFolder = async () => {
    if (disposed || pickingFolder || openFolderPath() != null || openFolderBusy()) return;
    const client = getLycaonClient();
    if (!client) {
      options.reportError(
        new Error("Can't open a folder while the app is reconnecting."),
      );
      return;
    }
    let path: string | null;
    pickingFolder = true;
    try {
      path = await pickProjectFolder();
    } catch (err) {
      if (!disposed) options.reportError(err);
      return;
    } finally {
      pickingFolder = false;
    }
    if (disposed || !path) return;
    await proposeFolderAsProject(path);
  };

  const proposeFolderAsProject = async (
    path: string,
    proposeOptions?: { forceNew?: boolean },
  ) => {
    if (disposed) return;
    const forceNew = proposeOptions?.forceNew === true;
    const request = ++folderRequest;
    const isCurrent = () => !disposed && request === folderRequest;
    const client = getLycaonClient();
    if (!client) {
      options.reportError(
        new Error("Can't open a folder while the app is reconnecting."),
      );
      return;
    }
    // A newer folder pick supersedes this request.
    pendingClonedProjectId = null;
    setOpenFolderPath(null);
    setOpenFolderDetect(null);

    let detect: FolderDetect;
    try {
      detect = await client.detectFolder(path);
    } catch {
      if (!isCurrent()) return;
      try {
        await openFolderAsProject(path, null);
      } catch {
        // The callee reports this failure.
      }
      return;
    }
    if (!isCurrent()) return;
    const effectiveDetect: FolderDetect = forceNew
      ? {
          path: detect.path,
          ...(detect.extension_suggestions
            ? { extension_suggestions: detect.extension_suggestions }
            : {}),
        }
      : detect;
    setOpenFolderDetect(effectiveDetect);
    if (
      detectedFolderProjects(effectiveDetect).length > 0 ||
      hasInstallableExtensionSuggestions(effectiveDetect.extension_suggestions)
    ) {
      setOpenFolderPath(path);
      return;
    }
    try {
      await openFolderAsProject(path, null);
    } catch {
      // The callee reports this failure.
    } finally {
      if (isCurrent()) setOpenFolderDetect(null);
    }
  };

  const confirmOpenFolder = async (choice: OpenFolderChoice) => {
    const path = openFolderDetect()?.path ?? openFolderPath();
    if (!path || openFolderDetect() == null) return;
    const clonedId = pendingClonedProjectId;
    const effectiveChoice: OpenFolderChoice =
      clonedId != null
        ? { ...choice, projectId: clonedId }
        : choice;
    setOpenFolderBusy(true);
    try {
      await openFolderAsProject(path, effectiveChoice, () => {
        pendingClonedProjectId = null;
        setOpenFolderPath(null);
        setOpenFolderDetect(null);
      });
    } catch {
      // The inner operation reported the error.
    } finally {
      setOpenFolderBusy(false);
    }
  };

  const closeOpenFolder = () => {
    if (openFolderBusy()) return;
    folderRequest += 1;
    const clonedId = pendingClonedProjectId;
    pendingClonedProjectId = null;
    setOpenFolderPath(null);
    setOpenFolderDetect(null);
    // Closing skips installs for an already cloned project.
    if (clonedId && options.projects.byId(clonedId)) {
      options.showProjects();
      void options.startSession(
        { kind: "new-session", projectId: clonedId },
        { switchKind: "cross-project" },
      );
      return;
    }
    const opening = options.shell.state.opening;
    if (opening) options.abandonWorkspaceOpening(opening.token);
  };

  const submitClone = async (args: CloneRepoSubmit) => {
    if (disposed || cloneBusy()) return;
    const request = ++folderRequest;
    const client = getLycaonClient();
    if (!client) return;
    setCloneBusy(true);
    setCloneError(null);
    // Keep clone progress visible while covering the outgoing workspace.
    const token = options.beginWorkspaceOpening(repoWorkspaceLabel(args.url));
    const current = () => !disposed && request === folderRequest && options.shell.state.opening?.token === token;
    try {
      let project = await client.cloneProject({
        url: args.url,
        parent_dir: args.parentDir,
      });
      if (!current()) {
        setCloneOpen(false);
        return;
      }
      options.projects.upsert(project);
      project = options.projects.byId(project.id) ?? project;
      setCloneOpen(false);
      const root =
        project.roots?.find((r) => r.is_primary)?.path ??
        project.roots?.[0]?.path;
      if (root) {
        setOpenFolderDetect(null);
        try {
          const detect = await client.detectFolder(root);
          if (!current()) return;
          if (hasInstallableExtensionSuggestions(detect.extension_suggestions)) {
            // The setup sheet completes this project open.
            pendingClonedProjectId = project.id;
            setOpenFolderDetect({
              path: detect.path,
              ...(detect.extension_suggestions
                ? { extension_suggestions: detect.extension_suggestions }
                : {}),
            });
            setOpenFolderPath(root);
            return;
          }
        } catch {
          // Identity failed; open the cloned project without offering installs.
        }
        if (!current()) return;
        pendingClonedProjectId = null;
        setOpenFolderPath(null);
        setOpenFolderDetect(null);
      }
      options.showProjects();
      await options.startSession(
        { kind: "new-session", projectId: project.id },
        { switchKind: "cross-project" },
      );
    } catch (err) {
      if (!current()) return;
      options.abandonWorkspaceOpening(token);
      setCloneError(options.formatError(err));
    } finally {
      setCloneBusy(false);
    }
  };

  const openCloneDialog = () => {
    setCloneError(null);
    setCloneOpen(true);
  };

  return {
    cloneOpen, cloneBusy, cloneError, setCloneError, setCloneOpen,
    openFolderPath, openFolderDetect, openFolderBusy,
    startOpenFolder, proposeFolderAsProject, confirmOpenFolder, closeOpenFolder, submitClone, openCloneDialog
  };
}
