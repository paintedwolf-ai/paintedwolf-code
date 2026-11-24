import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
  on,
  onCleanup,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { GitRepoEntry, GitWorktreeEntry, GitWorktreesView } from "../../api/types.ts";
import { refreshGitStatus } from "../../chat/actions/git-status-reads.ts";
import { subscribeSourceInvalidation } from "../../files/source/source-invalidation.ts";
import { SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS } from "../../files/source/source-refresh.ts";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { resolveScope } from "../git-repo-scope.ts";
import { GitWorktreeSelector } from "./GitWorktreeSelector.tsx";
import {
  clearRepoWorktreePin,
  formatCheckoutBranchLabel,
  pinRepoWorktreeSelection,
  readCheckoutInspectionState,
  reconcileRepoWorktreeSelection,
} from "./git-worktree-inspection.ts";

export type FilesCheckoutPickerProps = {
  projectId: string;
  client: LycaonClient | null;
  appStore: AppStore;
};

export function FilesCheckoutPicker(props: FilesCheckoutPickerProps) {
  const id = createUniqueId();
  const [open, setOpen] = createSignal(false);
  const [viewsByRepo, setViewsByRepo] = createSignal<Record<string, GitWorktreesView>>({});
  const [localRepos, setLocalRepos] = createSignal<GitRepoEntry[]>([]);
  const [selectedRepoId, setSelectedRepoId] = createSignal("");
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal<string>();
  let triggerRef: HTMLButtonElement | undefined;
  let surfaceRef: HTMLDivElement | undefined;
  let fetchGeneration = 0;

  createAnchoredPopoverFocus(open, () => surfaceRef, {
    trigger: () => triggerRef,
    onEscape: () => setOpen(false),
  });

  const sessionId = createMemo(() => {
    const sess = props.appStore.state.currentSession;
    return sess?.project_id?.trim() === props.projectId.trim()
      ? sess.id?.trim() || undefined
      : undefined;
  });

  const repos = createMemo(() => {
    const storeRepos = props.appStore.state.gitRepos;
    const source = storeRepos && storeRepos.length > 0 ? storeRepos : localRepos();
    return source.filter((r) => r.available && !!r.repo_id?.trim());
  });

  const activeRepoId = createMemo(() => {
    const manual = selectedRepoId().trim();
    const list = repos();
    if (manual && list.some((r) => r.repo_id === manual)) return manual;
    const scoped = resolveScope(
      list,
      props.appStore.state.gitActiveRepoId ?? "",
      props.appStore.state.gitScopePin ?? null,
    );
    if (scoped.kind === "repo") return scoped.repoId;
    return list[0]?.repo_id?.trim() ?? "";
  });

  const loadRepoCheckouts = async () => {
    const c = props.client;
    const pid = props.projectId.trim();
    if (!c || !pid || typeof c.listGitWorktrees !== "function") return;
    const gen = ++fetchGeneration;
    setLoading(true);
    setError(undefined);
    try {
      let availableRepos = repos();
      if (availableRepos.length === 0 && typeof c.listGitRepos === "function") {
        const repoView = await c.listGitRepos(pid, sessionId());
        if (gen !== fetchGeneration) return;
        setLocalRepos(repoView.repos);
        availableRepos = repoView.repos.filter((r) => r.available && !!r.repo_id?.trim());
      }
      const nextViews: Record<string, GitWorktreesView> = {};
      for (const repo of availableRepos) {
        const rid = repo.repo_id?.trim();
        if (!rid) continue;
        const view = await c.listGitWorktrees(pid, rid, sessionId());
        if (gen !== fetchGeneration) return;
        nextViews[rid] = view;
        reconcileRepoWorktreeSelection(props.appStore, pid, rid, view);
      }
      setViewsByRepo(nextViews);
    } catch (err) {
      if (gen === fetchGeneration) {
        setError(err instanceof Error ? err.message : "Could not load checkouts.");
      }
    } finally {
      if (gen === fetchGeneration) setLoading(false);
    }
  };

  createEffect(
    on(
      () => [props.projectId.trim(), sessionId() ?? "", props.client != null] as const,
      ([pid, , hasClient]) => {
        setViewsByRepo({});
        setLocalRepos([]);
        setSelectedRepoId("");
        if (pid && hasClient) void loadRepoCheckouts();
      },
    ),
  );

  createEffect(
    on(open, (isOpen) => {
      if (isOpen) void loadRepoCheckouts();
    }),
  );

  const invalidationRefresh = createBoundedDebouncedAsyncScheduler(
    async () => {
      await loadRepoCheckouts();
    },
    SOURCE_REFRESH_IDLE_MS,
    SOURCE_REFRESH_MAX_WAIT_MS,
  );
  onCleanup(invalidationRefresh.cancel);
  onCleanup(
    subscribeSourceInvalidation((scope) => {
      if (scope.projectId === props.projectId.trim()) invalidationRefresh.schedule();
    }),
  );

  const currentView = () => viewsByRepo()[activeRepoId()];
  const inspection = createMemo(() => {
    const rid = activeRepoId();
    const pid = props.projectId.trim();
    if (!pid || !rid) return { pinned: false, unavailable: false };
    const view = viewsByRepo()[rid];
    const base = readCheckoutInspectionState(props.appStore, pid, rid);
    if (!view) return base;
    const match = base.inspectedWorktreeId
      ? view.worktrees.find((w) => w.worktree_id === base.inspectedWorktreeId)
      : undefined;
    const unavailable = base.pinned && (!match || !match.available);
    return { ...base, unavailable };
  });

  const showPicker = createMemo(() => {
    const rid = activeRepoId();
    if (!rid) return false;
    const allViews = Object.values(viewsByRepo());
    if (allViews.some((v) => v.worktrees.length > 1)) return true;
    const state = inspection();
    return state.pinned || state.unavailable;
  });

  const buttonLabel = createMemo(() => {
    const state = inspection();
    if (state.unavailable) return "Unavailable checkout";
    const view = currentView();
    const match = state.inspectedWorktreeId
      ? view?.worktrees.find((w) => w.worktree_id === state.inspectedWorktreeId)
      : undefined;
    if (match) {
      return match.is_project_checkout ? "Project folder" : formatCheckoutBranchLabel(match);
    }
    if (state.summary) {
      return state.summary.isProjectCheckout
        ? "Project folder"
        : formatCheckoutBranchLabel({
            branch: state.summary.branch,
            detached: state.summary.detached,
            head_short: state.summary.headShort,
          });
    }
    return "Project folder";
  });

  const selectEntry = (entry: GitWorktreeEntry) => {
    const pid = props.projectId.trim();
    const rid = activeRepoId();
    if (!pid || !rid) return;
    pinRepoWorktreeSelection(props.appStore, pid, rid, entry);
    if (props.client) {
      void refreshGitStatus(props.appStore, props.client, pid, sessionId(), {
        repoId: rid,
        worktreeId: entry.worktree_id,
        refreshSet: false,
      }).catch(() => undefined);
    }
    setOpen(false);
  };

  const clearPin = () => {
    const pid = props.projectId.trim();
    const rid = activeRepoId();
    if (!pid || !rid) return;
    const next = clearRepoWorktreePin(props.appStore, pid, rid, currentView());
    if (props.client) {
      void refreshGitStatus(props.appStore, props.client, pid, sessionId(), {
        repoId: rid,
        worktreeId: next.inspectedWorktreeId,
        refreshSet: false,
      }).catch(() => undefined);
    }
  };

  return (
    <Show when={showPicker()}>
      <button
        ref={triggerRef}
        type="button"
        class="den-files-history-button den-files-checkout-trigger"
        data-testid="files-checkout-picker"
        aria-label={`Inspect checkout: ${buttonLabel()}`}
        aria-haspopup="dialog"
        aria-expanded={open()}
        aria-controls={`${id}-popover`}
        onClick={() => setOpen((v) => !v)}
      >
        <span aria-hidden="true">⎇</span>
        <span class="den-files-checkout-trigger__label">{buttonLabel()}</span>
      </button>
      <Show when={open()}>
        <AnchoredSurface
          ref={(el) => {
            surfaceRef = el;
          }}
          id={`${id}-popover`}
          anchor={() => triggerRef}
          role="dialog"
          ariaLabel="Inspect checkout"
          testId="files-checkout-popover"
          minWidth={280}
          onDismiss={() => setOpen(false)}
          onEscape={() => setOpen(false)}
        >
          <Show when={repos().length > 1}>
            <div class="git-worktrees__repos" role="group" aria-label="Repository">
              <For each={repos()}>
                {(repo) => (
                  <button
                    type="button"
                    class="git-strip__scope-unpin"
                    data-testid="files-checkout-repo"
                    aria-pressed={repo.repo_id === activeRepoId()}
                    onClick={() => setSelectedRepoId(repo.repo_id ?? "")}
                  >
                    {repo.label}
                  </button>
                )}
              </For>
            </div>
          </Show>
          <GitWorktreeSelector
            worktrees={currentView()?.worktrees ?? []}
            inspectedWorktreeId={inspection().inspectedWorktreeId}
            pinned={inspection().pinned}
            unavailable={inspection().unavailable}
            preservedSummary={inspection().summary}
            loading={loading()}
            error={error()}
            onSelectWorktree={selectEntry}
            onClearPin={clearPin}
            onRetry={() => void loadRepoCheckouts()}
          />
        </AnchoredSurface>
      </Show>
    </Show>
  );
}
