import { batch, createSignal } from "solid-js";
import { type MoveSourceState } from "../commands/MoveSourceDialog.tsx";
import { type TrashConfirmState } from "../commands/MoveToTrashDialog.tsx";
import { copySourcePath, deleteSourcePath, nextDuplicatePath, renameSourcePath } from "../commands/project-files-lifecycle.ts";
import { sourceHistoryErrorMessage } from "../history/project-files-history.ts";
import { childTreePath, fileDisplayName } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";

type MutationDependencies = Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "lifecycleHooks"> & {
  directoryEntries: (rootId: string, dir: string) => readonly { name: string }[] | undefined;
  noteLifecycleChange: (label: string) => void;
  reportHistoryFailure: (error: unknown) => void;
};

function sourceParentDir(path: string): string {
  const index = path.lastIndexOf("/");
  return index < 0 ? "." : path.slice(0, index) || ".";
}

export function createFileMutationCommands({ projectId, client, sourceSessionId, lifecycleHooks,
  directoryEntries, noteLifecycleChange, reportHistoryFailure }: MutationDependencies) {
  const [trashConfirm, setTrashConfirm] =
    createSignal<TrashConfirmState | null>(null);
  const [trashRunning, setTrashRunning] = createSignal<Set<string>>(new Set());
  const trashBusy = () => { const selected = trashConfirm(); return selected ? trashRunning().has(selected.operationId) : false; };
  const [moveSource, setMoveSource] = createSignal<MoveSourceState | null>(null);
  const [moveRunning, setMoveRunning] = createSignal<Set<MoveSourceState>>(new Set());
  const moveBusy = () => { const selected = moveSource(); return selected ? moveRunning().has(selected) : false; };
  const [moveError, setMoveError] = createSignal<string | null>(null);
  const moveAttempts = new WeakMap<MoveSourceState, { destination: string; operationId: string }>();
  const siblingPaths = (rootId: string, path: string): Set<string> => {
    const parent = path.includes("/")
      ? path.slice(0, path.lastIndexOf("/"))
      : ".";
    const names = directoryEntries(rootId, parent)?.map((entry) => entry.name) ?? [];
    const occupied = new Set<string>();
    for (const name of names) {
      occupied.add(childTreePath(parent, name));
    }
    return occupied;
  };

  const duplicateEntry = async (
    rootId: string,
    path: string,
    isDir: boolean,
  ) => {
    const c = client();
    if (!c) return;
    const to = nextDuplicatePath(path, siblingPaths(rootId, path));
    try {
      const result = await copySourcePath(
        c,
        projectId(),
        { rootId, from: path, to, isDir },
        lifecycleHooks(),
        sourceSessionId(),
      );
      noteLifecycleChange(`Duplicated ${fileDisplayName(result.path)}`);
    } catch (error) {
      reportHistoryFailure(error);
    }
  };

  const commitRename = async (
    rootId: string,
    from: string,
    to: string,
    isDir: boolean,
  ) => {
    const c = client();
    if (!c) throw new Error("Sidecar is not connected.");
    const result = await renameSourcePath(
      c,
      projectId(),
      { rootId, from, to, isDir },
      lifecycleHooks(),
      sourceSessionId(),
    );
    noteLifecycleChange(
      sourceParentDir(from) === sourceParentDir(result.path)
        ? `Renamed to ${fileDisplayName(result.path)}`
        : `Moved ${fileDisplayName(result.path)}`,
    );
  };

  const onMovePath = async (
    rootId: string,
    from: string,
    toDir: string,
    isDir: boolean,
  ) => {
    const c = client();
    if (!c) throw new Error("Sidecar is not connected.");
    const name = fileDisplayName(from);
    const to = childTreePath(toDir, name);
    if (to === from) return;
    const result = await renameSourcePath(
      c,
      projectId(),
      { rootId, from, to, isDir },
      lifecycleHooks(),
      sourceSessionId(),
    );
    noteLifecycleChange(`Moved ${fileDisplayName(result.path)}`);
  };

  const runMoveSource = async (toDir: string) => {
    const held = moveSource();
    if (!held || moveBusy()) return;
    const c = client();
    if (!c) return;
    const previous = moveAttempts.get(held);
    const retry = previous?.destination === toDir;
    const operationId = retry ? previous.operationId : crypto.randomUUID();
    moveAttempts.set(held, { destination: toDir, operationId });
    setMoveRunning((current) => new Set([...current, held]));
    setMoveError(null);
    try {
      const result = await renameSourcePath(c, projectId(), {
        rootId: held.rootId, from: held.path, to: childTreePath(toDir, fileDisplayName(held.path)),
        isDir: held.isDir, operationId, retry,
      }, lifecycleHooks(), sourceSessionId());
      noteLifecycleChange(`Moved ${fileDisplayName(result.path)}`);
      if (moveSource() === held) setMoveSource(null);
    } catch (error) {
      batch(() => {
        setMoveRunning((current) => new Set([...current].filter((entry) => entry !== held)));
        if (moveSource() === held) setMoveError(sourceHistoryErrorMessage(error));
      });
    } finally {
      setMoveRunning((current) => new Set([...current].filter((entry) => entry !== held)));
    }
  };

  const runTrashDelete = async (st: TrashConfirmState) => {
    const c = client();
    if (!c || trashRunning().has(st.operationId)) return;
    setTrashRunning((current) => new Set([...current, st.operationId]));
    try {
      const result = await deleteSourcePath(
        c,
        projectId(),
        { operationId: st.operationId, rootId: st.rootId, path: st.path, recursive: st.isDir, isDir: st.isDir, retry: Boolean(st.error) },
        lifecycleHooks(),
        st.sessionId,
      );
      if (result.ok) {
        if (trashConfirm() === st) setTrashConfirm(null);
        noteLifecycleChange(`Moved ${st.name} to trash`);
      } else if ("cancelled" in result && result.cancelled) {
        if (trashConfirm() === st) setTrashConfirm(null);
      } else if ("message" in result) {
        batch(() => {
          setTrashRunning((current) => new Set([...current].filter((id) => id !== st.operationId)));
          if (trashConfirm() === st) setTrashConfirm({ ...st, error: result.message, retryable: result.retryable, suggestedAction: result.suggestedAction });
        });
      }
    } catch (error) {
      batch(() => {
        setTrashRunning((current) => new Set([...current].filter((id) => id !== st.operationId)));
        if (trashConfirm() === st) setTrashConfirm({ ...st, error: sourceHistoryErrorMessage(error), retryable: true });
      });
    } finally {
      setTrashRunning((current) => new Set([...current].filter((id) => id !== st.operationId)));
    }
  };

  return { trashConfirm, setTrashConfirm, trashBusy, moveSource, setMoveSource, moveBusy,
    moveError, setMoveError, duplicateEntry, commitRename, onMovePath, runMoveSource, runTrashDelete };
}
