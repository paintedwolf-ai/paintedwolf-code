import { untrack } from "solid-js";
import type { DenListPanePrefs, DenListPanesPrefs } from "../../shared/app-state-types.ts";
import { clampListPaneSizePx, resolveListPanePrefs, type ListPaneAxis } from "./list-pane-model.ts";
import { clampColumnWidthPx, type ColumnWidth } from "./list-columns.ts";
import type { SortState } from "./list-sort.ts";
import type { ResizeSession } from "../layout/resize-session.ts";
import { PANE_HIDDEN_PX } from "../layout/drag-to-hide.ts";
import { currentPreferences, commitPreferences } from "../layout/layout-preferences.ts";
import { columnResizePreview, resizePreview, beginResize } from "../layout/resize-transaction.ts";

function baseListPanePrefs(surface: string): DenListPanePrefs {
  return resolveListPanePrefs(currentPreferences(), surface);
}

export function listPanePrefs(surface: string): DenListPanePrefs {
  const base = baseListPanePrefs(surface);
  const width = resizePreview(`pane:${surface}:width`);
  const height = resizePreview(`pane:${surface}:height`);
  const column = columnResizePreview(surface);
  if (width != null) {
    return width === PANE_HIDDEN_PX ? base : { ...base, widthPx: width };
  }
  if (height != null) {
    return height === PANE_HIDDEN_PX ? base : { ...base, heightPx: height };
  }
  if (column) {
    return {
      ...base,
      columnWidths: {
        ...(base.columnWidths ?? {}),
        [column.column]: column.width,
      },
    };
  }
  return base;
}

async function writeListPane(
  surface: string,
  pane: DenListPanePrefs,
): Promise<void> {
  const next = { ...currentPreferences() };
  const listPanes: DenListPanesPrefs = { ...(next.listPanes ?? {}) };
  if (Object.keys(pane).length > 0) listPanes[surface] = pane;
  else delete listPanes[surface];
  if (Object.keys(listPanes).length > 0) next.listPanes = listPanes;
  else delete next.listPanes;
  await commitPreferences(next);
}

/** A pane drag has passed the point where releasing hides the pane. */
export function listPaneHidePreviewed(
  surface: string,
  axis: ListPaneAxis,
): boolean {
  return resizePreview(`pane:${surface}:${axis}`) === PANE_HIDDEN_PX;
}

/** With `onHide`, a drag may preview and commit {@link PANE_HIDDEN_PX}. */
export function beginListPaneResize(
  surface: string,
  axis: ListPaneAxis,
  effectivePx: number,
  bodySize?: number,
  onHide?: () => void,
): ResizeSession {
  const normalize = (value: number) =>
    onHide && value === PANE_HIDDEN_PX
      ? value
      : clampListPaneSizePx(surface, axis, value, bodySize);
  return beginResize(
    `pane:${surface}:${axis}`,
    effectivePx,
    normalize,
    0.5,
    (value) => {
      untrack(() => {
        if (value === PANE_HIDDEN_PX) {
          onHide?.();
          return;
        }
        const pane = baseListPanePrefs(surface);
        void writeListPane(
          surface,
          axis === "width"
            ? { ...pane, widthPx: value }
            : { ...pane, heightPx: value },
        );
      });
    },
  );
}

export function beginListColumnResize(
  surface: string,
  column: string,
  effectivePx: number,
  width: ColumnWidth,
): ResizeSession {
  return beginResize(
    `column:${surface}:${column}`,
    effectivePx,
    (value) => clampColumnWidthPx(width, value),
    0.5,
    (value) => {
      untrack(() => {
        const pane = baseListPanePrefs(surface);
        void writeListPane(surface, {
          ...pane,
          columnWidths: { ...(pane.columnWidths ?? {}), [column]: value },
        });
      });
    },
  );
}

export async function resetListPaneSize(
  surface: string,
  axis: ListPaneAxis,
): Promise<void> {
  const pane = { ...baseListPanePrefs(surface) };
  if (axis === "width") delete pane.widthPx;
  else delete pane.heightPx;
  await writeListPane(surface, pane);
}

export async function commitListSort(
  surface: string,
  state: SortState,
): Promise<void> {
  const pane = { ...baseListPanePrefs(surface) };
  if (state) {
    pane.sortKey = state.key;
    pane.sortDir = state.dir;
  } else {
    delete pane.sortKey;
    delete pane.sortDir;
  }
  await writeListPane(surface, pane);
}

