import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type {
  SourceChange,
  SourceHistoryAction,
  SourceHistoryMutationResponse,
  SourceHistoryState,
} from "../../api/types.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { closeFilesBuffersUnderPath } from "../documents/project-files-buffers.ts";
import type { DirtyGuardResult } from "../commands/project-files-lifecycle.ts";
import { retargetOpenFilesUnderPath } from "../commands/project-files-retarget.ts";

type HistoryClient = Pick<
  LycaonClient,
  "undoProjectSourceHistory" | "redoProjectSourceHistory"
>;

export type HistoryHooks = {
  guardDirtyUnderPath: (rootId: string, path: string) => Promise<DirtyGuardResult>;
  confirmChange: (change: SourceChange) => void;
};

export async function applyProjectSourceHistory(
  client: HistoryClient,
  projectId: string,
  direction: "undo" | "redo",
  action: SourceHistoryAction,
  hooks: HistoryHooks,
  sessionId?: string,
): Promise<SourceHistoryMutationResponse | null> {
  if (action.removes_path) {
    const currentPath = action.from_path || action.path;
    if (await hooks.guardDirtyUnderPath(action.root_id, currentPath) === "cancel") {
      return null;
    }
  }
  const request = {
    operation_id: crypto.randomUUID(),
    expected_entry_id: action.id,
  };
  const response = direction === "undo"
    ? await client.undoProjectSourceHistory(projectId, request, sessionId)
    : await client.redoProjectSourceHistory(projectId, request, sessionId);
  await applyHistoryResponse(projectId, response, hooks);
  return response;
}

async function applyHistoryResponse(
  projectId: string,
  response: SourceHistoryMutationResponse,
  hooks: Pick<HistoryHooks, "confirmChange">,
): Promise<void> {
  if (response.op === "rename" && response.from_path) {
    retargetOpenFilesUnderPath(
      projectId,
      response.root_id,
      response.from_path,
      response.path,
    );
  } else if (response.op === "delete") {
    closeFilesBuffersUnderPath(projectId, response.root_id, response.path);
  }
  hooks.confirmChange({
    root_id: response.root_id,
    path: response.path,
    from_path: response.from_path,
    op: response.op,
    origin: "user",
    is_dir: response.is_dir,
    changed_at: new Date().toISOString(),
  });
}

export async function retryProjectSourceHistory(
  client: Pick<LycaonClient, "getProjectSourceHistory" | "retrySourceOperation">,
  projectId: string,
  requestId: string,
  direction: "undo" | "redo",
  hooks: HistoryHooks,
  expectedEntryId: string,
): Promise<void> {
  const history = await client.getProjectSourceHistory(projectId);
  const action = history[direction];
  if (!action || action.id !== expectedEntryId) {
    throw new Error("File history changed. Review the current Undo or Redo action.");
  }
  if (action?.removes_path) {
    const currentPath = action.from_path || action.path;
    if (await hooks.guardDirtyUnderPath(action.root_id, currentPath) === "cancel") {
      return;
    }
  }
  const response = await client.retrySourceOperation(projectId, requestId) as SourceHistoryMutationResponse;
  await applyHistoryResponse(projectId, response, hooks);
}

export function sourceHistoryErrorMessage(error: unknown): string {
  if (error instanceof LycaonApiError) {
    if (error.code === "source_history_changed" || error.code === "source_mutation_diverged") {
      return "Files changed on disk. Review the current tree before trying again.";
    }
    if (error.message.trim()) return error.message.trim();
  }
  if (error instanceof Error && error.message.trim()) return error.message.trim();
  return "Could not update file history.";
}

const FILE_HISTORY_ACTION_FAILURE = {
  code: "files_history_action_failed",
  title: "File history action failed",
  suggestedAction: "Check the file operation and try again.",
};

/** The stage's undo and redo of file operations; failures go to the project's notices. */
export function createSourceHistoryCommands({ projectId, client, sourceSessionId, lifecycleHooks }:
  Pick<FilesScope, "projectId" | "client" | "sourceSessionId"> & { lifecycleHooks: () => HistoryHooks }) {
  const [sourceHistory, setSourceHistory] = createSignal<SourceHistoryState>({
    undo: null,
    redo: null,
  });
  const [historyBusy, setHistoryBusy] = createSignal(false);
  const reportHistoryFailure = (error: unknown) =>
    reportSurfaceFailure(FILE_HISTORY_ACTION_FAILURE, sourceHistoryErrorMessage(error), projectId());
  const [historyNotice, setHistoryNotice] = createSignal<{
    label: string;
    action: "undo" | "redo";
  } | null>(null);
  let historyRequest = 0;
  const refreshSourceHistory = async () => {
    const c = client();
    if (!c) {
      setSourceHistory({ undo: null, redo: null });
      return;
    }
    const request = ++historyRequest;
    try {
      const next = await c.getProjectSourceHistory(projectId());
      if (request === historyRequest) setSourceHistory(next);
    } catch (error) {
      if (request === historyRequest) {
        setSourceHistory({ undo: null, redo: null });
        reportHistoryFailure(error);
      }
    }
  };

  const noteLifecycleChange = (label: string) => {
    setHistoryNotice({ label, action: "undo" });
    void refreshSourceHistory();
  };

  const runSourceHistory = async (direction: "undo" | "redo") => {
    const c = client();
    const action = sourceHistory()[direction];
    if (!c || !action || historyBusy()) return;
    setHistoryBusy(true);
    try {
      const result = await applyProjectSourceHistory(
        c,
        projectId(),
        direction,
        action,
        lifecycleHooks(),
        sourceSessionId(),
      );
      if (!result) return;
      setHistoryNotice({
        label: direction === "undo" ? "File change undone" : "File change redone",
        action: direction === "undo" ? "redo" : "undo",
      });
    } catch (error) {
      reportHistoryFailure(error);
    } finally {
      await refreshSourceHistory();
      setHistoryBusy(false);
    }
  };

  return { sourceHistory, historyBusy, historyNotice, setHistoryNotice, reportHistoryFailure,
    refreshSourceHistory, noteLifecycleChange, runSourceHistory };
}
