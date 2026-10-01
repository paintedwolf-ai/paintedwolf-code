import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import { paginateSlice } from "../../list/pagination.ts";
import { TablePager } from "../list/TablePager.tsx";

export type DenseReadOnlyListItem = {
  id: string;
  primary: string;
  secondary?: string;
  /** Right-aligned meta (e.g. relative time). */
  meta?: string;
  /** Hover tip for meta (e.g. absolute timestamp). */
  metaTitle?: string;
};

export const DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE = 10;

type Props = {
  items: readonly DenseReadOnlyListItem[];
  emptyLabel: string;
  ariaLabel: string;
  pageSize?: number;
  testId?: string;
};

export function DenseReadOnlyList(props: Props) {
  const pageSize = () => props.pageSize ?? DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE;
  const testId = () => props.testId ?? "dense-read-only-list";
  const [page, setPage] = createSignal(0);

  createEffect(() => {
    props.items;
    pageSize();
    setPage(0);
  });

  const paged = createMemo(() => paginateSlice(props.items, page(), pageSize()));

  createEffect(() => {
    const { pageCount } = paged();
    if (pageCount === 0) {
      if (page() !== 0) setPage(0);
      return;
    }
    if (page() > pageCount - 1) setPage(pageCount - 1);
  });

  return (
    <div class="den-dense-list" data-testid={testId()} role="region" aria-label={props.ariaLabel}>
      <Show
        when={props.items.length > 0}
        fallback={
          <p class="den-dense-list-empty" data-testid={`${testId()}-empty`}>
            {props.emptyLabel}
          </p>
        }
      >
        <ul class="den-dense-list-items" data-testid={`${testId()}-items`}>
          <For each={paged().slice}>
            {(item) => (
              <li class="den-dense-list-row" data-testid={`${testId()}-row-${item.id}`}>
                <span class="den-dense-list-primary">{item.primary}</span>
                <Show when={item.meta} keyed>
                  {(meta) => (
                    <span class="den-dense-list-meta" data-tip={item.metaTitle}>
                      {meta}
                    </span>
                  )}
                </Show>
                <Show when={item.secondary} keyed>
                  {(secondary) => (
                    <span class="den-dense-list-secondary">{secondary}</span>
                  )}
                </Show>
              </li>
            )}
          </For>
        </ul>
        <TablePager
          page={page()}
          pageSize={pageSize()}
          total={paged().total}
          onPageChange={setPage}
          testId={`${testId()}-pager`}
          ariaLabel={`${props.ariaLabel} pagination`}
          compact
        />
      </Show>
    </div>
  );
}
