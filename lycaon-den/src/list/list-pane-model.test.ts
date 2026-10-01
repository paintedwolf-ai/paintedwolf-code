import { describe, expect, it } from "vitest";
import {
  FILES_TREE_WIDTH_DEFAULT_PX,
  FILES_TREE_WIDTH_MIN_PX,
  LIST_PANE_HEIGHT_DEFAULT_PX,
  LIST_PANE_HEIGHT_MAX_PX,
  LIST_PANE_HEIGHT_MIN_PX,
  LIST_PANE_WIDTH_DEFAULT_PX,
  LIST_PANE_WIDTH_MAX_PX,
  LIST_PANE_WIDTH_MIN_PX,
} from "../../shared/app-state-types.ts";
import {
  clampListPanePrefs,
  clampListPaneSizePx,
  FILES_TREE_SURFACE,
  listPaneSizeMinPx,
  resolveListPanePrefs,
  resolveListPaneSizePx,
  resolveListPaneSort,
} from "./list-pane-model.ts";
import type { ColumnSpec } from "./list-columns.ts";

const COLUMNS: readonly ColumnSpec<unknown>[] = [
  { key: "a", label: "A", width: { basis: 100, min: 60, max: 200 } },
  { key: "b", label: "B", width: { basis: 100, min: 60, max: 200 } },
];

describe("pane size clamping", () => {
  it("holds each axis inside its own floor and ceiling", () => {
    expect(clampListPaneSizePx("search", "width", 9999)).toBe(
      LIST_PANE_WIDTH_MAX_PX,
    );
    expect(clampListPaneSizePx("search", "width", 1)).toBe(
      LIST_PANE_WIDTH_MIN_PX,
    );
    expect(clampListPaneSizePx("search", "height", 9999)).toBe(
      LIST_PANE_HEIGHT_MAX_PX,
    );
    expect(clampListPaneSizePx("search", "height", 1)).toBe(
      LIST_PANE_HEIGHT_MIN_PX,
    );
  });

  it("falls back to the default for a non-finite value", () => {
    expect(clampListPaneSizePx("search", "width", Number.NaN)).toBe(
      LIST_PANE_WIDTH_DEFAULT_PX,
    );
    expect(
      clampListPaneSizePx("search", "height", Number.POSITIVE_INFINITY),
    ).toBe(LIST_PANE_HEIGHT_DEFAULT_PX);
  });

  it("caps the pane at 60% of the measured split so the list cannot starve", () => {
    // 60% of 800 is 480, below the stored 700.
    expect(clampListPaneSizePx("search", "width", 700, 800)).toBe(480);
    // Never below the floor, however narrow the split gets.
    expect(clampListPaneSizePx("search", "width", 700, 200)).toBe(
      LIST_PANE_WIDTH_MIN_PX,
    );
  });
});

describe("per-surface bounds", () => {
  it("lets the Files tree go narrower than a main-detail pane", () => {
    expect(FILES_TREE_WIDTH_MIN_PX).toBe(180);
    expect(FILES_TREE_WIDTH_MIN_PX).toBeLessThan(LIST_PANE_WIDTH_MIN_PX);
    expect(listPaneSizeMinPx(FILES_TREE_SURFACE, "width")).toBe(
      FILES_TREE_WIDTH_MIN_PX,
    );
    expect(clampListPaneSizePx(FILES_TREE_SURFACE, "width", 200)).toBe(200);
    expect(clampListPaneSizePx(FILES_TREE_SURFACE, "width", 1)).toBe(
      FILES_TREE_WIDTH_MIN_PX,
    );
  });

  it("resolves an unsized Files tree to the width the stylesheet renders", () => {
    expect(resolveListPaneSizePx(FILES_TREE_SURFACE, {}, "width")).toBe(
      FILES_TREE_WIDTH_DEFAULT_PX,
    );
  });

  it("leaves surfaces without an override on the shared bounds", () => {
    expect(listPaneSizeMinPx("search", "width")).toBe(LIST_PANE_WIDTH_MIN_PX);
    expect(listPaneSizeMinPx(FILES_TREE_SURFACE, "height")).toBe(
      LIST_PANE_HEIGHT_MIN_PX,
    );
  });

  it("sanitizes a stored size against the surface that controls it", () => {
    // The same 200px entry is legal for the tree and too narrow for a list.
    expect(clampListPanePrefs(FILES_TREE_SURFACE, { widthPx: 200 }).widthPx).toBe(
      200,
    );
    expect(clampListPanePrefs("search", { widthPx: 200 }).widthPx).toBe(
      LIST_PANE_WIDTH_MIN_PX,
    );
  });
});

describe("clampListPanePrefs", () => {
  it("keeps width and height independent", () => {
    const pane = clampListPanePrefs("search", { widthPx: 500, heightPx: 9999 });
    expect(pane.widthPx).toBe(500);
    expect(pane.heightPx).toBe(LIST_PANE_HEIGHT_MAX_PX);
  });

  it("omits an axis that was never sized", () => {
    const pane = clampListPanePrefs("search", { widthPx: 400 });
    expect(pane.widthPx).toBe(400);
    expect("heightPx" in pane).toBe(false);
  });

  it("drops an invalid sort rather than persisting half of one", () => {
    const pane = clampListPanePrefs("search", {
      sortKey: "a",
      sortDir: "sideways" as never,
    });
    expect(pane.sortKey).toBeUndefined();
    expect(pane.sortDir).toBeUndefined();
  });

  it("prunes column widths for keys the spec no longer has", () => {
    const pane = clampListPanePrefs(
      "search",
      { columnWidths: { a: 120, retired: 90 } },
      COLUMNS,
    );
    expect(pane.columnWidths).toEqual({ a: 120 });
  });
});

describe("resolve helpers", () => {
  it("keys panes by surface", () => {
    const shell = {
      listPanes: {
        search: { widthPx: 400 },
        security: { widthPx: 300, sortKey: "severity", sortDir: "desc" as const },
      },
    };
    expect(resolveListPanePrefs(shell, "search").widthPx).toBe(400);
    expect(resolveListPanePrefs(shell, "security").widthPx).toBe(300);
    expect(resolveListPanePrefs(shell, "absent")).toEqual({});
  });

  it("falls back to the axis default when that axis was never sized", () => {
    expect(resolveListPaneSizePx("search", { widthPx: 420 }, "width")).toBe(420);
    expect(resolveListPaneSizePx("search", { widthPx: 420 }, "height")).toBe(
      LIST_PANE_HEIGHT_DEFAULT_PX,
    );
  });

  it("round-trips a persisted sort", () => {
    expect(resolveListPaneSort({ sortKey: "severity", sortDir: "desc" })).toEqual({
      key: "severity",
      dir: "desc",
    });
    expect(resolveListPaneSort({})).toBeNull();
  });
});
