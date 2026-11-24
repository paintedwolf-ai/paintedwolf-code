import { LycaonApiError } from "../../api/http.ts";
import {
  flushEditorDocumentDraft,
  preserveEditorDocumentOperations,
} from "../../files/documents/editor-document.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { flushFilesDraftSyncFor } from "../../files/documents/files-draft-sync.ts";
import { worktreeIdFromBranchId } from "../../files/source/files-checkout-scope.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { isRecord } from "../../utils/type-guards.ts";
import { selectRepoWorktreeById } from "./git-worktree-inspection.ts";

export type WorktreeBlockerKind =
  | "dirty_worktree"
  | "unsaved_editor"
  | "pending_save"
  | "active_work"
  | "bound_elsewhere"
  | "unmanaged_worktree"
  | "incarnation_mismatch";

export type WorktreeRemovalBlocker = {
  message: string;
  kind?: WorktreeBlockerKind;
  repoId?: string;
  worktreeId?: string;
  rootId?: string;
  path?: string;
  documentId?: string;
  actorSessionId?: string;
};

export type WorktreeBlockerNavigation = {
  label: string;
  run: () => void;
};

function stringField(record: Record<string, unknown>, key: string): string | undefined {
  const val = record[key];
  return typeof val === "string" && val.trim() ? val.trim() : undefined;
}

/** Flushes local editor input and durable outbox state for a checkout before lifecycle removal. */
export async function flushCheckoutBuffersBeforeRemoval(
  projectId: string | undefined,
  worktreeId: string | undefined,
): Promise<void> {
  const pid = projectId?.trim() ?? "";
  const wid = worktreeId?.trim() ?? "";
  if (!pid || !wid) return;
  const state = projectFilesState(pid);
  const docIds: string[] = [];
  for (const key of state.order) {
    const buf = state.byKey[key];
    if (!buf) continue;
    const bufWt = buf.worktreeId?.trim() || worktreeIdFromBranchId(buf.branchId);
    if (bufWt !== wid) continue;
    flushFilesDraftSyncFor(pid, key);
    const updated = projectFilesState(pid).byKey[key];
    if (updated?.documentId) docIds.push(updated.documentId);
  }
  if (docIds.length > 0) {
    await Promise.all(docIds.map((id) => flushEditorDocumentDraft(id)));
  }
  await preserveEditorDocumentOperations();
}

export function parseWorktreeRemovalBlocker(error: unknown): WorktreeRemovalBlocker {
  const fallback = error instanceof Error
    ? error.message
    : "The action failed. Please try again.";
  if (!(error instanceof LycaonApiError) || !isRecord(error.details)) {
    return { message: fallback };
  }
  const rawKind = stringField(error.details, "blocker");
  const kind = (
    rawKind === "dirty_worktree" ||
    rawKind === "unsaved_editor" ||
    rawKind === "pending_save" ||
    rawKind === "active_work" ||
    rawKind === "bound_elsewhere" ||
    rawKind === "unmanaged_worktree" ||
    rawKind === "incarnation_mismatch"
      ? rawKind
      : undefined
  );
  return {
    message: fallback,
    kind,
    repoId: stringField(error.details, "repo_id"),
    worktreeId: stringField(error.details, "worktree_id"),
    rootId: stringField(error.details, "root_id"),
    path: stringField(error.details, "path"),
    documentId: stringField(error.details, "document_id"),
    actorSessionId: stringField(error.details, "actor_session_id"),
  };
}

export function resolveWorktreeBlockerNavigation(args: {
  projectId?: string;
  appStore?: AppStore;
  blocker: WorktreeRemovalBlocker | undefined;
  onSelectWorktreeId?: (repoId: string | undefined, worktreeId: string) => void;
  onDismissDialog: () => void;
}): WorktreeBlockerNavigation | undefined {
  const pid = args.projectId?.trim() ?? "";
  const blocker = args.blocker;
  if (!pid || !blocker?.kind) return undefined;

  if (blocker.rootId && blocker.path && (blocker.kind === "unsaved_editor" || blocker.kind === "pending_save" || blocker.kind === "dirty_worktree")) {
    const rootId = blocker.rootId;
    const path = blocker.path;
    return {
      label: `Open ${path} in Files`,
      run: () => {
        args.onDismissDialog();
        void openSourceLocation({
          intent: "permanent",
          projectId: pid,
          rootId,
          path,
          repoId: blocker.repoId,
          worktreeId: blocker.worktreeId,
          selectCheckout: Boolean(blocker.worktreeId),
        });
      },
    };
  }

  if (blocker.kind === "dirty_worktree" && blocker.worktreeId) {
    const worktreeId = blocker.worktreeId;
    return {
      label: "Inspect worktree changes",
      run: () => {
        args.onDismissDialog();
        if (args.onSelectWorktreeId) {
          args.onSelectWorktreeId(blocker.repoId, worktreeId);
        } else if (args.appStore) {
          selectRepoWorktreeById(args.appStore, pid, blocker.repoId, worktreeId);
        }
      },
    };
  }

  if (blocker.kind === "active_work") {
    return {
      label: "Open in Running",
      run: () => {
        args.onDismissDialog();
        openRunningStage({ projectId: pid });
      },
    };
  }

  return undefined;
}
