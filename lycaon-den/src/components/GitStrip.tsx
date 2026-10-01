import { ChromeDragSurface } from "./shell/ChromeDragSurface.tsx";
import { DenButton } from "./primitives/DenButton.tsx";
import { chromeProps } from "../styling/ui-chrome.ts";
import {
  For,
  Show,
  createEffect,
  createSignal,
  onCleanup,
  onMount,
  untrack,
  type Accessor,
} from "solid-js";
import { ResidentPortal } from "./primitives/ResidentPortal.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { subscribeSourceInvalidation } from "../files/source/source-invalidation.ts";
import { SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS } from "../files/source/source-refresh.ts";
import { createBoundedDebouncedAsyncScheduler } from "../store/coalesced-async.ts";
import { createAdaptivePoller } from "../store/adaptive-poller.ts";
import { useResidentLive } from "../ui/resident-activity.ts";
import type {
  GitFileEntry,
  GitRepoEntry,
} from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";
import type { ResolveProjectRoot } from "../api/project-path.ts";
import { GitCheckout, type GitCheckoutActions } from "./GitCheckout.tsx";
import {
  gitReposChangeCount,
  nonRepoRootId,
  resolveScope,
  type GitScope,
} from "./git-repo-scope.ts";
import { SourcePathLink } from "./source/SourcePathLink.tsx";
import { createModalFocusTrap } from "../platform/interaction/modal-focus-trap.ts";
import {
  type LoadState,
  errorOf,
  isLoaded,
  valueOf,
} from "../store/load-state.ts";

type Props = GitCheckoutActions & {
  status: LoadState<GitWorkspaceStatus>;
  repos?: readonly GitRepoEntry[];
  activeRepoId?: string;
  scopePin?: GitScope | null;
  projectId?: string;
  sessionId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  busy?: boolean | Accessor<boolean>;
  /** Refreshes repositories and scoped status. */
  onRefreshRepos?: () => void | Promise<unknown>;
  onScopePin: (pin: GitScope | null) => void;
  /** Explicit scope selection refreshes repository status. */
  onSelectRepo: (repoId: string) => void;
  onInit: (rootId: string) => void;
  /** Reports failures and resolves true only after a successful commit. */
  onCommit: (message: string) => Promise<boolean>;
  onStash: () => void;
  /** Resolves to a commit subject based on the current diff. */
  onDraftMessage: () => Promise<string>;
  /** The strip confirms before discarding all changes. */
  onDiscard: () => void;
  onPush: () => void;
  /** Fast-forward only. */
  onPull: () => void;
  /** Requests grouped commits from the coordinator. */
  onGroupCommit: () => void;
};

function statusLabel(code: string): string {
  const xy = code.trim();
  if (xy === "??") return "new";
  if (xy.startsWith("R")) return "renamed";
  if (xy.startsWith("D") || xy.endsWith("D")) return "deleted";
  if (xy.startsWith("A")) return "added";
  return "modified";
}

function statusFileChange(code: string): "added" | "deleted" | undefined {
  const label = statusLabel(code);
  if (label === "deleted") return "deleted";
  return label === "new" || label === "added" ? "added" : undefined;
}

function entryChangeCount(entry: GitRepoEntry): number {
  return (entry.staged_count ?? 0) + (entry.unstaged_count ?? 0);
}

function repoSummaryPending(repos: readonly GitRepoEntry[]): boolean {
  return repos.some(
    (entry) => entry.available && entry.status_pending === true,
  );
}

function scopeLabel(scope: GitScope, repos: readonly GitRepoEntry[]): string {
  if (scope.kind === "all") return "All repositories";
  if (scope.kind === "repo") {
    return repos.find((r) => r.repo_id === scope.repoId)?.label ?? "Repository";
  }
  return (
    repos.find((r) => !r.available && r.root_ids.includes(scope.rootId))
      ?.label ?? "Root"
  );
}

function rootsForFile(
  file: GitFileEntry,
  rootRefs: readonly ResolveProjectRoot[] | undefined,
): ResolveProjectRoot[] | undefined {
  const rid = file.root_id?.trim();
  if (!rid || !rootRefs?.length) return undefined;
  const match = rootRefs.filter((r) => r.id === rid);
  return match.length ? match : undefined;
}

export function GitStrip(props: Props) {
  const [message, setMessage] = createSignal("");
  const [committing, setCommitting] = createSignal(false);
  const [filesOpen, setFilesOpen] = createSignal(false);
  const [branchesOpen, setBranchesOpen] = createSignal(false);
  const [scopeOpen, setScopeOpen] = createSignal(false);
  const [drafting, setDrafting] = createSignal(false);
  const [confirmDiscard, setConfirmDiscard] = createSignal(false);
  let discardDialogRef: HTMLDivElement | undefined;

  createModalFocusTrap(confirmDiscard, () => discardDialogRef, {
    onEscape: () => setConfirmDiscard(false),
  });
  const busy = () => {
    const b = props.busy;
    return committing() || (typeof b === "function" ? b() : !!b);
  };

  onMount(() => {
    void props.onRefreshRepos?.();
  });

  const [sourceRefreshPending, setSourceRefreshPending] = createSignal(false);
  const sourceRefresh = createBoundedDebouncedAsyncScheduler(async () => {
    setSourceRefreshPending(true);
  }, SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS);
  onCleanup(sourceRefresh.cancel);
  onCleanup(subscribeSourceInvalidation((scope) => {
    if (scope.projectId === props.projectId) sourceRefresh.schedule();
  }));
  createEffect(() => {
    if (!sourceRefreshPending() || busy()) return;
    setSourceRefreshPending(false);
    void untrack(() => props.onRefreshRepos?.());
  });

  const repos = () => props.repos ?? [];
  const live = useResidentLive();
  const pendingStatusRefresh = createAdaptivePoller(async () => {
    await props.onRefreshRepos?.();
  }, 250, 2_000);
  onCleanup(pendingStatusRefresh.cancel);
  createEffect(() => {
    pendingStatusRefresh.setEnabled(live() && !busy() && repoSummaryPending(repos()));
  });
  const showScope = () => repos().length > 1;
  const scope = () =>
    resolveScope(repos(), props.activeRepoId ?? "", props.scopePin ?? null);
  const pinned = () => props.scopePin != null && showScope();

  const status = () => valueOf(props.status);
  // A cached status remains usable during refresh.
  const statusKnown = () => isLoaded(props.status) || status() !== undefined;
  const statusError = () => errorOf(props.status);
  const available = () => status()?.available === true;
  const files = (): GitFileEntry[] => status()?.files ?? [];
  const dirty = () => status()?.dirty === true;
  const upstream = () => (status()?.upstream ?? "").trim().length > 0;
  const ahead = () => status()?.ahead ?? 0;
  const behind = () => status()?.behind ?? 0;
  const canCommit = () => !busy() && dirty() && message().trim().length > 0;

  const closeSiblingLists = () => {
    setBranchesOpen(false);
    setFilesOpen(false);
  };

  const toggleScope = () => {
    const next = !scopeOpen();
    setScopeOpen(next);
    if (next) closeSiblingLists();
  };

  const toggleFiles = () => {
    setFilesOpen((v) => {
      const next = !v;
      if (next) {
        setScopeOpen(false);
        setBranchesOpen(false);
      }
      return next;
    });
  };

  const selectScope = (next: GitScope) => {
    props.onScopePin(next);
    setScopeOpen(false);
    if (next.kind === "repo") {
      props.onSelectRepo(next.repoId);
    }
  };

  const clearPin = () => {
    props.onScopePin(null);
  };

  const draft = async () => {
    if (busy() || drafting()) return;
    setDrafting(true);
    try {
      const suggested = await props.onDraftMessage();
      if (suggested.trim()) setMessage(suggested.trim());
    } finally {
      setDrafting(false);
    }
  };

  let msgRef: HTMLTextAreaElement | undefined;
  const resizeMessage = () => {
    const el = msgRef;
    if (!el) return;
    if (el.value === "") {
      el.style.height = "";
      return;
    }
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  };
  createEffect(() => {
    message();
    resizeMessage();
  });

  const commit = async () => {
    if (!canCommit()) return;
    const submitted = message();
    const projectId = props.projectId;
    const sessionId = props.sessionId;
    const repoId = status()?.repo_id;
    setCommitting(true);
    try {
      const committed = await props.onCommit(submitted.trim());
      if (committed && message() === submitted && props.projectId === projectId &&
          props.sessionId === sessionId && status()?.repo_id === repoId) {
        setMessage("");
      }
    } finally {
      setCommitting(false);
    }
  };

  const initRootId = (): string => {
    const s = scope();
    if (s.kind === "root") return s.rootId;
    const first = repos().find((r) => !r.available);
    if (first) return nonRepoRootId(first);
    return (status()?.root_ids?.[0] ?? "").trim();
  };

  const nonRepoHintLabel = (): string => {
    const s = scope();
    if (s.kind === "root") {
      return (
        repos().find((r) => !r.available && r.root_ids.includes(s.rootId))
          ?.label ?? s.rootId
      );
    }
    return repos().find((r) => !r.available)?.label ?? "this root";
  };

  const initializesOutsidePrimaryRoot = (): boolean => {
    const target = initRootId();
    if (!target) return false;
    const primary = props.rootRefs?.find((root) => root.is_primary)?.id?.trim();
    return Boolean(primary && target !== primary);
  };

  const isScopeCurrent = (entry: GitRepoEntry, s: GitScope): boolean => {
    if (s.kind === "repo") return entry.available && entry.repo_id === s.repoId;
    if (s.kind === "root") {
      return !entry.available && entry.root_ids.includes(s.rootId);
    }
    return false;
  };

  const worktreeRepoId = (): string => {
    const s = scope();
    if (s.kind === "repo") return s.repoId;
    const active = (props.activeRepoId ?? "").trim();
    if (active) return active;
    return (status()?.repo_id ?? "").trim();
  };

  const checkout = (children?: import("solid-js").JSX.Element) => (
    <GitCheckout
      projectId={props.projectId}
      sessionId={props.sessionId}
      repoId={worktreeRepoId()}
      repoLabel={repos().find((r) => r.repo_id === worktreeRepoId())?.label}
      repositoryCount={repos().filter((r) => r.available).length}
      status={status()}
      busy={busy()}
      menuOpen={branchesOpen()}
      onMenuOpenChange={(open) => {
        setBranchesOpen(open);
        if (open) {
          setScopeOpen(false);
          setFilesOpen(false);
        }
      }}
      onSelectWorktreeRepo={(repoId) => selectScope({ kind: "repo", repoId })}
      onListBranches={props.onListBranches}
      onCheckout={props.onCheckout}
      onRefreshWorktree={props.onRefreshWorktree}
      onBindWorktree={props.onBindWorktree}
      onLandWorktree={props.onLandWorktree}
      onUnbindWorktree={props.onUnbindWorktree}
    >
      {children}
    </GitCheckout>
  );

  const scopeRow = () => (
    <Show when={showScope()}>
      <div class="git-strip__row git-strip__row--scope">
        <button
          type="button"
          class="git-strip__scope"
          data-testid="git-scope"
          aria-expanded={scopeOpen()}
          onClick={toggleScope}
        >
          <span class="git-strip__leaf" aria-hidden="true">
            ⌂
          </span>
          <span class="git-strip__scope-label">
            {scopeLabel(scope(), repos())}
          </span>
          <span class="git-strip__caret" aria-hidden="true">
            {scopeOpen() ? "⌃" : "⌄"}
          </span>
        </button>
        <Show when={scope().kind === "all"}>
          <span class="git-strip__scope-total">
            {repoSummaryPending(repos())
              ? "Checking changes…"
              : `${gitReposChangeCount(repos())} changed`}
          </span>
        </Show>
        <span class="git-strip__scope-follow" data-testid="git-scope-follow">
          <Show when={pinned()} fallback={<span>follows chat</span>}>
            <span>pinned</span>
            <button
              type="button"
              class="git-strip__scope-unpin"
              data-tip="Clear pin and follow chat"
              onClick={clearPin}
            >
              Clear
            </button>
          </Show>
        </span>
      </div>
      <Show when={scopeOpen()}>
        <div
          class="git-strip__panel"
          data-testid="git-scope-list"
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.stopPropagation();
              setScopeOpen(false);
            }
          }}
        >
          <Scrollport
            class="git-strip__branch-list"
            contentAs="ul"
            contentClass="git-strip__branch-list-content"
          >
            <li>
              <button
                type="button"
                class="git-strip__branch-item"
                classList={{
                  "git-strip__branch-item--current": scope().kind === "all",
                }}
                data-testid="git-scope-all"
                onClick={() => selectScope({ kind: "all" })}
              >
                <span>All repositories</span>
                <span class="git-strip__scope-meta">
                  {repoSummaryPending(repos())
                    ? "Checking changes…"
                    : `${gitReposChangeCount(repos())} changed`}
                </span>
              </button>
            </li>
            <For each={[...repos()]}>
              {(entry) => (
                <li>
                  <button
                    type="button"
                    class="git-strip__branch-item"
                    classList={{
                      "git-strip__branch-item--current": isScopeCurrent(
                        entry,
                        scope(),
                      ),
                    }}
                    data-testid="git-scope-item"
                    onClick={() => {
                      if (entry.available && entry.repo_id) {
                        selectScope({ kind: "repo", repoId: entry.repo_id });
                      } else {
                        const rid = nonRepoRootId(entry);
                        if (rid) selectScope({ kind: "root", rootId: rid });
                      }
                    }}
                  >
                    <span>{entry.label}</span>
                    <span class="git-strip__scope-meta">
                      <Show when={entry.available} fallback="not a repo">
                        {entry.status_pending
                          ? "Checking changes…"
                          : (entry.branch || "—") +
                            " · " +
                            entryChangeCount(entry)}
                      </Show>
                    </span>
                  </button>
                </li>
              )}
            </For>
          </Scrollport>
        </div>
      </Show>
    </Show>
  );

  const aggregateBody = () => (
    <div class="git-strip__repos" data-testid="git-repos">
      <For each={repos().filter((r) => r.available)}>
        {(entry) => (
          <div class="git-strip__repo-card" data-testid="git-repo-card">
            <div class="git-strip__repo-card-line">
              <span class="git-strip__repo-label">{entry.label}</span>
              <Show when={entry.branch}>
                <span class="git-strip__repo-branch">{entry.branch}</span>
              </Show>
              <span class="git-strip__repo-count">
                {entry.status_pending
                  ? "Checking changes…"
                  : `${entryChangeCount(entry)} changed`}
              </span>
            </div>
            <div class="git-strip__repo-card-line">
              <button
                type="button"
                class="git-strip__repo-open"
                data-testid="git-repo-open"
                onClick={() => {
                  if (entry.repo_id) {
                    selectScope({ kind: "repo", repoId: entry.repo_id });
                  }
                }}
              >
                Open
              </button>
              <Show when={(entry.ahead ?? 0) > 0 || (entry.behind ?? 0) > 0}>
                <span class="git-strip__track">
                  <Show when={(entry.ahead ?? 0) > 0}>
                    <span>↑{entry.ahead}</span>
                  </Show>
                  <Show when={(entry.behind ?? 0) > 0}>
                    <span>↓{entry.behind}</span>
                  </Show>
                </span>
              </Show>
            </div>
          </div>
        )}
      </For>
      <p class="git-strip__repos-footnote">Commit one repository at a time.</p>
    </div>
  );

  const emptyBody = (withHint: boolean) => (
    <div class="git-strip git-strip--empty" data-testid="git-strip">
      {scopeRow()}
      <span class="git-strip__empty">Not a git repository.</span>
      <Show when={withHint && initializesOutsidePrimaryRoot()}>
        <span class="git-strip__empty-hint">
          Initializes in {nonRepoHintLabel()}, not the primary root.
        </span>
      </Show>
      <button
        type="button"
        class="git-strip__init"
        data-testid="git-init"
        disabled={busy() || !initRootId()}
        onClick={() => {
          const rid = initRootId();
          if (rid) props.onInit(rid);
        }}
      >
        Initialize repository
      </button>
    </div>
  );

  const showAggregate = () => showScope() && scope().kind === "all";
  // Repository initialization requires a confirmed non-repository root.
  const showEmpty = () =>
    !showAggregate() &&
    (scope().kind === "root" || (statusKnown() && !available()));
  const showPending = () =>
    !showAggregate() &&
    scope().kind !== "root" &&
    !statusKnown() &&
    !available();
  const showEmptyHint = () =>
    scope().kind === "root" || (showScope() && !available());

  const pendingBody = () => (
    <div class="git-strip git-strip--empty" data-testid="git-strip">
      {scopeRow()}
      <Show when={props.sessionId && props.onRefreshWorktree}>
        {checkout()}
      </Show>
      <span class="git-strip__empty" data-testid="git-status-pending">
        {statusError() !== undefined
          ? "Couldn’t read git status. It will refresh with the next update."
          : "Reading git status…"}
      </span>
    </div>
  );

  return (
    <Show
      when={!showAggregate() && !showEmpty() && !showPending()}
      fallback={
        <Show
          when={showAggregate()}
          fallback={showPending() ? pendingBody() : emptyBody(showEmptyHint())}
        >
          <div class="git-strip" data-testid="git-strip">
            {scopeRow()}
            {aggregateBody()}
          </div>
        </Show>
      }
    >
      <div class="git-strip" data-testid="git-strip">
        {scopeRow()}
        {checkout(
          <>
            <Show when={upstream()}>
              <div class="git-strip__sync" data-testid="git-sync">
                <Show when={ahead() > 0 || behind() > 0}>
                  <span
                    class="git-strip__track"
                    data-tip="Ahead / behind upstream"
                  >
                    <Show when={ahead() > 0}>
                      <span data-testid="git-ahead">↑{ahead()}</span>
                    </Show>
                    <Show when={behind() > 0}>
                      <span data-testid="git-behind">↓{behind()}</span>
                    </Show>
                  </span>
                </Show>
                <button
                  type="button"
                  class="git-strip__sync-btn"
                  data-testid="git-pull"
                  disabled={busy()}
                  data-tip="Fetch and fast-forward from upstream"
                  onClick={() => props.onPull()}
                >
                  Pull
                </button>
                <button
                  type="button"
                  class="git-strip__sync-btn"
                  data-testid="git-push"
                  disabled={busy() || ahead() === 0}
                  data-tip="Push to upstream"
                  onClick={() => props.onPush()}
                >
                  Push
                </button>
              </div>
            </Show>

            <Show
              when={dirty()}
              fallback={
                <span
                  class="git-strip__clean"
                  data-testid="git-clean"
                  role="status"
                >
                  ✓ Repository clean
                </span>
              }
            >
              <button
                type="button"
                class="git-strip__count"
                data-testid="git-count"
                aria-expanded={filesOpen()}
                onClick={toggleFiles}
              >
                <span class="git-strip__count-n">{files().length}</span> changed
              </button>
            </Show>
          </>,
        )}

        <Show when={dirty()}>
          <div class="git-strip__row git-strip__row--compose">
            <div class="git-strip__compose">
              <Scrollport class="git-strip__message-scroll">
                <textarea
                  ref={msgRef}
                  rows={1}
                  class="git-strip__message"
                  data-testid="git-message"
                  placeholder="Commit message… (⏎ commit · ⇧⏎ newline)"
                  value={message()}
                  disabled={busy()}
                  onInput={(e) => setMessage(e.currentTarget.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
                      e.preventDefault();
                      void commit();
                    }
                  }}
                />
              </Scrollport>
              <div class="git-strip__compose-foot">
                <div class="git-strip__ai-group">
                  <button
                    type="button"
                    class="git-strip__ai"
                    data-testid="git-ai"
                    disabled={busy() || drafting()}
                    data-tip="Draft this commit message from the diff"
                    onClick={() => void draft()}
                  >
                    {drafting() ? "…" : "✨ AI summary"}
                  </button>
                  <Show when={scope().kind === "repo" || !showScope()}>
                    <button
                      type="button"
                      class="git-strip__ai"
                      data-testid="git-group-commit"
                      disabled={busy()}
                      data-tip="Let the agent commit all changes in logical groups"
                      onClick={() => props.onGroupCommit()}
                    >
                      ✨ AI auto-commit
                    </button>
                  </Show>
                </div>
                <button
                  type="button"
                  class="git-strip__commit"
                  data-testid="git-commit"
                  disabled={!canCommit()}
                  data-tip="Stage all changes and commit (⏎)"
                  onClick={() => void commit()}
                >
                  Stage &amp; commit
                </button>
              </div>
            </div>

            <div class="git-strip__actions">
              <button
                type="button"
                class="git-strip__stash"
                data-testid="git-stash"
                disabled={busy()}
                data-tip="Stash all changes (including untracked)"
                onClick={() => props.onStash()}
              >
                Stash
              </button>
              <button
                type="button"
                class="git-strip__discard"
                data-testid="git-discard"
                disabled={busy()}
                data-tip="Discard all changes (cannot be undone)"
                onClick={() => setConfirmDiscard(true)}
              >
                Discard
              </button>
            </div>
          </div>
        </Show>

        <Show when={filesOpen() && dirty()}>
          <Scrollport
            class="git-strip__files"
            contentAs="ul"
            contentClass="git-strip__files-content"
            data-testid="git-files"
          >
            <For each={files()}>
              {(f) => {
                const attributed = () => (f.root_id ?? "").trim().length > 0;
                return (
                  <li class="git-strip__file">
                    <span class="git-strip__file-status">
                      {statusLabel(f.status)}
                    </span>
                    <Show
                      when={attributed() && props.projectId?.trim()}
                      fallback={
                        <span
                          class="den-source-path-plain den-source-path--truncate"
                          data-testid="git-file-unlinked"
                          data-file-change={statusFileChange(f.status)}
                        >
                          {f.path}
                        </span>
                      }
                      keyed
                    >
                      {(pid) => (
                        <SourcePathLink
                          truncate
                          projectId={pid}
                          path={f.root_relative_path || f.path}
                          change={statusFileChange(f.status)}
                          rootRefs={rootsForFile(f, props.rootRefs)}
                          chatDestination={
                            props.sessionId?.trim()
                              ? {
                                  projectId: pid,
                                  sessionId: props.sessionId.trim(),
                                }
                              : null
                          }
                        />
                      )}
                    </Show>
                  </li>
                );
              }}
            </For>
          </Scrollport>
        </Show>

        <Show when={confirmDiscard()}>
          <ResidentPortal>
            <div
              class="den-dialog-backdrop"
              data-testid="git-discard-backdrop"
              onClick={(e) => {
                if (e.target === e.currentTarget) setConfirmDiscard(false);
              }}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.stopPropagation();
                  setConfirmDiscard(false);
                }
              }}
            >
              <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
              <div
                ref={discardDialogRef}
                class="den-dialog"
                role="dialog"
                aria-modal="true"
                aria-labelledby="git-discard-title"
                data-testid="git-discard-modal"
              >
                <header class="den-dialog__header" {...chromeProps()}>
                  <h2 id="git-discard-title">Discard all changes?</h2>
                </header>
                <p class="den-dialog__hint">
                  This permanently discards every staged, unstaged, and
                  untracked change in the working tree. It cannot be undone.
                </p>
                <footer class="den-dialog__footer">
                  <DenButton
                    variant="ghost"
                    data-testid="git-discard-cancel"
                    onClick={() => setConfirmDiscard(false)}
                  >
                    Cancel
                  </DenButton>
                  <DenButton
                    variant="danger"
                    data-testid="git-discard-confirm"
                    disabled={busy()}
                    onClick={() => {
                      props.onDiscard();
                      setConfirmDiscard(false);
                    }}
                  >
                    Discard all
                  </DenButton>
                </footer>
              </div>
            </div>
          </ResidentPortal>
        </Show>
      </div>
    </Show>
  );
}
