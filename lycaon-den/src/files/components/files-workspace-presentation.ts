import { observeFilesBufferDemand } from "../documents/files-buffer-demand.ts";
import { scopeChangeKindFor } from "../tree/scope-resolution.ts";
import type { ProjectFilesViewProps } from "./project-files-view-props.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { createFilesResidency } from "../documents/files-residency.ts";
import { hasFilesTreeAddress } from "../tree/files-tree-reveal.ts";
import { createMemo, createSignal } from "solid-js";
import { basenameOfPath } from "../../api/project-path.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { useSurfaceReveal } from "../../ui/surface-reveal.ts";
import { filesPaneKey, parseFilesPaneKey } from "./files-pane-key.ts";
import { selectedFileVersion } from "../history/files-version-selection.ts";
import { createFilesWorkspace } from "../source/files-workspace.ts";
import type { FilesScope } from "./files-scope.ts";

type WorkspacePresentation = Pick<ReturnType<typeof createFilesWorkspace>,
  "workspaceFault" | "workspaceError" | "workspaceSettled" | "treeSettled" | "filesWorkspaceId"> &
  Pick<FilesScope, "state" | "activeBuffer" | "aimedKey"> & {
    projectId: string;
    projectName: () => string | undefined;
    document: () => ProjectFilesViewProps["document"];
  };

export function createFilesWorkspacePresentation(options: WorkspacePresentation) {
  const { projectId, projectName, document, state, activeBuffer, aimedKey,
    workspaceFault, workspaceError, workspaceSettled, treeSettled, filesWorkspaceId } = options;
  const holdLabel = () => {
    const target = document();
    return target ? basenameOfPath(target.path) : projectName();
  };
  const holdNotice = (): { message: string; tone: "waiting" | "error" } | null => {
    const fault = workspaceFault();
    if (fault) return { message: fault, tone: "error" };
    const retrying = workspaceError();
    if (retrying) return { message: retrying, tone: "waiting" };
    return document() ? { message: "Opening file…", tone: "waiting" } : null;
  };
  observeSurfaceFailure(
    {
      code: "files_workspace_syncing",
      title: "Editor sync is unavailable",
      suggestedAction: "Files keep retrying on their own; if it persists, check the connection to the host.",
    },
    workspaceError,
    () => projectId,
  );
  const holdHint = () => holdNotice()?.message;
  const holdTone = () => holdNotice()?.tone;
  const workspacePresentationSettled = () =>
    workspaceSettled() && treeSettled();
  // The tree reads its workspace from input handlers too, outside any reactive owner.
  const treeWorkspaceId = createMemo(() => workspaceSettled() ? filesWorkspaceId() : "");
  const stageSettled = () => {
    const buffer = activeBuffer();
    // A buffer showing a past version has content without a source read.
    return workspacePresentationSettled() && (
      !buffer ||
      !buffer.loading ||
      selectedFileVersion(projectId, buffer.key) != null
    );
  };
  const { ready: stageReady } = useSurfaceReveal({
    ready: stageSettled,
    name: "files-stage",
    faulted: () => workspaceFault() != null,
    notice: holdNotice,
  });
  const retainedWorkspacePending = () =>
    stageReady() && !workspacePresentationSettled() && workspaceFault() == null;
  const showRetainedWorkspaceWait = createPresentationWaiting(
    retainedWorkspacePending,
  );
  const [displayedPane, setDisplayedPane] = createSignal<string | null>(null);
  const [publishingPane, setPublishingPane] = createSignal<string | null>(null);
  createFilesResidency(projectId, () => parseFilesPaneKey(displayedPane() ?? "")?.key ?? null);
  const [editorDockMounts, setEditorDockMounts] = createSignal(new Map<string, HTMLElement>());
  const displayedBuffer = () => {
    const pane = displayedPane();
    const parsed = pane ? parseFilesPaneKey(pane) : null;
    return parsed ? state().byKey[parsed.key] ?? null : null;
  };
  const editorPresentationPending = () => {
    const key = aimedKey();
    const buffer = key ? state().byKey[key] : undefined;
    const requested = key && buffer ? filesPaneKey(key, buffer.kind) : null;
    return requested !== displayedPane();
  };
  const showRetainedEditorWait = createPresentationWaiting(
    editorPresentationPending,
  );
  const [mountedTreeWorkspace, setMountedTreeWorkspace] = createSignal("");
  const displayedTreeFile = createMemo<{ rootId: string; path: string; revision: number } | null>(() => {
    const key = aimedKey();
    const buffer = key ? state().byKey[key] : undefined;
    if (buffer && hasFilesTreeAddress(buffer)) {
      return { rootId: buffer.rootId, path: buffer.path, revision: state().aimRevision };
    }
    const pane = displayedPane();
    const parsed = pane ? parseFilesPaneKey(pane) : null;
    const displayed = parsed ? state().byKey[parsed.key] : undefined;
    return displayed && hasFilesTreeAddress(displayed) ? { rootId: displayed.rootId, path: displayed.path, revision: state().aimRevision } : null;
  });
  // Automatic tree motion follows the editor once its destination publishes.
  const publishedTreeFile = () => editorPresentationPending() ? null : displayedTreeFile();
  const activePane = createMemo(() => {
    const key = state().activeKey;
    if (!key) return undefined;
    const buf = state().byKey[key];
    if (!buf) return undefined;
    return filesPaneKey(key, buf.kind);
  });
  return { holdLabel, holdHint, holdTone, treeWorkspaceId, stageReady,
    retainedWorkspacePending, showRetainedWorkspaceWait, displayedPane, setDisplayedPane,
    publishingPane, setPublishingPane, editorDockMounts, setEditorDockMounts,
    displayedBuffer, editorPresentationPending, showRetainedEditorWait,
    mountedTreeWorkspace, setMountedTreeWorkspace, displayedTreeFile,
    publishedTreeFile, activePane };
}

type WorkspaceDemand = Omit<Parameters<typeof observeFilesBufferDemand>[0], "pendingPreparation"> & {
  publishingPane: () => string | null;
  pendingWalkPreparation: () => string[];
  walking: () => boolean;
  walkReady: () => boolean;
};

export function observeFilesWorkspaceDemand(options: WorkspaceDemand): void {
  observeFilesBufferDemand({
    ...options,
    pendingPreparation: (buffer) => [
      ...options.pendingWalkPreparation(),
      ...(options.walking() && !options.walkReady() ? ["walk presentation"] : []),
      ...(buffer?.kind === "text" && buffer.condenseContext && !buffer.loadError && !buffer.deleted &&
        scopeChangeKindFor(options.projectId(), buffer.rootId, buffer.path) !== "deleted"
        ? ["comparison context"] : []),
      ...(buffer && options.publishingPane() === filesPaneKey(buffer.key, buffer.kind)
        ? ["pane publication"] : []),
    ],
  });
}
