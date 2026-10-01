import { IgnoreSecretDialog } from "../../components/source/secrets/IgnoreSecretDialog.tsx";
import { Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot, SourceDirListing } from "../../api/types.ts";
import { ContextMenu, type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";
import { clearScopePreview, clearSymbolEmphasis } from "../../components/source/editor/codemirror-theme.ts";
import { UnsavedChangesDialog } from "../../components/source/editor/UnsavedChangesDialog.tsx";
import { HunkReviewPanel } from "../../components/source/inline-edit/HunkReviewPanel.tsx";
import { cancelInlineEdit } from "../../components/source/inline-edit/inline-edit-controller.ts";
import { InlineEditPanel } from "../../components/source/inline-edit/InlineEditPanel.tsx";
import { submitInlineEditPanel } from "../../components/source/inline-edit/run-editor-action.ts";
import { MarkSecretDialog } from "../../components/source/secrets/MarkSecretDialog.tsx";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createFileConfirmations } from "../commands/file-confirmations.ts";
import type { createFileMutationCommands } from "../commands/file-mutation-commands.ts";
import { MoveSourceDialog } from "../commands/MoveSourceDialog.tsx";
import { MoveToTrashDialog } from "../commands/MoveToTrashDialog.tsx";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import type { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { absolutePathForBuffer } from "./project-files-model.ts";

export function FilesStageDialogs(props: {
  projectId: string;
  appStore: AppStore;
  client: LycaonClient | null;
  roots: ProjectRoot[];
  browse: (rootId: string, dir: string) => Promise<SourceDirListing>;
  editorPane: HTMLElement | null;
  activeBuffer: FileBuffer | null;
  saving: boolean;
  setSaveError: (message: string | null) => void;
  menu: { anchor: ContextMenuAnchor; items: ContextMenuItem[] } | null;
  onCloseMenu: () => void;
  confirmations: ReturnType<typeof createFileConfirmations>;
  mutations: ReturnType<typeof createFileMutationCommands>;
  editorActions: ReturnType<typeof createFilesEditorActions>;
}) {
  const buildContext = () => props.editorActions.buildEditorActionContext();
  return (
    <>
      <InlineEditPanel
        mountEl={props.editorPane}
        docText={() =>
          buildContext()?.view.state.doc.toString() ?? ""
        }
        onSubmit={(text) => {
          const ctx = buildContext();
          if (ctx) void submitInlineEditPanel(ctx, text);
        }}
        onCancelRunning={() => {
          props.editorActions.abortEditorAction();
          cancelInlineEdit();
          const sid = props.appStore.state.currentSession?.id?.trim();
          if (sid) {
            void props.client
              ?.abortSession(sid)
              .catch(() => undefined);
          }
        }}
      />
      <Show when={props.editorActions.hunkReview()} keyed>
        {(target) => (
          <HunkReviewPanel
            client={props.client}
            target={target}
            onClose={() => props.editorActions.setHunkReview(null)}
          />
        )}
      </Show>
      <Show when={props.client} keyed>{(client) => (
        <Show when={props.editorActions.ignoreSecretTarget()} keyed>{(target) => (
          <IgnoreSecretDialog client={client} target={target} onClose={() => props.editorActions.setIgnoreSecretTarget(null)} />
        )}</Show>
      )}</Show>
      <MarkSecretDialog
        target={props.editorActions.markSecretTarget()}
        onClose={() => props.editorActions.setMarkSecretTarget(null)}
        onMarked={() => {
          // The tracked span confirms marking; revocation handles reversal.
          props.setSaveError(null);
        }}
        onError={(message) => props.setSaveError(message)}
      />
      <UnsavedChangesDialog
        intent={props.confirmations.confirm()?.intent ?? null}
        canSave={props.confirmations.confirmBufferSaveable()}
        heldAgentEdit={props.confirmations.confirmBufferHeldAgentEdit()}
        saving={props.saving}
        fileNames={(() => {
          const target = props.confirmations.confirm();
          return target?.intent === "bulk-close" ? target.fileNames : undefined;
        })()}
        onCancel={props.confirmations.onConfirmCancel}
        onDiscard={props.confirmations.onConfirmDiscard}
        onSave={props.confirmations.onConfirmSave}
      />
      <MoveToTrashDialog
        state={props.mutations.trashConfirm()}
        busy={props.mutations.trashBusy()}
        onCancel={() => props.mutations.setTrashConfirm(null)}
        onConfirm={() => {
          const st = props.mutations.trashConfirm();
          if (st) void props.mutations.runTrashDelete(st);
        }}
        openIn={(() => {
          const target = props.mutations.trashConfirm();
          const root = props.roots.find((r) => r.id === target?.rootId);
          return target && root ? { absolutePath: absolutePathForBuffer(root.path, target.path), projectRoots: props.roots.map((r) => r.path), entryKind: target.isDir ? "folder" : "file" } : null;
        })()}
      />
      <MoveSourceDialog
        projectId={props.projectId}
        state={props.mutations.moveSource()}
        roots={props.roots}
        browse={props.browse}
        busy={props.mutations.moveBusy()}
        error={props.mutations.moveError()}
        onCancel={() => {
          props.mutations.setMoveSource(null);
        }}
        onMove={(dir) => void props.mutations.runMoveSource(dir)}
      />
      <Show when={props.menu} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={menu.items}
            onDismiss={() => {
              props.onCloseMenu();
              const buf = props.activeBuffer;
              if (buf && !isComposedBufferKind(buf.kind)) {
                const view = getFilesEditorView(props.projectId, buf.key);
                if (view) {
                  clearScopePreview(view);
                  clearSymbolEmphasis(view);
                }
              }
            }}
          />
        )}
      </Show>
    </>
  );
}
