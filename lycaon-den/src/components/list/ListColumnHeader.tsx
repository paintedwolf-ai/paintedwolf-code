import { For, Show } from "solid-js";
import type { JSX } from "solid-js";
import {
  clampColumnWidth,
  columnFirstDirection,
  columnGridTemplate,
  columnSortable,
  type ColumnSpec,
  type ColumnWidths,
} from "../../list/list-columns.ts";
import {
  nextSortState,
  sortAriaValue,
  type SortState,
} from "../../list/list-sort.ts";
import { ResizeHandle } from "../primitives/ResizeHandle.tsx";
import { beginListColumnResize } from "../../shell/layout-store.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";

type ListColumnHeaderProps<T> = {
  surface: string;
  columns: readonly ColumnSpec<T>[];
  widths: ColumnWidths;
  sort: SortState;
  onSort: (state: SortState) => void;
  ariaLabel?: string;
  testId?: string;
  /** Replaces a column label with a control aligned to that column's rows. */
  renderCell?: (column: ColumnSpec<T>) => JSX.Element | undefined;
  /** References the content box shared by header and row tracks for width fitting. */
  measureRef?: (el: HTMLElement | undefined) => void;
};

/** Shared sortable header using the row grid template. */
export function ListColumnHeader<T>(props: ListColumnHeaderProps<T>) {
  const template = () => columnGridTemplate(props.columns, props.widths);
  const widthOf = (col: ColumnSpec<T>) =>
    props.widths[col.key] ?? col.width.basis;

  const lastIndex = () => props.columns.length - 1;
  const resizable = (col: ColumnSpec<T>, index: number) =>
    !col.width.grow && index < lastIndex();

  return (
    <div
      ref={(el) => props.measureRef?.(el)}
      class="den-list-header"
      role="row"
      aria-label={props.ariaLabel}
      data-testid={props.testId}
      style={{ "--den-list-cols": template() }}
      {...chromeProps()}
    >
      <For each={props.columns}>
        {(col, index) => (
          <div
            class="den-list-header__cell"
            classList={{
              "den-list-header__cell--end": col.align === "end",
              "den-list-header__cell--sorted": props.sort?.key === col.key,
            }}
            role="columnheader"
            aria-sort={columnSortable(col) ? sortAriaValue(props.sort, col.key) : undefined}
            data-column={col.key}
            data-optional={col.optional ? "true" : undefined}
          >
            <Show
              when={props.renderCell?.(col) == null && columnSortable(col) && !col.headerHidden}
              fallback={
                <Show
                  when={props.renderCell?.(col) == null}
                  fallback={props.renderCell?.(col)}
                >
                  <Show when={!col.headerHidden}>
                    <span class="den-list-header__label">{col.label}</span>
                  </Show>
                </Show>
              }
            >
              <button
                type="button"
                class="den-list-header__sort"
                data-testid={`list-sort-${col.key}`}
                onClick={() =>
                  props.onSort(
                    nextSortState(props.sort, col.key, columnFirstDirection(col)),
                  )
                }
              >
                <span class="den-list-header__label">{col.label}</span>
                <span class="den-list-header__glyph" aria-hidden="true">
                  {props.sort?.key === col.key
                    ? props.sort.dir === "asc"
                      ? "↑"
                      : "↓"
                    : ""}
                </span>
              </button>
            </Show>
            <Show when={resizable(col, index())}>
              <ResizeHandle
                class="den-list-header__grip"
                axis="width"
                size={() => widthOf(col)}
                clamp={(value) => clampColumnWidth(col, value)}
                onBegin={(_axis, startSize) =>
                  beginListColumnResize(
                    props.surface,
                    col.key,
                    startSize,
                    col.width,
                  )
                }
                rootClass="den-list--column-resizing"
                ariaLabel={`Resize ${col.label} column`}
                ariaMin={() => col.width.min}
                ariaMax={() => col.width.max}
                tip={`Drag to resize ${col.label}`}
              />
            </Show>
          </div>
        )}
      </For>
    </div>
  );
}
