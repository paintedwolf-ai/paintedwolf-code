import { Show } from "solid-js";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import type { TurnDiffsAddress } from "./diffs-address.ts";

type Props = {
  title: string;
  note: string;
  /** Whether the initial read is pending. */
  reading: boolean;
  fileCount: number;
  total: { added: number; removed: number } | null;
  /** Formatted writes count label, or null for git comparisons. */
  writes: string | null;
  turnAddress: TurnDiffsAddress | null;
  onWalkTurn?: (address: TurnDiffsAddress) => void;
  foldable: boolean;
  allOpen: boolean;
  onFoldAll: () => void;
};

function fileCount(count: number): string {
  return count === 1 ? "1 file changed" : `${count} files changed`;
}

export function DiffsPageHeading(props: Props) {
  return (
    <article class="den-diffs-page__article">
      <header class="den-diffs-page__head">
        <span class="den-diffs-page__glyph" aria-hidden="true">
          <ThemeIcon slot="diff" size={16} />
        </span>
        <div>
          <p class="den-diffs-page__eyebrow" data-testid="diffs-page-comparison">{props.title}</p>
          <h1 class="den-diffs-page__title" data-testid="diffs-page-title">
            <Show when={props.reading && props.fileCount === 0} fallback={fileCount(props.fileCount)}>
              Reading this comparison…
            </Show>
          </h1>
          <p class="den-diffs-page__stat" data-testid="diffs-page-stat">
            <Show when={props.total}>
              {(counted) => (
                <>
                  <span class="den-file-edit-diff-stat-add">+{counted().added}</span>
                  <span class="den-file-edit-diff-stat-del">−{counted().removed}</span>
                  <Show when={props.writes}><span aria-hidden="true"> · </span></Show>
                </>
              )}
            </Show>
            {props.writes}
          </p>
        </div>
      </header>
      <p class="den-diffs-page__lede">{props.note}</p>
      <div class="den-diffs-page__actions">
        <Show when={props.turnAddress}>
          {(address) => (
            <button
              type="button"
              class="den-diffs-page__action"
              data-testid="diffs-page-walk"
              onClick={() => props.onWalkTurn?.(address())}
            >
              <ThemeIcon slot="walk" size={13} />
              Walk these changes
            </button>
          )}
        </Show>
        <Show when={props.foldable}>
          <button
            type="button"
            class="den-diffs-page__action"
            data-testid="diffs-page-fold"
            onClick={() => props.onFoldAll()}
          >
            {props.allOpen ? "Collapse all" : "Expand all"}
          </button>
        </Show>
      </div>
    </article>
  );
}
