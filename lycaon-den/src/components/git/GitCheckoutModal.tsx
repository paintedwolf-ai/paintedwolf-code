import { onCleanup, Show } from "solid-js";
import type { GitWorktreeView } from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import type { WorktreeBlockerNavigation } from "./worktree-lifecycle-blocker.ts";

export type GitCheckoutDialogKind = "branch" | "worktree" | "merge" | "remove";

export function GitCheckoutModal(props: {
  idPrefix: string;
  dialog: GitCheckoutDialogKind;
  heading: string;
  creating: boolean;
  pending: boolean;
  busy: boolean;
  name: string;
  branch?: string;
  commitLabel: string;
  repoLabel?: string;
  repositoryCount: number;
  status?: GitWorkspaceStatus;
  worktree?: GitWorktreeView;
  actionError?: string;
  blockerNav?: WorktreeBlockerNavigation;
  onModalRef: (el: HTMLDivElement | undefined) => void;
  onNameInput: (value: string) => void;
  onClose: () => void;
  onSubmit: () => void;
}) {
  return (
    <ResidentPortal>
      <div
        class="den-dialog-backdrop"
        onClick={(e) => {
          if (e.target === e.currentTarget) props.onClose();
        }}
      >
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <div
          ref={(el) => {
            props.onModalRef(el);
            onCleanup(() => props.onModalRef(undefined));
          }}
          class="den-dialog git-checkout-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby={`${props.idPrefix}-title`}
          aria-describedby={`${props.idPrefix}-description`}
          data-testid={
            props.dialog === "merge"
              ? "git-worktree-land-modal"
              : props.dialog === "remove"
                ? "git-worktree-unbind-modal"
                : "git-checkout-create-modal"
          }
        >
          <header class="den-dialog__header" {...chromeProps()}>
            <h2 id={`${props.idPrefix}-title`}>{props.heading}</h2>
          </header>
          <form
            class="git-checkout-dialog__form"
            aria-busy={props.pending}
            onSubmit={(e) => {
              e.preventDefault();
              props.onSubmit();
            }}
          >
            <Scrollport
              class="git-checkout-dialog__body"
              contentClass="git-checkout-dialog__body-content"
            >
              <Show
                when={props.creating}
                fallback={
                  <p
                    id={`${props.idPrefix}-description`}
                    data-testid={
                      props.dialog === "merge"
                        ? "git-worktree-land-description"
                        : "git-worktree-unbind-description"
                    }
                  >
                    <Show
                      when={props.dialog === "merge"}
                      fallback={
                        <>
                          This chat will resume in the project folder. The
                          separate checkout will be removed; branch{" "}
                          <strong>{props.branch}</strong> and its commits will
                          be kept.
                        </>
                      }
                    >
                      Merge {props.commitLabel} from{" "}
                      <strong>{props.branch}</strong> into{" "}
                      <strong>{props.worktree?.base_branch}</strong> in the
                      project folder. This chat will stay in its worktree.
                    </Show>
                  </p>
                }
              >
                <p id={`${props.idPrefix}-description`}>
                  {props.dialog === "worktree"
                    ? "Give this chat a separate checkout on a new branch."
                    : "Create and switch to a new branch in the project folder."}
                </p>
                <dl
                  class="git-checkout-dialog__facts"
                  data-testid="git-checkout-facts"
                >
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
                      {props.dialog === "worktree"
                        ? "New chat worktree"
                        : "Current project folder"}
                    </dd>
                  </div>
                </dl>
                <label class="git-checkout__label" for={`${props.idPrefix}-name`}>
                  New branch name
                </label>
                <input
                  id={`${props.idPrefix}-name`}
                  class="git-checkout__input"
                  data-testid={
                    props.dialog === "worktree"
                      ? "git-worktree-branch"
                      : "git-new-branch"
                  }
                  value={props.name}
                  required
                  autocomplete="off"
                  spellcheck={false}
                  placeholder="feature/your-change"
                  disabled={props.busy}
                  onInput={(e) => props.onNameInput(e.currentTarget.value)}
                />
                <div
                  class="git-checkout-dialog__notice"
                  data-testid={
                    props.dialog === "worktree"
                      ? "git-worktree-notice"
                      : "git-new-branch-notice"
                  }
                >
                  <Show
                    when={props.dialog === "worktree"}
                    fallback={
                      <>
                        <p>
                          Your uncommitted changes stay in this folder on the
                          new branch.
                        </p>
                        <p>
                          Other chats using the project folder see the branch
                          change too.
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
                      {props.status?.dirty && props.status.changed_count === 1
                        ? "is"
                        : "are"}{" "}
                      not copied.
                    </p>
                    <Show when={props.repositoryCount > 1}>
                      <p>
                        Other attached repositories continue using their project
                        folders.
                      </p>
                    </Show>
                  </Show>
                </div>
              </Show>
              <Show when={!props.creating}>
                <p
                  class="git-checkout-dialog__notice"
                  data-testid={
                    props.dialog === "merge"
                      ? "git-worktree-land-notice"
                      : "git-worktree-unbind-notice"
                  }
                >
                  {props.dialog === "merge"
                    ? "Both checkouts must have their changes committed or stashed before merging."
                    : "Commit or stash any worktree changes before returning. Unmerged commits remain on the branch."}
                </p>
              </Show>
              <Show when={props.actionError}>
                <div class="git-checkout__error" role="alert">
                  <p>{props.actionError}</p>
                  <Show when={props.blockerNav} keyed>
                    {(nav) => (
                      <DenButton
                        variant="link"
                        data-testid="git-worktree-blocker-action"
                        onClick={() => nav.run()}
                      >
                        {nav.label}
                      </DenButton>
                    )}
                  </Show>
                </div>
              </Show>
            </Scrollport>
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                data-testid={
                  props.dialog === "merge"
                    ? "git-worktree-land-cancel"
                    : props.dialog === "remove"
                      ? "git-worktree-unbind-cancel"
                      : "git-checkout-cancel"
                }
                disabled={props.pending}
                onClick={props.onClose}
              >
                Cancel
              </DenButton>
              <DenButton
                variant={props.dialog === "remove" ? "danger" : "primary"}
                type="submit"
                disabled={props.busy || (props.creating && !props.name.trim())}
                data-testid={
                  props.dialog === "worktree"
                    ? "git-worktree-create"
                    : props.dialog === "branch"
                      ? "git-create-branch"
                      : props.dialog === "merge"
                        ? "git-worktree-land-confirm"
                        : "git-worktree-unbind-confirm"
                }
              >
                {props.pending
                  ? "Working…"
                  : props.dialog === "worktree"
                    ? "Create worktree"
                    : props.dialog === "branch"
                      ? "Create branch"
                      : props.dialog === "merge"
                        ? "Merge"
                        : "Return and remove worktree"}
              </DenButton>
            </footer>
          </form>
        </div>
      </div>
    </ResidentPortal>
  );
}
