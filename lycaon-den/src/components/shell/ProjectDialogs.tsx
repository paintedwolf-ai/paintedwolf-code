import { type ShellScope } from "./shell-scope.ts";
import { pickProjectFolder } from "../../platform/files/folder.ts";
import { ProjectRemovalDialog, createProjectRemovalReview } from "./ProjectRemovalDialog.tsx";
import { createProjectLifecycle } from "./project-lifecycle.ts";
import { Show } from "solid-js";
import { type Project } from "../../api/types.ts";
import { projectScope } from "../../notices/notice-scope.ts";
import { getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { CloneRepoDialog } from "../home/CloneRepoDialog.tsx";
import { MoveToProjectDialog } from "../home/MoveToProjectDialog.tsx";
import { OpenFolderConfirm } from "../home/OpenFolderConfirm.tsx";
import { WorkspaceFoldersPanel } from "../WorkspaceFoldersPanel.tsx";
import { createProjectOpening } from "./project-opening.ts";

function projectPromotionPending(
  project: Pick<Project, "promotion"> | null | undefined,
): boolean {
  return project?.promotion != null && project.promotion.phase !== "committed";
}

type Props = Pick<ShellScope, "projects" | "activeProject"> & {
  review: ReturnType<typeof createProjectRemovalReview>;
  lifecycle: Pick<
    ReturnType<typeof createProjectLifecycle>,
    | "moveTarget"
    | "moveBusy"
    | "moveError"
    | "setMoveError"
    | "submitMove"
    | "setMoveTarget"
    | "dialogError"
    | "detachProjectRoot"
  >;
  opening: Pick<
    ReturnType<typeof createProjectOpening>,
    | "cloneOpen"
    | "cloneBusy"
    | "cloneError"
    | "setCloneError"
    | "setCloneOpen"
    | "submitClone"
    | "openFolderPath"
    | "openFolderDetect"
    | "openFolderBusy"
    | "confirmOpenFolder"
    | "closeOpenFolder"
  >;
  foldersPanelProjectId: () => string | null;
  setFoldersPanelProjectId: (id: string | null) => void;
};

export function ProjectDialogs(props: Props) {
  const { activeProject, foldersPanelProjectId, setFoldersPanelProjectId } = props;
  const {
    moveTarget,
    moveBusy,
    moveError,
    setMoveError,
    submitMove,
    setMoveTarget,
    dialogError,
    detachProjectRoot,
  } = props.lifecycle;
  const {
    cloneOpen,
    cloneBusy,
    cloneError,
    setCloneError,
    setCloneOpen,
    submitClone,
    openFolderPath,
    openFolderDetect,
    openFolderBusy,
    confirmOpenFolder,
    closeOpenFolder,
  } = props.opening;
  return <>
      <ProjectRemovalDialog review={props.review} />
      <MoveToProjectDialog
        open={moveTarget() != null}
        busy={moveBusy() || projectPromotionPending(activeProject())}
        error={moveError()}
        onPickFolder={async () => {
          setMoveError(null);
          try {
            return await pickProjectFolder();
          } catch (err) {
            setMoveError(dialogError(err));
            return null;
          }
        }}
        onSubmit={(args) => void submitMove(args)}
        onClose={() => setMoveTarget(null)}
      />
      <CloneRepoDialog
        open={cloneOpen()}
        busy={cloneBusy()}
        error={cloneError()}
        onPickParent={async () => {
          setCloneError(null);
          try {
            return await pickProjectFolder();
          } catch (err) {
            setCloneError(dialogError(err));
            return null;
          }
        }}
        onSubmit={(args) => void submitClone(args)}
        onClose={() => setCloneOpen(false)}
      />
      <OpenFolderConfirm
        open={openFolderPath() != null}
        path={openFolderPath()}
        detect={openFolderDetect()}
        busy={openFolderBusy()}
        onConfirm={(choice) => void confirmOpenFolder(choice)}
        onClose={closeOpenFolder}
      />
      <Show when={foldersPanelProjectId()} keyed>
        {(projectId) => (
            <WorkspaceFoldersPanel
              open
              projectId={projectId}
              roots={
                props.projects
                  .byId(projectId)
                  ?.roots.filter((root) => root.kind === "attached") ?? []
              }
              onClose={() => setFoldersPanelProjectId(null)}
              onAttach={async (path) => {
                const client = getLycaonClient();
                if (!client) return;
                try {
                  const updated = await client.attachProjectRoot(projectId, { path });
                  props.projects.upsert(updated);
                } catch (err) {
                  noticeReporterFor(projectScope(projectId)).reportError(err);
                }
              }}
              onDetach={(rootId) => detachProjectRoot(projectId, rootId)}
              onSetPrimary={async (rootId) => {
                const client = getLycaonClient();
                if (!client) return;
                const updated = await client.updateProjectRoot(projectId, rootId, {
                  is_primary: true,
                });
                props.projects.upsert(updated);
              }}
              onRenameLabel={async (rootId, label) => {
                const client = getLycaonClient();
                if (!client) return;
                const updated = await client.updateProjectRoot(projectId, rootId, {
                  label,
                });
                props.projects.upsert(updated);
              }}
            />
        )}
      </Show>
  </>;
}
