import { createEffect, createSignal } from "solid-js";
import { sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import { type UnsavedChangesIntent } from "../../components/source/editor/UnsavedChangesDialog.tsx";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { closeFilesBuffersUnderPath, dirtyFilesBuffersUnderPath, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { type DirtyGuardResult } from "../commands/project-files-lifecycle.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";


export type ConfirmTarget =
  | { intent: Exclude<UnsavedChangesIntent, "bulk-close">; key: FileBufferKey }
  | { intent: "bulk-close"; fileNames: string[] };

export type BulkCloseState = {
  keys: FileBufferKey[];
  dirtyKeys: FileBufferKey[];
  /** When set, discard/close also clears every buffer under this deleted path. */
  foreignDelete?: { rootId: string; path: string };
} | null;

type DeleteGuard = {
  rootId: string;
  path: string;
  resolve: (result: DirtyGuardResult) => void;
};

type ConfirmationDependencies = Pick<FilesScope, "projectId" | "state" | "closeBuffer" | "reloadBuffer" | "setSaveError"> & {
  closeBuffers: (keys: FileBufferKey[], options?: { discardDraft?: boolean }) => boolean;
  discardBuffer: (key: FileBufferKey) => Promise<void>;
  performSave: (key: FileBufferKey) => Promise<boolean>;
  saveable: (buffer: FileBuffer) => boolean;
};

export function createFileConfirmations({ projectId, state, closeBuffer, closeBuffers, reloadBuffer,
  discardBuffer, performSave, saveable, setSaveError }: ConfirmationDependencies) {
  const [confirm, setConfirm] = createSignal<ConfirmTarget | null>(null);
  const [bulkClose, setBulkClose] = createSignal<BulkCloseState>(null);
  const [deleteGuard, setDeleteGuard] = createSignal<DeleteGuard | null>(null);
  let editorActionDirtyResolve:
    ((result: "proceed" | "cancel") => void) | null = null;
  const ensureCleanDiskForEditorAction = (
    key: FileBufferKey,
  ): Promise<"proceed" | "cancel"> => {
    const buf = state().byKey[key];
    if (!buf?.dirty) return Promise.resolve("proceed");
    return new Promise((resolve) => {
      editorActionDirtyResolve = resolve;
      setConfirm({ intent: "editor-action", key });
    });
  };

  const requestCloseTab = (key: FileBufferKey) => {
    const buf = state().byKey[key];
    if (!buf) return;
    if (buf.dirty) {
      setConfirm({ intent: "close", key });
      return;
    }
    void closeBuffer(key);
  };

  const requestReload = (key: FileBufferKey) => {
    const buf = state().byKey[key];
    if (!buf) return;
    if (buf.dirty) {
      setConfirm({ intent: "reload", key });
      return;
    }
    reloadBuffer(key);
  };

  const requestDiscard = (key: FileBufferKey) => {
    const buf = state().byKey[key];
    if (!buf?.dirty) return;
    setConfirm({ intent: "discard", key });
  };

  const onConfirmDiscard = () => {
    const target = confirm();
    const bulk = bulkClose();
    const duringDelete = deleteGuard() != null;
    setConfirm(null);
    setBulkClose(null);
    if (!target) return;
    // Discarded tabs close before host completion.
    if (target.intent === "bulk-close") {
      if (!bulk || !closeBuffers(bulk.keys, { discardDraft: true })) return;
      if (bulk.foreignDelete) {
        closeFilesBuffersUnderPath(projectId(), bulk.foreignDelete.rootId, bulk.foreignDelete.path);
      }
      return;
    }
    if (target.intent === "close") {
      if (!closeBuffer(target.key, { discardDraft: true })) {
        if (duringDelete) onConfirmCancel();
        return;
      }
      if (duringDelete) finishDeleteGuardStep();
      return;
    }
    void (async () => {
      await discardBuffer(target.key);
      if (target.intent === "editor-action") {
        setSaveError(null);
        const resolve = editorActionDirtyResolve;
        editorActionDirtyResolve = null;
        resolve?.("proceed");
      } else if (target.intent === "discard") {
        setSaveError(null);
        if (duringDelete) finishDeleteGuardStep();
        else queueMicrotask(() => {
          if (state().activeKey === target.key && document.activeElement === document.body) {
            getFilesEditorView(projectId(), target.key)?.focus();
          }
        });
      } else if (target.intent === "reload") {
        setSaveError(null);
        reloadBuffer(target.key);
      }
    })().catch((err) => setSaveError(sourceSaveErrorMessage(err)));
  };

  const onConfirmSave = () => {
    const target = confirm();
    if (!target) return;
    if (target.intent === "bulk-close") {
      const bulk = bulkClose();
      if (!bulk) return;
      void (async () => {
        for (const key of bulk.dirtyKeys) {
          setFilesActiveBuffer(projectId(), key);
          const ok = await performSave(key);
          if (!ok) {
            setConfirm(null);
            setBulkClose(null);
            return;
          }
        }
        const keys = bulk.keys;
        const foreign = bulk.foreignDelete;
        setConfirm(null);
        setBulkClose(null);
        if (!closeBuffers(keys)) return;
        if (foreign) {
          closeFilesBuffersUnderPath(
            projectId(),
            foreign.rootId,
            foreign.path,
          );
        }
      })();
      return;
    }
    void performSave(target.key).then(async (ok) => {
      setConfirm(null);
      if (!ok) {
        if (target.intent === "editor-action") {
          const resolve = editorActionDirtyResolve;
          editorActionDirtyResolve = null;
          resolve?.("cancel");
        }
        return;
      }
      if (deleteGuard()) {
        finishDeleteGuardStep();
        return;
      }
      if (target.intent === "close") {
        void closeBuffer(target.key);
      } else if (target.intent === "editor-action") {
        const resolve = editorActionDirtyResolve;
        editorActionDirtyResolve = null;
        resolve?.("proceed");
      }
    });
  };

  const confirmBufferSaveable = () => {
    const target = confirm();
    if (!target) return false;
    if (target.intent === "bulk-close") {
      const bulk = bulkClose();
      if (!bulk) return false;
      return bulk.dirtyKeys.some((key) => {
        const buf = state().byKey[key];
        return Boolean(buf && saveable(buf));
      });
    }
    const buf = state().byKey[target.key];
    return Boolean(buf && saveable(buf));
  };

  // Discarding the draft preserves the agent edit in version history.
  const confirmBufferHeldAgentEdit = () => {
    const target = confirm();
    if (!target || target.intent === "bulk-close") return false;
    return state().byKey[target.key]?.heldAgentVersionId != null;
  };

  const advanceDeleteGuard = () => {
    const guard = deleteGuard();
    if (!guard) return;
    const dirty = dirtyFilesBuffersUnderPath(
      projectId(),
      guard.rootId,
      guard.path,
    );
    if (dirty.length === 0) {
      guard.resolve("proceed");
      setDeleteGuard(null);
      return;
    }
    const first = dirty[0];
    if (!first) return;
    setConfirm({ intent: "close", key: first.key });
  };


  const onConfirmCancel = () => {
    const guard = deleteGuard();
    if (guard) {
      guard.resolve("cancel");
      setDeleteGuard(null);
    }
    if (editorActionDirtyResolve) {
      const resolve = editorActionDirtyResolve;
      editorActionDirtyResolve = null;
      resolve("cancel");
    }
    setBulkClose(null);
    setConfirm(null);
  };

  const finishDeleteGuardStep = () => {
    if (deleteGuard()) advanceDeleteGuard();
  };

  const observeDeleteGuard = () => {
  createEffect(() => {
    if (deleteGuard()) advanceDeleteGuard();
  });
  };
  return { confirm, setConfirm, setBulkClose, deleteGuard, setDeleteGuard, ensureCleanDiskForEditorAction,
    requestCloseTab, requestReload, requestDiscard, onConfirmDiscard, onConfirmSave,
    confirmBufferSaveable, confirmBufferHeldAgentEdit, onConfirmCancel, observeDeleteGuard };
}
