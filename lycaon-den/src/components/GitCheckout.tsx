import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
  on,
  onCleanup,
  type JSX,
} from "solid-js";
import { Scrollport } from "./primitives/Scrollport.tsx";
import type {
  GitBranchEntry,
  GitWorktreeEntry,
  GitWorktreesView,
  GitWorktreeView,
} from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";
import type { WorktreeLandOutcome } from "../chat/actions/git-actions.ts";
import type { AppStore } from "../store/app-state-model.ts";
import {
  createAnchoredPopoverFocus,
} from "../platform/interaction/modal-focus-trap.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { formatWorktreeLandResult } from "./git-worktree-result.ts";
import { GitCheckoutModal, type GitCheckoutDialogKind } from "./git/GitCheckoutModal.tsx";
import { GitWorktreeSelector } from "./git/GitWorktreeSelector.tsx";
import { useGitCheckoutWorktrees } from "./git/useGitCheckoutWorktrees.ts";
import {
  flushCheckoutBuffersBeforeRemoval,
  parseWorktreeRemovalBlocker,
  resolveWorktreeBlockerNavigation,
  type WorktreeRemovalBlocker,
} from "./git/worktree-lifecycle-blocker.ts";

export type GitCheckoutActions = {
  onListBranches: () => Promise<GitBranchEntry[]>;
  onCheckout: (branch: string, create: boolean) => Promise<void>;
  onRefreshWorktree?: () => Promise<GitWorktreeView | undefined>;
  onBindWorktree?: (
    repoId: string,
    branch?: string,
  ) => Promise<GitWorktreeView>;
  onLandWorktree?: () => Promise<WorktreeLandOutcome>;
  onUnbindWorktree?: () => Promise<void>;
  onListWorktrees?: (repoId: string) => Promise<GitWorktreesView | undefined>;
  onSelectWorktree?: (repoId: string, entry: GitWorktreeEntry) => void | Promise<void>;
  onClearWorktreePin?: (repoId: string, nextWorktreeId?: string) => void | Promise<void>;
  onOpenInFiles?: () => void;
};

type Props = GitCheckoutActions & {
  appStore?: AppStore;
  projectId?: string;
  sessionId?: string;
  repoId: string;
  repoLabel?: string;
  repositoryCount: number;
  status?: GitWorkspaceStatus;
  busy: boolean;
  menuOpen: boolean;
  onMenuOpenChange: (open: boolean) => void;
  onSelectWorktreeRepo: (repoId: string) => void;
  onInspectionUnavailableChange?: (unavailable: boolean) => void;
  onInspectedTargetChange?: (
    target: { branchLabel: string; shortDir: string; isProjectCheckout: boolean } | undefined,
  ) => void;
  children?: JSX.Element;
};

type Dialog = GitCheckoutDialogKind;

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "The action failed. Please try again.";
}

export function GitCheckout(props: Props) {
  const id = createUniqueId();
  const [worktree, setWorktree] = createSignal<GitWorktreeView>();
  const [loading, setLoading] = createSignal(false);
  const [loadError, setLoadError] = createSignal<string>();
  const [branches, setBranches] = createSignal<GitBranchEntry[]>([]);
  const [branchesLoading, setBranchesLoading] = createSignal(false);
  const [branchesError, setBranchesError] = createSignal<string>();
  const [query, setQuery] = createSignal("");
  const [dialog, setDialog] = createSignal<Dialog>();
  const [name, setName] = createSignal("");
  const [actionError, setActionError] = createSignal<string>();
  const [actionBlocker, setActionBlocker] = createSignal<WorktreeRemovalBlocker>();
  const [pending, setPending] = createSignal(false);
  const [result, setResult] =
    createSignal<ReturnType<typeof formatWorktreeLandResult>>();
  let trigger: HTMLButtonElement | undefined;
  let panel: HTMLDivElement | undefined;
  let container: HTMLDivElement | undefined;
  let viewVersion = 0;
  let branchVersion = 0;
  let contextVersion = 0;

  const hasWorktreeSupport = () =>
    !!props.sessionId && !!props.onRefreshWorktree;
  const bound = () => worktree()?.bound === true;
  const scopedWorktree = () => bound() && worktree()?.repo_id === props.repoId;
  const stale = () => scopedWorktree() && worktree()?.state === "stale";
  const busy = () => props.busy || pending();
  const checkoutKnown = () => !hasWorktreeSupport() || worktree() !== undefined;
  const boundBranch = () => worktree()?.branch;
  const inspection = useGitCheckoutWorktrees({
    appStore: props.appStore,
    projectId: () => props.projectId,
    sessionId: () => props.sessionId,
    repoId: () => props.repoId,
    status: () => props.status,
    scopedWorktree,
    boundBranch,
    menuOpen: () => props.menuOpen,
    onListWorktrees: props.onListWorktrees,
    onSelectWorktree: props.onSelectWorktree,
    onClearWorktreePin: props.onClearWorktreePin,
    onInspectionUnavailableChange: props.onInspectionUnavailableChange,
    onInspectedTargetChange: props.onInspectedTargetChange,
  });
  // Inspection stays available during running turns while mutations stay gated by busy().
  const triggerUnavailable = () =>
    (inspection.enabled() ? pending() : busy()) || !checkoutKnown() || !!loadError();
  const inspectedLinked = () => {
    const row = inspection.inspectedRow();
    return Boolean(row && !row.entry.is_project_checkout);
  };
  const inspectingOtherCheckout = () => {
    const wid = worktree()?.worktree_id?.trim();
    const row = inspection.inspectedRow();
    if (!scopedWorktree() || !row) return false;
    return wid ? row.entry.worktree_id !== wid : !row.entry.bound_to_session;
  };
  const branch = () =>
    scopedWorktree() ? boundBranch() : props.status?.branch;
  const canChangeBranch = () =>
    !busy() &&
    !bound() &&
    !inspection.selection().unavailable &&
    checkoutKnown() &&
    !loadError() &&
    props.status?.available === true;
  const canCreateWorktree = () =>
    canChangeBranch() &&
    !inspectedLinked() &&
    !!branch() &&
    !!props.repoId &&
    !!props.onBindWorktree &&
    hasWorktreeSupport();
  const canMerge = () =>
    scopedWorktree() &&
    !stale() &&
    !busy() &&
    !loadError() &&
    !!props.onLandWorktree &&
    ((worktree()?.ahead_of_base ?? 0) > 0 || !!worktree()?.land_blocked_reason);
  const commitCount = () => worktree()?.ahead_of_base ?? 0;
  const commitLabel = () =>
    `${commitCount()} ${commitCount() === 1 ? "commit" : "commits"}`;
  const creating = () => dialog() === "branch" || dialog() === "worktree";
  const heading = () =>
    ({
      branch: "Create branch",
      worktree: "Create worktree",
      merge: "Merge into project folder?",
      remove: "Return to project folder?",
    })[dialog() ?? "branch"];
  const filteredBranches = () =>
    branches().filter((b) =>
      b.name.toLowerCase().includes(query().toLowerCase()),
    );

  const refresh = async () => {
    const version = ++viewVersion;
    const readWorktree = props.onRefreshWorktree;
    if (!hasWorktreeSupport() || !readWorktree) return;
    setLoading(true);
    setLoadError(undefined);
    try {
      const view = await readWorktree();
      if (version !== viewVersion) return;
      if (!view) throw new Error("Could not read this chat's checkout.");
      setWorktree(view);
    } catch (error) {
      if (version === viewVersion) setLoadError(errorMessage(error));
    } finally {
      if (version === viewVersion) setLoading(false);
    }
  };

  const contextKey = createMemo(() =>
    [props.projectId ?? "", props.sessionId ?? "", props.repoId].join("\0"),
  );

  createEffect(
    on(contextKey, () => {
      contextVersion++;
      viewVersion++;
      branchVersion++;
      setWorktree(undefined);
      setLoadError(undefined);
      setLoading(false);
      setPending(false);
      setDialog(undefined);
      setName("");
      setActionError(undefined);
      setActionBlocker(undefined);
      setResult(undefined);
      props.onMenuOpenChange(false);
      void refresh();
    }),
  );
  // Status refreshes also update merge counts; checkout target switches close open branch dialogs.
  createEffect(
    on(
      () => props.status?.worktree_id,
      () => {
        if (!pending() && dialog() === "branch") {
          setDialog(undefined);
          setActionError(undefined);
        }
      },
      { defer: true },
    ),
  );
  createEffect(
    on(
      () => props.status,
      () => {
        if (!pending() && hasWorktreeSupport()) void refresh();
      },
      { defer: true },
    ),
  );
  onCleanup(() => {
    contextVersion++;
    viewVersion++;
    branchVersion++;
  });

  const loadBranches = async () => {
    const version = ++branchVersion;
    setBranchesLoading(true);
    setBranchesError(undefined);
    try {
      const list = await props.onListBranches();
      if (version === branchVersion) setBranches(list);
    } catch (error) {
      if (version === branchVersion) setBranchesError(errorMessage(error));
    } finally {
      if (version === branchVersion) setBranchesLoading(false);
    }
  };
  createEffect(
    on(
      () => props.menuOpen,
      (open) => {
        if (!open) {
          branchVersion++;
          return;
        }
        setQuery("");
        setBranches([]);
        setActionError(undefined);
        if (canChangeBranch()) void loadBranches();
      },
    ),
  );

  createAnchoredPopoverFocus(
    () => props.menuOpen,
    () => panel,
    {
      trigger: () => trigger,
      onEscape: () => props.onMenuOpenChange(false),
    },
  );
  createEffect(() => {
    if (!props.menuOpen) return;
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !container?.contains(event.target))
        props.onMenuOpenChange(false);
    };
    document.addEventListener("pointerdown", outside);
    onCleanup(() => document.removeEventListener("pointerdown", outside));
  });
  const closeDialog = () => {
    if (pending()) return;
    setDialog(undefined);
    setActionError(undefined);
    setActionBlocker(undefined);
  };

  const openDialog = (next: Dialog) => {
    props.onMenuOpenChange(false);
    setName("");
    setActionError(undefined);
    setActionBlocker(undefined);
    setResult(undefined);
    setDialog(next);
  };

  const perform = async (action: () => Promise<void>) => {
    if (busy()) return;
    const version = contextVersion;
    setPending(true);
    setActionError(undefined);
    setActionBlocker(undefined);
    viewVersion++;
    try {
      await action();
      if (version === contextVersion) {
        setPending(false);
        setDialog(undefined);
        props.onMenuOpenChange(false);
      }
    } catch (error) {
      if (version === contextVersion) {
        const parsed = parseWorktreeRemovalBlocker(error);
        setActionError(parsed.message);
        setActionBlocker(parsed);
      }
    } finally {
      if (version === contextVersion) {
        setPending(false);
        void refresh();
      }
    }
  };
  const blockerNav = () =>
    resolveWorktreeBlockerNavigation({
      projectId: props.projectId,
      appStore: props.appStore,
      blocker: actionBlocker(),
      onDismissDialog: closeDialog,
    });
  const switchBranch = (next: string) => {
    if (!canChangeBranch() || next === branch()) return;
    void perform(() => props.onCheckout(next, false));
  };
  const submit = () => {
    const mode = dialog();
    const next = name().trim();
    if (creating() && (!next || !canChangeBranch())) return;
    if (mode === "worktree" && !canCreateWorktree()) return;
    if (mode === "merge" && !canMerge()) return;
    const version = contextVersion;
    void perform(async () => {
      if (mode === "branch") await props.onCheckout(next, true);
      if (mode === "worktree") {
        const bind = props.onBindWorktree;
        if (!bind) throw new Error("Worktree creation is unavailable.");
        const view = await bind(props.repoId, next);
        if (version === contextVersion) setWorktree(view);
        void inspection.refreshList();
      }
      if (mode === "merge") {
        const merge = props.onLandWorktree;
        if (!merge) throw new Error("Worktree merging is unavailable.");
        const outcome = await merge();
        if (version === contextVersion)
          setResult(
            formatWorktreeLandResult(outcome, branch() ?? "this branch"),
          );
        void inspection.refreshList();
      }
      if (mode === "remove" && props.onUnbindWorktree) {
        await flushCheckoutBuffersBeforeRemoval(props.projectId, worktree()?.worktree_id);
        await props.onUnbindWorktree();
        const view = await props.onRefreshWorktree?.();
        if (version === contextVersion) setWorktree(view);
        void inspection.refreshList();
      }
    });
  };

  return (
    <div class="git-checkout" ref={container} data-testid="git-checkout">
      <div class="git-checkout__location" data-testid="git-checkout-location">
        <Show
          when={checkoutKnown()}
          fallback={<span data-testid="git-checkout-pending">Checking checkout…</span>}
        >
          <span data-testid={inspection.modeInfo().testId}>
            {inspection.modeInfo().label}
          </span>
          <Show when={props.repoLabel}>
            <span class="git-checkout__repository">{props.repoLabel}</span>
          </Show>
          <Show when={inspection.directoryLabel()}>
            {(dir) => (
              <code class="git-checkout__directory" data-testid="git-checkout-directory">
                {dir()}
              </code>
            )}
          </Show>
        </Show>
      </div>
      <div class="git-strip__row git-strip__row--nav">
        <button
          ref={trigger}
          type="button"
          class="git-strip__branch"
          data-testid="git-branch"
          aria-label={`Branch and checkout: ${inspection.displayBranch()}`}
          aria-expanded={props.menuOpen}
          aria-controls={`${id}-menu`}
          aria-haspopup="dialog"
          aria-disabled={triggerUnavailable() ? "true" : undefined}
          onClick={() => {
            if (!triggerUnavailable()) props.onMenuOpenChange(!props.menuOpen);
          }}
        >
          <span class="git-strip__leaf" aria-hidden="true">
            ⎇
          </span>
          <span class="git-strip__branch-name" data-tip={inspection.displayBranch()} data-tip-when-clipped>
            {inspection.displayBranch()}
          </span>
          <span class="git-strip__caret" aria-hidden="true">
            ⌄
          </span>
        </button>
        {props.children}
      </div>
      <Show when={loadError()}>
        <div class="git-checkout__error" role="alert">
          <p>{loadError()}</p>
          <DenButton
            variant="link"
            data-testid="git-worktree-retry"
            disabled={busy() || loading()}
            onClick={() => void refresh()}
          >
            Retry checkout
          </DenButton>
        </div>
      </Show>
      <Show when={scopedWorktree()}>
        <div class="git-checkout__worktree" data-testid="git-worktree">
          <Show
            when={!stale()}
            fallback={
              <>
                <p role="status" data-testid="git-worktree-stale-status">
                  This chat's worktree is missing or invalid. Return to the
                  project folder to continue.
                </p>
                <code>{worktree()?.path}</code>
              </>
            }
          >
            <p data-testid="git-worktree-bound-summary">
              <Show when={inspectingOtherCheckout()}>
                Chat bound to <strong>{boundBranch()}</strong> ·{" "}
              </Show>
              Based on <strong>{worktree()?.base_branch}</strong> ·{" "}
              {commitLabel()} ahead · {worktree()?.behind_base ?? 0} behind
            </p>
            <p>
              {inspectingOtherCheckout()
                ? "Inspecting another checkout above leaves this chat's bound worktree unchanged."
                : "Changes and commits here belong to this chat's worktree."}
            </p>
          </Show>
          <div class="git-checkout__actions">
            <Show when={!stale()}>
              <DenButton
                variant="secondary"
                compact
                data-testid="git-worktree-land"
                disabled={!canMerge()}
                onClick={() => openDialog("merge")}
              >
                Merge into {worktree()?.base_branch}…
              </DenButton>
            </Show>
            <DenButton
              variant="ghost"
              compact
              data-testid="git-worktree-unbind"
              disabled={busy() || !props.onUnbindWorktree}
              onClick={() => openDialog("remove")}
            >
              Return to project folder…
            </DenButton>
          </div>
        </div>
      </Show>
      <Show when={result()}>
        {(outcome) => (
          <div
            class="git-checkout__result"
            role="status"
            data-testid="git-worktree-result"
          >
            <p>{outcome().sentence}</p>
            <Show when={outcome().conflicts.length}>
              <ul>
                <For each={outcome().conflicts}>
                  {(path) => <li>{path}</li>}
                </For>
              </ul>
            </Show>
          </div>
        )}
      </Show>
      <Show when={props.menuOpen}>
        <div
          ref={panel}
          id={`${id}-menu`}
          class="git-checkout__menu"
          role="dialog"
          aria-label="Branch and checkout"
          data-testid="git-branches"
        >
          <Show when={inspection.enabled()}>
            <GitWorktreeSelector
              worktrees={inspection.worktrees()}
              inspectedWorktreeId={inspection.selection().inspectedWorktreeId}
              pinned={inspection.selection().pinned}
              unavailable={inspection.selection().unavailable}
              preservedSummary={inspection.selection().summary}
              loading={inspection.loading()}
              error={inspection.error()}
              onSelectWorktree={(entry) => inspection.selectWorktree(entry)}
              onClearPin={() => inspection.clearPin()}
              onRetry={() => void inspection.refreshList()}
              onOpenInFiles={
                props.onOpenInFiles
                  ? () => {
                      props.onMenuOpenChange(false);
                      props.onOpenInFiles?.();
                    }
                  : undefined
              }
            />
          </Show>
          <Show
            when={!bound()}
            fallback={
              <>
                <p class="git-checkout__hint" data-testid="git-branches-bound-hint">
                  This chat uses a worktree
                  {!scopedWorktree()
                    ? ` for ${worktree()?.label || "another repository"}`
                    : ""}
                  . Return to the project folder before switching branches.
                </p>
                <Show when={!scopedWorktree() && worktree()?.repo_id}>
                  <DenButton
                    variant="link"
                    onClick={() => {
                      const repoId = worktree()?.repo_id;
                      if (repoId) props.onSelectWorktreeRepo(repoId);
                    }}
                  >
                    Open {worktree()?.label || "worktree repository"}
                  </DenButton>
                </Show>
              </>
            }
          >
            <label class="git-checkout__label" for={`${id}-search`}>
              Switch branch
            </label>
            <input
              id={`${id}-search`}
              class="git-checkout__input"
              data-testid="git-branch-search"
              type="search"
              placeholder="Find a branch…"
              value={query()}
              onInput={(e) => setQuery(e.currentTarget.value)}
            />
            <p class="git-checkout__hint">
              Switches the branch in the project folder. Other chats using that
              folder see the switch too.
            </p>
            <Show when={props.status?.dirty}>
              <p class="git-checkout__hint">
                Uncommitted changes stay in this folder. Git refuses the switch
                if it would overwrite them.
              </p>
            </Show>
            <Show when={branchesLoading()}>
              <p role="status">Loading branches…</p>
            </Show>
            <Show when={branchesError()}>
              <div class="git-checkout__error" role="alert">
                <p>{branchesError()}</p>
                <DenButton data-testid="retry-branches"
                  variant="link"
                  disabled={branchesLoading()}
                  onClick={() => void loadBranches()}
                >
                  Retry branches
                </DenButton>
              </div>
            </Show>
            <Show when={!branchesLoading() && !branchesError()}>
              <Scrollport
                class="git-checkout__branches"
                contentAs="ul"
                contentClass="git-checkout__branches-content"
              >
                <For each={filteredBranches()}>
                  {(entry) => (
                    <li>
                      <button
                        type="button"
                        class="git-checkout__branch-option"
                        data-testid="git-branch-item"
                        disabled={busy() || entry.current}
                        onClick={() => switchBranch(entry.name)}
                      >
                        <span>{entry.name}</span>
                        <Show when={entry.current}>
                          <span class="git-checkout__hint">Current</span>
                        </Show>
                      </button>
                    </li>
                  )}
                </For>
              </Scrollport>
              <Show when={!filteredBranches().length}>
                <p class="git-checkout__hint">
                  {query() ? "No matching branches." : "No local branches yet."}
                </p>
              </Show>
            </Show>
            <div class="git-checkout__choices">
              <button
                type="button"
                class="git-checkout__choice"
                data-testid="git-new-branch-open"
                disabled={!canChangeBranch()}
                onClick={() => openDialog("branch")}
              >
                <strong>Create branch…</strong>
                <span>Work in the current project folder</span>
              </button>
              <Show when={hasWorktreeSupport() && props.onBindWorktree}>
                <button
                  type="button"
                  class="git-checkout__choice"
                  data-testid="git-worktree-bind-open"
                  disabled={!canCreateWorktree()}
                  onClick={() => openDialog("worktree")}
                >
                  <strong>Create worktree…</strong>
                  <span>Give this chat a separate checkout and new branch</span>
                </button>
                <Show when={!branch()}>
                  <p class="git-checkout__hint">
                    Check out a branch before creating a worktree.
                  </p>
                </Show>
              </Show>
            </div>
          </Show>
          <Show when={actionError() && !dialog()}>
            <p class="git-checkout__error" role="alert">
              {actionError()}
            </p>
          </Show>
          <DenButton
            variant="ghost"
            compact
            onClick={() => props.onMenuOpenChange(false)}
          >
            Close
          </DenButton>
        </div>
      </Show>
      <Show when={dialog()} keyed>
        {(activeDialog) => (
          <GitCheckoutModal
            idPrefix={id}
            dialog={activeDialog}
            heading={heading()}
            creating={creating()}
            pending={pending()}
            busy={busy()}
            name={name()}
            branch={branch()}
            commitLabel={commitLabel()}
            repoLabel={props.repoLabel}
            repositoryCount={props.repositoryCount}
            status={props.status}
            worktree={worktree()}
            actionError={actionError()}
            blockerNav={blockerNav()}
            onNameInput={(val) => {
              setName(val);
              setActionError(undefined);
              setActionBlocker(undefined);
            }}
            onClose={closeDialog}
            onSubmit={submit}
          />
        )}
      </Show>
    </div>
  );
}
