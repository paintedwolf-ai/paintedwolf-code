import { For, Show, createMemo, createSignal } from "solid-js";
import type { GitWorktreeEntry } from "../../api/types.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  formatCheckoutBranchLabel,
  groupWorktreeEntries,
  type CheckoutPinSummary,
  type WorktreeRowView,
} from "./git-worktree-inspection.ts";

export type GitWorktreeSelectorProps = {
  worktrees: readonly GitWorktreeEntry[];
  inspectedWorktreeId?: string;
  pinned: boolean;
  unavailable: boolean;
  preservedSummary?: CheckoutPinSummary;
  loading?: boolean;
  error?: string;
  onSelectWorktree: (entry: GitWorktreeEntry) => void;
  onClearPin: () => void;
  onRetry?: () => void;
  onOpenInFiles?: () => void;
};

export function GitWorktreeSelector(props: GitWorktreeSelectorProps) {
  const [expandedIds, setExpandedIds] = createSignal<Set<string>>(new Set());

  const toggleExpand = (worktreeId: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(worktreeId)) next.delete(worktreeId);
      else next.add(worktreeId);
      return next;
    });
  };

  const groups = createMemo(() =>
    groupWorktreeEntries(props.worktrees, props.inspectedWorktreeId),
  );

  const hasAnyRows = () =>
    groups().project.length > 0 || groups().chat.length > 0 || groups().other.length > 0;

  const renderGroup = (title: string, testId: string, rows: readonly WorktreeRowView[]) => (
    <Show when={rows.length > 0}>
      <div class="git-worktrees__group" data-testid={testId}>
        <div class="git-checkout__label">{title}</div>
        <ul class="git-worktrees__list">
          <For each={rows}>
            {(row) => {
              const expanded = () => expandedIds().has(row.entry.worktree_id);
              return (
                <li class="git-worktrees__row">
                  <button
                    type="button"
                    class="git-checkout__branch-option git-worktrees__option"
                    classList={{ "git-worktrees__option--inspected": row.inspected }}
                    data-testid="git-worktree-option"
                    data-worktree-id={row.entry.worktree_id}
                    aria-current={row.inspected ? "true" : undefined}
                    disabled={!row.entry.available}
                    onClick={() => props.onSelectWorktree(row.entry)}
                  >
                    <span class="git-worktrees__primary">
                      <span class="git-worktrees__branch">{row.branchLabel}</span>
                      <code
                        class="git-worktrees__dir"
                        data-testid="git-worktree-dir"
                      >
                        {expanded() ? row.fullPath : row.shortDir}
                      </code>
                    </span>
                    <span class="git-worktrees__badges">
                      <Show when={row.inspected}>
                        <span
                          class="git-checkout__hint"
                          data-testid="git-worktree-badge-inspected"
                        >
                          Inspected
                        </span>
                      </Show>
                      <Show when={row.boundToChat}>
                        <span
                          class="git-checkout__hint"
                          data-testid="git-worktree-badge-bound"
                        >
                          Bound to chat
                        </span>
                      </Show>
                      <Show when={row.changeSummary}>
                        {(summary) => (
                          <span
                            class="git-checkout__hint"
                            data-testid="git-worktree-badge-changes"
                          >
                            {summary()}
                          </span>
                        )}
                      </Show>
                      <Show when={!row.entry.available}>
                        <span
                          class="git-checkout__hint"
                          data-testid="git-worktree-badge-unavailable"
                        >
                          Unavailable
                        </span>
                      </Show>
                    </span>
                  </button>
                  <Show when={row.hasDuplicateBasename}>
                    <button
                      type="button"
                      class="git-strip__scope-unpin git-worktrees__expand"
                      data-testid="git-worktree-expand-path"
                      aria-expanded={expanded()}
                      aria-label={
                        expanded()
                          ? `Show short path for ${row.branchLabel}`
                          : `Show full path for ${row.branchLabel}`
                      }
                      onClick={() => toggleExpand(row.entry.worktree_id)}
                    >
                      {expanded() ? "Short path" : "Full path"}
                    </button>
                  </Show>
                </li>
              );
            }}
          </For>
        </ul>
      </div>
    </Show>
  );

  return (
    <div class="git-worktrees" data-testid="git-worktree-selector">
      <div class="git-worktrees__header">
        <span class="git-checkout__label">Inspect checkout</span>
        <span class="git-worktrees__header-actions">
          <span class="git-strip__scope-follow" data-testid="git-worktree-follow">
            <Show when={props.pinned} fallback={<span>follows chat</span>}>
              <span>pinned</span>
              <button
                type="button"
                class="git-strip__scope-unpin"
                data-testid="git-worktree-unpin"
                onClick={() => props.onClearPin()}
              >
                Follow chat
              </button>
            </Show>
          </span>
          <Show when={props.onOpenInFiles} keyed>
            {(openInFiles) => (
              <DenButton
                variant="ghost"
                compact
                data-testid="git-open-in-files"
                disabled={props.unavailable}
                onClick={() => openInFiles()}
              >
                Open in Files
              </DenButton>
            )}
          </Show>
        </span>
      </div>

      <Show when={props.unavailable}>
        <div
          class="git-checkout__worktree"
          data-testid="git-checkout-target-unavailable"
        >
          <p>
            Selected checkout{" "}
            <strong>
              {props.preservedSummary
                ? formatCheckoutBranchLabel({
                    branch: props.preservedSummary.branch,
                    detached: props.preservedSummary.detached,
                    head_short: props.preservedSummary.headShort,
                  })
                : "target"}
            </strong>{" "}
            is no longer available on disk. Choose a current checkout to inspect
            or run Git actions.
          </p>
          <Show when={props.preservedSummary?.path}>
            <code>{props.preservedSummary?.path}</code>
          </Show>
        </div>
      </Show>

      <Show when={props.error}>
        <div class="git-checkout__worktree" data-testid="git-worktrees-error">
          <p>{props.error}</p>
          <Show when={props.onRetry} keyed>
            {(retry) => (
              <DenButton
                variant="link"
                data-testid="retry-worktrees"
                disabled={props.loading}
                onClick={() => retry()}
              >
                Retry checkouts
              </DenButton>
            )}
          </Show>
        </div>
      </Show>

      <Show when={hasAnyRows()}>
        <Scrollport
          class="git-checkout__branches git-worktrees__scroll"
          contentClass="git-checkout__branches-content"
        >
          {renderGroup("Project folder", "git-worktrees-group-project", groups().project)}
          {renderGroup("Used by this chat", "git-worktrees-group-chat", groups().chat)}
          {renderGroup("Other registered worktrees", "git-worktrees-group-other", groups().other)}
        </Scrollport>
      </Show>
    </div>
  );
}
