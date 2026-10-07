import { ChromeDragSurface } from "./shell/ChromeDragSurface.tsx";
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
import { ResidentPortal } from "./primitives/ResidentPortal.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import type {
  GitBranchEntry,
  GitWorktreeView,
} from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";
import type { WorktreeLandOutcome } from "../chat/actions/git-actions.ts";
import {
  createAnchoredPopoverFocus,
  createModalFocusTrap,
} from "../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../styling/ui-chrome.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { formatWorktreeLandResult } from "./git-worktree-result.ts";

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
};

type Props = GitCheckoutActions & {
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
  children?: JSX.Element;
};

type Dialog = "branch" | "worktree" | "merge" | "remove";

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
  const [pending, setPending] = createSignal(false);
  const [result, setResult] =
    createSignal<ReturnType<typeof formatWorktreeLandResult>>();
  let trigger: HTMLButtonElement | undefined;
  let panel: HTMLDivElement | undefined;
  const [modal, setModal] = createSignal<HTMLDivElement | undefined>();
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
  // Stays focusable while unavailable so dialogs can restore focus to it.
  const triggerUnavailable = () => busy() || !checkoutKnown() || !!loadError();
  const branch = () =>
    scopedWorktree() ? worktree()?.branch : props.status?.branch;
  const canChangeBranch = () =>
    !busy() &&
    !bound() &&
    checkoutKnown() &&
    !loadError() &&
    props.status?.available === true;
  const canCreateWorktree = () =>
    canChangeBranch() &&
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
      setResult(undefined);
      props.onMenuOpenChange(false);
      void refresh();
    }),
  );
  // Status refreshes also update merge counts.
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
    setModal(undefined);
  };
  createModalFocusTrap(
    () => dialog() !== undefined,
    modal,
    {
      onEscape: closeDialog,
    },
  );

  const openDialog = (next: Dialog) => {
    props.onMenuOpenChange(false);
    setName("");
    setActionError(undefined);
    setResult(undefined);
    setModal(undefined);
    setDialog(next);
  };

  const perform = async (action: () => Promise<void>) => {
    if (busy()) return;
    const version = contextVersion;
    setPending(true);
    setActionError(undefined);
    viewVersion++;
    try {
      await action();
      if (version === contextVersion) {
        setPending(false);
        setDialog(undefined);
        props.onMenuOpenChange(false);
      }
    } catch (error) {
      if (version === contextVersion) setActionError(errorMessage(error));
    } finally {
      if (version === contextVersion) {
        setPending(false);
        void refresh();
      }
    }
  };
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
      }
      if (mode === "merge") {
        const merge = props.onLandWorktree;
        if (!merge) throw new Error("Worktree merging is unavailable.");
        const outcome = await merge();
        if (version === contextVersion)
          setResult(
            formatWorktreeLandResult(outcome, branch() ?? "this branch"),
          );
      }
      if (mode === "remove" && props.onUnbindWorktree) {
        await props.onUnbindWorktree();
        const view = await props.onRefreshWorktree?.();
        if (version === contextVersion) setWorktree(view);
      }
    });
  };

  return (
    <div class="git-checkout" ref={container} data-testid="git-checkout">
      <div class="git-checkout__location" data-testid="git-checkout-location">
        <Show when={checkoutKnown()} fallback="Checking checkout…">
          <span>{scopedWorktree() ? "Chat worktree" : "Project folder"}</span>
          <Show when={props.repoLabel}>
            <span class="git-checkout__repository">{props.repoLabel}</span>
          </Show>
        </Show>
      </div>
      <div class="git-strip__row git-strip__row--nav">
        <button
          ref={trigger}
          type="button"
          class="git-strip__branch"
          data-testid="git-branch"
          aria-label={`Branch and checkout: ${branch() || "Detached HEAD"}`}
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
          <span class="git-strip__branch-name" data-tip={branch()} data-tip-when-clipped>
            {branch() ||
              (props.status?.head_short
                ? `Detached at ${props.status.head_short}`
                : "No branch")}
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
                <p role="status">
                  This chat's worktree is missing or invalid. Return to the
                  project folder to continue.
                </p>
                <code>{worktree()?.path}</code>
              </>
            }
          >
            <p>
              Based on <strong>{worktree()?.base_branch}</strong> ·{" "}
              {commitLabel()} ahead · {worktree()?.behind_base ?? 0} behind
            </p>
            <p>Changes and commits here belong to this chat's worktree.</p>
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
          <Show
            when={!bound()}
            fallback={
              <>
                <p class="git-checkout__hint">
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
                <DenButton
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
      <Show when={dialog()}>
        <ResidentPortal>
          <div
            class="den-dialog-backdrop"
            onClick={(e) => {
              if (e.target === e.currentTarget) closeDialog();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={(el) => {
                setModal(el);
                onCleanup(() => setModal(undefined));
              }}
              class="den-dialog git-checkout-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby={`${id}-title`}
              aria-describedby={`${id}-description`}
              data-testid={
                dialog() === "merge"
                  ? "git-worktree-land-modal"
                  : dialog() === "remove"
                    ? "git-worktree-unbind-modal"
                    : "git-checkout-create-modal"
              }
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id={`${id}-title`}>{heading()}</h2>
              </header>
              <form
                class="git-checkout-dialog__form"
                aria-busy={pending()}
                onSubmit={(e) => {
                  e.preventDefault();
                  submit();
                }}
              >
                <Scrollport
                  class="git-checkout-dialog__body"
                  contentClass="git-checkout-dialog__body-content"
                >
                  <Show
                    when={creating()}
                    fallback={
                      <p id={`${id}-description`}>
                        <Show
                          when={dialog() === "merge"}
                          fallback={
                            <>
                              This chat will resume in the project folder. The
                              separate checkout will be removed; branch{" "}
                              <strong>{branch()}</strong> and its commits will
                              be kept.
                            </>
                          }
                        >
                          Merge {commitLabel()} from <strong>{branch()}</strong>{" "}
                          into <strong>{worktree()?.base_branch}</strong> in the
                          project folder. This chat will stay in its worktree.
                        </Show>
                      </p>
                    }
                  >
                    <p id={`${id}-description`}>
                      {dialog() === "worktree"
                        ? "Give this chat a separate checkout on a new branch."
                        : "Create and switch to a new branch in the project folder."}
                    </p>
                    <dl class="git-checkout-dialog__facts">
                      <Show when={props.repoLabel}>
                        <div>
                          <dt>Repository</dt>
                          <dd>{props.repoLabel}</dd>
                        </div>
                      </Show>
                      <div>
                        <dt>Starting point</dt>
                        <dd>
                          {props.status?.branch ||
                            props.status?.head_short ||
                            "Current checkout"}
                        </dd>
                      </div>
                      <div>
                        <dt>Location</dt>
                        <dd>
                          {dialog() === "worktree"
                            ? "New chat worktree"
                            : "Current project folder"}
                        </dd>
                      </div>
                    </dl>
                    <label class="git-checkout__label" for={`${id}-name`}>
                      New branch name
                    </label>
                    <input
                      id={`${id}-name`}
                      class="git-checkout__input"
                      data-testid={
                        dialog() === "worktree"
                          ? "git-worktree-branch"
                          : "git-new-branch"
                      }
                      value={name()}
                      required
                      autocomplete="off"
                      spellcheck={false}
                      placeholder="feature/your-change"
                      disabled={busy()}
                      onInput={(e) => {
                        setName(e.currentTarget.value);
                        setActionError(undefined);
                      }}
                    />
                    <div class="git-checkout-dialog__notice">
                      <Show
                        when={dialog() === "worktree"}
                        fallback={
                          <>
                            <p>
                              Your uncommitted changes stay in this folder on
                              the new branch.
                            </p>
                            <p>
                              Other chats using the project folder see the
                              branch change too.
                            </p>
                          </>
                        }
                      >
                        <p>
                          The worktree starts from the latest commit on{" "}
                          <strong>{props.status?.branch}</strong>.
                        </p>
                        <p>
                          {props.status?.dirty
                            ? `${props.status.changed_count} changed ${props.status.changed_count === 1 ? "file stays" : "files stay"}`
                            : "Uncommitted changes stay"}{" "}
                          in the project folder and{" "}
                          {props.status?.dirty &&
                          props.status.changed_count === 1
                            ? "is"
                            : "are"}{" "}
                          not copied.
                        </p>
                        <Show when={props.repositoryCount > 1}>
                          <p>
                            Other attached repositories continue using their
                            project folders.
                          </p>
                        </Show>
                      </Show>
                    </div>
                  </Show>
                  <Show when={!creating()}>
                    <p class="git-checkout-dialog__notice">
                      {dialog() === "merge"
                        ? "Both checkouts must have their changes committed or stashed before merging."
                        : "Commit or stash any worktree changes before returning. Unmerged commits remain on the branch."}
                    </p>
                  </Show>
                  <Show when={actionError()}>
                    <p class="git-checkout__error" role="alert">
                      {actionError()}
                    </p>
                  </Show>
                </Scrollport>
                <footer class="den-dialog__footer">
                  <DenButton
                    variant="ghost"
                    data-testid={
                      dialog() === "merge"
                        ? "git-worktree-land-cancel"
                        : dialog() === "remove"
                          ? "git-worktree-unbind-cancel"
                          : "git-checkout-cancel"
                    }
                    disabled={pending()}
                    onClick={closeDialog}
                  >
                    Cancel
                  </DenButton>
                  <DenButton
                    variant={dialog() === "remove" ? "danger" : "primary"}
                    type="submit"
                    disabled={busy() || (creating() && !name().trim())}
                    data-testid={
                      dialog() === "worktree"
                        ? "git-worktree-create"
                        : dialog() === "branch"
                          ? "git-create-branch"
                          : dialog() === "merge"
                            ? "git-worktree-land-confirm"
                            : "git-worktree-unbind-confirm"
                    }
                  >
                    {pending()
                      ? "Working…"
                      : dialog() === "worktree"
                        ? "Create worktree"
                        : dialog() === "branch"
                          ? "Create branch"
                          : dialog() === "merge"
                            ? "Merge"
                            : "Return and remove worktree"}
                  </DenButton>
                </footer>
              </form>
            </div>
          </div>
        </ResidentPortal>
      </Show>
    </div>
  );
}
