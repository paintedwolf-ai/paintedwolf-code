import { createSignal, createEffect } from "solid-js";
import type { SourceChange } from "../../api/types.ts";
import {
  newEntryPath,
  renameEntryPath,
  sourceCreateErrorMessage,
  type NewEntryKind,
} from "../commands/project-files-create.ts";
import { sourceLifecycleErrorMessage } from "../commands/project-files-lifecycle.ts";
import { filesTreeDirKey } from "./files-tree-keys.ts";
import type { DirState, FilesTreeProps, FilesTreeSelection } from "./files-tree-context.ts";
import type { DisplayRowModel } from "./files-tree-display-model.ts";

function defaultDirState(overrides?: Partial<DirState>): DirState {
  return {
    expanded: false,
    naming: null,
    namingDraft: "",
    createError: null,
    creating: false,
    ...overrides,
  };
}

function dirStateIncludesPatch(
  state: DirState | undefined,
  patch: Partial<DirState>,
): boolean {
  const current = state ?? defaultDirState();
  return (Object.keys(patch) as (keyof DirState)[]).every((field) =>
    Object.is(current[field], patch[field]),
  );
}


type TreeEditingOptions = {
  displayModel: () => DisplayRowModel;
  disclose: (rootId: string, dir: string, open: boolean) => Promise<unknown>;
  createEntry: FilesTreeProps["createEntry"];
  confirmSourceChange: (change: SourceChange) => void;
  openFile: (selection: FilesTreeSelection, intent: "transient" | "permanent") => void;
  renaming: () => FilesTreeProps["renaming"];
  commitRename: () => FilesTreeProps["onCommitRename"];
  cancelRename: () => FilesTreeProps["onCancelRename"];
};

export function createFilesTreeEditing(options: TreeEditingOptions) {
  const { displayModel, disclose, createEntry, confirmSourceChange, openFile, renaming } = options;
  const callbacks = {
    get onCommitRename() { return options.commitRename(); },
    get onCancelRename() { return options.cancelRename(); },
  };
  const [dirStates, setDirStates] = createSignal<Record<string, DirState>>({});

  const [renameError, setRenameError] = createSignal<string | null>(null);
  const [renamingBusy, setRenamingBusy] = createSignal(false);

  const patchDir = (rootId: string, dir: string, patch: Partial<DirState>) => {
    const key = filesTreeDirKey(rootId, dir);
    setDirStates(previous => {
      if (dirStateIncludesPatch(previous[key], patch)) return previous;
      const value = { ...defaultDirState(), ...previous[key], ...patch };
      const next = { ...previous };
      if (value.naming || value.creating || value.createError) next[key] = value;
      else delete next[key];
      return next;
    });
  };
  const dirState = (rootId: string, dir: string): DirState => {
    const model = displayModel();
    const row = model.rowAt(model.indexOfEntry(rootId, dir, true));
    return { ...defaultDirState(), ...dirStates()[filesTreeDirKey(rootId, dir)],
      expanded: row?.kind === "entry" && row.entry.expanded === true };
  };
  const startNaming = async (
    rootId: string,
    dir: string,
    kind: NewEntryKind,
  ) => {
    patchDir(rootId, dir, {
      naming: kind,
      namingDraft: "",
      createError: null,
    });
    await disclose(rootId, dir, true);
  };

  const cancelNaming = (rootId: string, dir: string) => {
    patchDir(rootId, dir, { naming: null, namingDraft: "", createError: null });
  };

  const submitName = async (
    rootId: string,
    rootLabel: string,
    dir: string,
    typed: string,
  ) => {
    const st = dirState(rootId, dir);
    const kind = st.naming;
    if (!kind || st.creating) return;
    const resolved = newEntryPath(dir, typed, kind);
    if ("error" in resolved) {
      patchDir(rootId, dir, {
        createError: resolved.error,
        namingDraft: typed,
      });
      return;
    }
    patchDir(rootId, dir, {
      creating: true,
      createError: null,
      namingDraft: typed,
    });
    try {
      const created = await createEntry(kind, rootId, resolved.path);
      patchDir(rootId, dir, { naming: null, namingDraft: "", creating: false });
      confirmSourceChange({
        root_id: rootId,
        path: created,
        op: "create",
        origin: "user",
        is_dir: kind === "folder",
        changed_at: new Date().toISOString(),
      });
      if (kind === "file") {
        openFile({ rootId, rootLabel, path: created }, "permanent");
      }
    } catch (err) {
      patchDir(rootId, dir, {
        creating: false,
        createError: sourceCreateErrorMessage(err),
      });
    }
  };

  const submitRename = async (
    rootId: string,
    fromPath: string,
    typed: string,
    isDir: boolean,
  ) => {
    if (renamingBusy() || !callbacks.onCommitRename) return false;
    const resolved = renameEntryPath(fromPath, typed);
    if ("unchanged" in resolved) {
      callbacks.onCancelRename?.();
      setRenameError(null);
      return false;
    }
    if ("error" in resolved) {
      setRenameError(resolved.error);
      return false;
    }
    setRenamingBusy(true);
    setRenameError(null);
    try {
      await callbacks.onCommitRename(rootId, fromPath, resolved.path, isDir);
      callbacks.onCancelRename?.();
      return true;
    } catch (err) {
      setRenameError(sourceLifecycleErrorMessage(err));
      return false;
    } finally {
      setRenamingBusy(false);
    }
  };

  const cancelRename = () => {
    callbacks.onCancelRename?.();
    setRenameError(null);
  };

  const observeRename = () => {
  createEffect(() => {
    if (!renaming()) setRenameError(null);
  });
  };

  return {
    dirStates,
    setDirStates,
    renameError,
    renamingBusy,
    patchDir,
    dirState,
    startNaming,
    cancelNaming,
    submitName,
    submitRename,
    cancelRename,
    observeRename,
  };
}
