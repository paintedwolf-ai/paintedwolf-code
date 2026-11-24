import { createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import type { GitWorktreeEntry, GitWorktreesView } from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  clearRepoWorktreePin,
  computeCheckoutSelection,
  formatCheckoutBranchLabel,
  groupWorktreeEntries,
  pinRepoWorktreeSelection,
  readCheckoutInspectionState,
  reconcileRepoWorktreeSelection,
  type CheckoutPinSummary,
  type ReconciledCheckoutSelection,
  type WorktreeRowView,
} from "./git-worktree-inspection.ts";

export type UseGitCheckoutWorktreesOptions = {
  appStore?: AppStore;
  projectId: () => string | undefined;
  sessionId: () => string | undefined;
  repoId: () => string;
  status: () => GitWorkspaceStatus | undefined;
  scopedWorktree: () => boolean;
  boundBranch: () => string | undefined;
  menuOpen: () => boolean;
  onListWorktrees?: (repoId: string) => Promise<GitWorktreesView | undefined>;
  onSelectWorktree?: (repoId: string, entry: GitWorktreeEntry) => void | Promise<void>;
  onClearWorktreePin?: (repoId: string, nextWorktreeId?: string) => void | Promise<void>;
  onInspectionUnavailableChange?: (unavailable: boolean) => void;
  onInspectedTargetChange?: (
    target: { branchLabel: string; shortDir: string; isProjectCheckout: boolean } | undefined,
  ) => void;
};

export function useGitCheckoutWorktrees(options: UseGitCheckoutWorktreesOptions) {
  const [view, setView] = createSignal<GitWorktreesView>();
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal<string>();
  const [localPin, setLocalPin] = createSignal<CheckoutPinSummary>();
  let version = 0;
  let lastFollowedTarget = "";

  const enabled = () => Boolean(options.onListWorktrees) && Boolean(options.repoId().trim());

  const applyReconciliation = (
    pid: string,
    rid: string,
    nextView: GitWorktreesView,
  ): ReconciledCheckoutSelection => {
    if (options.appStore && pid) {
      return reconcileRepoWorktreeSelection(options.appStore, pid, rid, nextView);
    }
    const computed = computeCheckoutSelection(
      nextView,
      localPin(),
      options.status()?.worktree_id?.trim(),
    );
    if (computed.pinned && !computed.unavailable && computed.summary) {
      setLocalPin(computed.summary);
    }
    return computed;
  };

  const refreshList = async () => {
    const listFn = options.onListWorktrees;
    const rid = options.repoId().trim();
    const pid = options.projectId()?.trim() ?? "";
    if (!listFn || !rid) return;
    const reqVersion = ++version;
    setLoading(true);
    setError(undefined);
    try {
      const loaded = await listFn(rid);
      if (reqVersion !== version || !loaded) return;
      setView(loaded);
      const reconciled = applyReconciliation(pid, rid, loaded);
      const statusWt = options.status()?.worktree_id?.trim();
      const nextWt = reconciled.inspectedWorktreeId?.trim();
      if (
        !reconciled.pinned &&
        !reconciled.unavailable &&
        nextWt &&
        statusWt &&
        nextWt !== statusWt &&
        lastFollowedTarget !== `${rid}\0${nextWt}`
      ) {
        lastFollowedTarget = `${rid}\0${nextWt}`;
        const match = loaded.worktrees.find((w) => w.worktree_id === nextWt);
        if (match) void options.onClearWorktreePin?.(rid, match.worktree_id);
      }
    } catch (err) {
      if (reqVersion === version) {
        setError(err instanceof Error ? err.message : "Could not load checkouts.");
      }
    } finally {
      if (reqVersion === version) setLoading(false);
    }
  };

  const contextKey = createMemo(() =>
    [options.projectId()?.trim() ?? "", options.sessionId()?.trim() ?? "", options.repoId().trim()].join("\0"),
  );

  createEffect(
    on(contextKey, () => {
      version++;
      lastFollowedTarget = "";
      setView(undefined);
      setError(undefined);
      setLoading(false);
      if (!options.appStore) setLocalPin(undefined);
      if (enabled()) void refreshList();
    }),
  );

  createEffect(
    on(
      () => options.status(),
      () => {
        if (enabled()) void refreshList();
      },
      { defer: true },
    ),
  );

  createEffect(
    on(
      () => options.menuOpen(),
      (open) => {
        if (open && enabled()) void refreshList();
      },
      { defer: true },
    ),
  );

  onCleanup(() => {
    version++;
  });

  const selection = createMemo((): ReconciledCheckoutSelection => {
    const pid = options.projectId()?.trim() ?? "";
    const rid = options.repoId().trim();
    const currentView = view();
    if (options.appStore && pid && rid) {
      const base = readCheckoutInspectionState(options.appStore, pid, rid);
      if (!currentView) {
        return {
          ...base,
          inspectedWorktreeId: base.inspectedWorktreeId ?? options.status()?.worktree_id,
        };
      }
      const match = base.inspectedWorktreeId
        ? currentView.worktrees.find((w) => w.worktree_id === base.inspectedWorktreeId)
        : undefined;
      const unavailable = base.pinned && (!match || !match.available);
      const fallbackId = currentView.bound_worktree_id
        || currentView.project_checkout_id
        || currentView.worktrees[0]?.worktree_id;
      return {
        ...base,
        inspectedWorktreeId: base.inspectedWorktreeId ?? options.status()?.worktree_id ?? fallbackId,
        unavailable,
      };
    }
    const pin = localPin();
    if (currentView) {
      return computeCheckoutSelection(
        currentView,
        pin,
        options.status()?.worktree_id?.trim(),
      );
    }
    if (pin?.pinned) {
      return { inspectedWorktreeId: pin.worktreeId, pinned: true, unavailable: false, summary: pin };
    }
    return {
      inspectedWorktreeId: options.status()?.worktree_id,
      pinned: false,
      unavailable: false,
    };
  });

  const worktrees = () => view()?.worktrees ?? [];
  const rowsByGroup = createMemo(() =>
    groupWorktreeEntries(worktrees(), selection().inspectedWorktreeId),
  );
  const allRows = createMemo((): WorktreeRowView[] => {
    const g = rowsByGroup();
    return [...g.project, ...g.chat, ...g.other];
  });
  const inspectedRow = createMemo(() => {
    const id = selection().inspectedWorktreeId;
    return id ? allRows().find((r) => r.entry.worktree_id === id) : undefined;
  });

  const selectWorktree = (entry: GitWorktreeEntry) => {
    const pid = options.projectId()?.trim() ?? "";
    const rid = options.repoId().trim();
    if (!rid) return;
    if (options.appStore && pid) {
      pinRepoWorktreeSelection(options.appStore, pid, rid, entry);
    } else {
      setLocalPin({
        worktreeId: entry.worktree_id,
        pinned: true,
        branch: entry.branch,
        headShort: entry.head_short,
        detached: entry.detached,
        path: entry.path,
        isProjectCheckout: entry.is_project_checkout,
      });
    }
    void options.onSelectWorktree?.(rid, entry);
  };

  const clearPin = () => {
    const pid = options.projectId()?.trim() ?? "";
    const rid = options.repoId().trim();
    if (!rid) return;
    let nextId: string | undefined;
    if (options.appStore && pid) {
      nextId = clearRepoWorktreePin(options.appStore, pid, rid, view()).inspectedWorktreeId;
    } else {
      setLocalPin(undefined);
      const currentView = view();
      nextId = currentView ? applyReconciliation(pid, rid, currentView).inspectedWorktreeId : undefined;
    }
    void options.onClearWorktreePin?.(rid, nextId);
  };

  const unavailableBranchLabel = createMemo(() => {
    const summary = selection().summary;
    if (!summary) return "Unavailable checkout";
    return formatCheckoutBranchLabel({
      branch: summary.branch,
      detached: summary.detached,
      head_short: summary.headShort,
    });
  });

  const modeInfo = createMemo(() => {
    if (selection().unavailable) {
      return { label: "Unavailable checkout", testId: "git-checkout-mode-unavailable" };
    }
    const row = inspectedRow();
    if (row) {
      if (row.entry.is_project_checkout) {
        return { label: "Project folder", testId: "git-checkout-mode-project" };
      }
      if (row.boundToChat) {
        return { label: "Chat worktree", testId: "git-checkout-mode-worktree" };
      }
      return { label: "Linked worktree", testId: "git-checkout-mode-linked" };
    }
    return options.scopedWorktree()
      ? { label: "Chat worktree", testId: "git-checkout-mode-worktree" }
      : { label: "Project folder", testId: "git-checkout-mode-project" };
  });

  const displayBranch = createMemo(() => {
    if (selection().unavailable) return unavailableBranchLabel();
    const row = inspectedRow();
    if (row) return row.branchLabel;
    const raw = options.scopedWorktree() ? options.boundBranch() : options.status()?.branch;
    if (raw) return raw;
    const headShort = options.status()?.head_short?.trim();
    if (options.status()?.detached || headShort) {
      return headShort ? `Detached HEAD (${headShort})` : "Detached HEAD";
    }
    return "No branch";
  });

  const directoryLabel = createMemo(() => {
    if (selection().unavailable) return selection().summary?.path;
    return inspectedRow()?.shortDir;
  });

  createEffect(() => {
    options.onInspectionUnavailableChange?.(selection().unavailable);
  });

  createEffect(() => {
    if (selection().unavailable) {
      const summary = selection().summary;
      options.onInspectedTargetChange?.(
        summary
          ? {
              branchLabel: unavailableBranchLabel(),
              shortDir: summary.path ?? "unavailable",
              isProjectCheckout: summary.isProjectCheckout === true,
            }
          : undefined,
      );
      return;
    }
    const row = inspectedRow();
    options.onInspectedTargetChange?.(
      row
        ? {
            branchLabel: row.branchLabel,
            shortDir: row.shortDir,
            isProjectCheckout: row.entry.is_project_checkout,
          }
        : undefined,
    );
  });

  return {
    enabled,
    worktrees,
    loading,
    error,
    selection,
    inspectedRow,
    unavailableBranchLabel,
    modeInfo,
    displayBranch,
    directoryLabel,
    refreshList,
    selectWorktree,
    clearPin,
  };
}
