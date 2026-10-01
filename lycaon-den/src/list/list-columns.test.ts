import { describe, expect, it } from "vitest";
import {
  LIST_COLUMN_GAP,
  columnGridTemplate,
  columnTrackTotal,
  fitColumns,
  resolveColumnWidths,
  type ColumnSpec,
} from "./list-columns.ts";
import { LEDGER_COLUMNS, SECURITY_COLUMNS } from "../lib/scan-columns.ts";
import { SEARCH_COLUMNS } from "../search/search-columns.ts";

/** Every browse-stage column set. */
const SPECS: readonly { name: string; columns: readonly ColumnSpec<never>[] }[] = [
  { name: "security ledger", columns: LEDGER_COLUMNS as readonly ColumnSpec<never>[] },
  { name: "security runs", columns: SECURITY_COLUMNS as readonly ColumnSpec<never>[] },
  { name: "search", columns: SEARCH_COLUMNS as readonly ColumnSpec<never>[] },
];

/** The narrowest track area a browse stage gives a list. */
const NARROW = 560;

describe("column fitting", () => {
  // The fitted tracks must fit the scrollport.
  it.each(SPECS)("$name sheds until the tracks fit", ({ columns }) => {
    const widths = resolveColumnWidths(columns);
    for (let available = NARROW; available <= 1400; available += 8) {
      const fitted = fitColumns(columns, widths, available, LIST_COLUMN_GAP);
      const needed = columnTrackTotal(fitted, widths, LIST_COLUMN_GAP);
      const optionalLeft = fitted.some((col) => col.optional);
      // Fits, or only required columns remain and their tracks shrink.
      expect(needed <= available || !optionalLeft).toBe(true);
    }
  });

  it.each(SPECS)("$name keeps every required column at any width", ({ columns }) => {
    const widths = resolveColumnWidths(columns);
    const required = columns.filter((col) => !col.optional).map((col) => col.key);
    for (const available of [200, NARROW, 700, 1400]) {
      const kept = fitColumns(columns, widths, available, LIST_COLUMN_GAP).map((col) => col.key);
      expect(kept).toEqual(expect.arrayContaining(required));
    }
  });

  it.each(SPECS)("$name shows everything when there is room", ({ columns }) => {
    const widths = resolveColumnWidths(columns);
    const full = columnTrackTotal(columns, widths, LIST_COLUMN_GAP);
    expect(fitColumns(columns, widths, full, LIST_COLUMN_GAP)).toHaveLength(columns.length);
  });

  // Before measurement, every column shows.
  it.each(SPECS)("$name shows everything before it has been measured", ({ columns }) => {
    const widths = resolveColumnWidths(columns);
    for (const unmeasured of [Number.NaN, 0, -1]) {
      expect(fitColumns(columns, widths, unmeasured, LIST_COLUMN_GAP)).toHaveLength(columns.length);
    }
  });

  it("sheds the rightmost optional column first", () => {
    const columns: ColumnSpec<never>[] = [
      { key: "a", label: "A", width: { basis: 100, min: 100, max: 100 } },
      { key: "b", label: "B", width: { basis: 100, min: 100, max: 100 }, optional: true },
      { key: "c", label: "C", width: { basis: 100, min: 100, max: 100 } },
      { key: "d", label: "D", width: { basis: 100, min: 100, max: 100 }, optional: true },
    ];
    const widths = resolveColumnWidths(columns);
    // Three tracks plus two gaps.
    expect(fitColumns(columns, widths, 324, 12).map((col) => col.key)).toEqual(["a", "b", "c"]);
    expect(fitColumns(columns, widths, 212, 12).map((col) => col.key)).toEqual(["a", "c"]);
    // Nothing droppable is left; the required columns stay.
    expect(fitColumns(columns, widths, 50, 12).map((col) => col.key)).toEqual(["a", "c"]);
  });

  // Fixed tracks are ranges so they can shrink.
  it("makes every track a range so a tight row tightens instead of overflowing", () => {
    const widths = resolveColumnWidths(LEDGER_COLUMNS as readonly ColumnSpec<never>[]);
    const template = columnGridTemplate(LEDGER_COLUMNS as readonly ColumnSpec<never>[], widths);
    // Tracks carry a space inside minmax(), so count them rather than split.
    const tracks = template.match(/minmax\([^)]*\)/gu) ?? [];
    expect(tracks).toHaveLength(LEDGER_COLUMNS.length);
    expect(template.replace(/minmax\([^)]*\)/gu, "").trim()).toBe("");
    // The growing column keeps its own floor; the rest can reach zero.
    expect(template).toContain("minmax(160px, 1fr)");
    expect(template).toContain("minmax(0, 28px)");
  });
});
