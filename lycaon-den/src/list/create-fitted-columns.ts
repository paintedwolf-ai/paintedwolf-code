import { createMemo, createSignal, onCleanup, type Accessor } from "solid-js";
import { observeSharedContentBox } from "../layout/shared-resize-observer.ts";
import {
  LIST_COLUMN_GAP,
  columnGridTemplate,
  fitColumns,
  type ColumnSpec,
  type ColumnWidths,
} from "./list-columns.ts";

/**
 * Columns a list draws, fitted to the header's content box. The template and
 * the cells both read the result, so a shed column never keeps a track.
 */
export type FittedColumns<T> = {
  /** Ref for the element whose content box bounds the tracks. */
  measure: (el: HTMLElement | undefined) => void;
  columns: Accessor<readonly ColumnSpec<T>[]>;
  template: Accessor<string>;
  /** Whether a column survived the fit. Only optional columns can fail it. */
  shows: (key: string) => boolean;
};

export type FittedColumnsOptions<T> = {
  columns: () => readonly ColumnSpec<T>[];
  widths: () => ColumnWidths;
  gap?: number;
};

export function createFittedColumns<T>(
  options: FittedColumnsOptions<T>,
): FittedColumns<T> {
  const [available, setAvailable] = createSignal(Number.NaN);
  let release: (() => void) | undefined;

  const measure = (el: HTMLElement | undefined) => {
    release?.();
    release = undefined;
    if (!el) {
      // Keep the last width so a remounted header doesn't paint the full set first.
      return;
    }
    release = observeSharedContentBox(el, (box) => setAvailable(box.width));
  };
  onCleanup(() => release?.());

  const columns = createMemo(() =>
    fitColumns(options.columns(), options.widths(), available(), options.gap ?? LIST_COLUMN_GAP),
  );
  const shown = createMemo(() => new Set(columns().map((col) => col.key)));

  return {
    measure,
    columns,
    template: createMemo(() => columnGridTemplate(columns(), options.widths())),
    shows: (key: string) => shown().has(key),
  };
}
