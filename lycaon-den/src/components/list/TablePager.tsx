import { Show, createMemo } from "solid-js";
import { paginationWindow } from "../../list/pagination.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { StableLabel, widestDigits } from "../primitives/StableLabel.tsx";

type CommonProps = {
  /** The requested page; Previous and Next step from it. */
  page: number;
  /** The page whose rows are on screen, when a request for `page` is still pending. */
  shownPage?: number;
  onPageChange: (page: number) => void;
  testId?: string;
  ariaLabel?: string;
  compact?: boolean;
  /** Reported as busy; the controls keep their labels and enablement while a page loads. */
  loading?: boolean;
  class?: string;
};

type Props = CommonProps &
  (
    | {
        pageSize: number;
        total: number;
        canNext?: boolean;
        itemCount?: never;
        hasNext?: never;
      }
    | {
        itemCount: number;
        hasNext: boolean;
        pageSize?: never;
        total?: never;
        canNext?: never;
      }
  );

/** Stable label widths keep paging controls fixed as rows change. */
export function TablePager(props: Props) {
  const window = createMemo(() =>
    props.total === undefined
      ? null
      : paginationWindow(props.total, props.page, props.pageSize),
  );
  const shownWindow = createMemo(() =>
    props.total === undefined
      ? null
      : paginationWindow(props.total, props.shownPage ?? props.page, props.pageSize),
  );
  const testId = () => props.testId ?? "table-pager";
  const visible = () => {
    const offset = window();
    return offset ? offset.pageCount > 1 : props.page > 0 || !!props.hasNext;
  };
  const rangeLabel = () => {
    const offset = shownWindow();
    return offset
      ? `${offset.from}–${offset.to} of ${offset.total}`
      : `${props.itemCount ?? 0} shown`;
  };
  const rangeSizer = () => {
    const offset = shownWindow();
    if (!offset) return `${widestDigits(props.itemCount ?? 0, 2)} shown`;
    const digits = widestDigits(offset.total);
    return `${digits}–${digits} of ${digits}`;
  };
  const pageLabel = () => {
    const offset = shownWindow();
    return offset
      ? `Page ${offset.page + 1} of ${offset.pageCount}`
      : `Page ${(props.shownPage ?? props.page) + 1}`;
  };
  const pageSizer = () => {
    const offset = shownWindow();
    if (!offset) return `Page ${widestDigits(props.page + 1, 2)}`;
    const digits = widestDigits(offset.pageCount);
    return `Page ${digits} of ${digits}`;
  };
  const nextDisabled = () => {
    const offset = window();
    return offset
      ? props.canNext === false || offset.page + 1 >= offset.pageCount
      : !props.hasNext;
  };

  return (
    <Show when={visible()}>
      <div
        class={`den-table-pager ${props.class ?? ""}`}
        classList={{ "den-table-pager-compact": props.compact }}
        data-testid={testId()}
        role="navigation"
        aria-label={props.ariaLabel ?? "Table pagination"}
        aria-busy={props.loading ? true : undefined}
      >
        <StableLabel
          class="den-table-pager-range"
          label={rangeLabel()}
          reserve={[rangeSizer()]}
          testId={`${testId()}-range`}
        />
        <div class="den-table-pager-actions">
          <DenButton
            variant="secondary"
            compact
            data-testid={`${testId()}-prev`}
            disabled={props.page <= 0}
            onClick={() => props.onPageChange(props.page - 1)}
          >
            Previous
          </DenButton>
          <span class="den-table-pager-page" aria-live="polite">
            <StableLabel
              label={pageLabel()}
              reserve={[pageSizer()]}
              align="center"
              testId={`${testId()}-page`}
            />
          </span>
          <DenButton
            variant="secondary"
            compact
            data-testid={`${testId()}-next`}
            disabled={nextDisabled()}
            onClick={() => props.onPageChange(props.page + 1)}
          >
            Next
          </DenButton>
        </div>
      </div>
    </Show>
  );
}
