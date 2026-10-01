import { describe, expect, it } from "vitest";
import {
  compareNumbers,
  compareStrings,
  nextSortState,
  sortAriaValue,
  toSortState,
} from "./list-sort.ts";
import { sortRows, type ColumnSpec } from "./list-columns.ts";

type Row = { name: string; rank: number };

const COLUMNS: readonly ColumnSpec<Row>[] = [
  {
    key: "name",
    label: "Name",
    width: { basis: 100, min: 40, max: 200 },
    compare: (a, b, dir) => compareStrings(a.name, b.name, dir),
  },
  {
    key: "rank",
    label: "Rank",
    width: { basis: 60, min: 40, max: 120 },
    firstDirection: "desc",
    compare: (a, b, dir) => compareNumbers(a.rank, b.rank, dir),
  },
  { key: "note", label: "Note", width: { basis: 60, min: 40, max: 120 } },
];

describe("nextSortState", () => {
  it("starts a cold column at its own first direction", () => {
    expect(nextSortState(null, "name", "asc")).toEqual({ key: "name", dir: "asc" });
    expect(nextSortState(null, "rank", "desc")).toEqual({ key: "rank", dir: "desc" });
  });

  it("cycles asc → desc → default on the active column", () => {
    const one = nextSortState(null, "name", "asc");
    const two = nextSortState(one, "name", "asc");
    const three = nextSortState(two, "name", "asc");
    expect(one).toEqual({ key: "name", dir: "asc" });
    expect(two).toEqual({ key: "name", dir: "desc" });
    expect(three).toBeNull();
  });

  it("cycles desc → asc → default when the column leads with desc", () => {
    const one = nextSortState(null, "rank", "desc");
    const two = nextSortState(one, "rank", "desc");
    expect(two).toEqual({ key: "rank", dir: "asc" });
    expect(nextSortState(two, "rank", "desc")).toBeNull();
  });

  it("restarts fresh when a different column is clicked", () => {
    const active = { key: "name", dir: "desc" } as const;
    expect(nextSortState(active, "rank", "desc")).toEqual({
      key: "rank",
      dir: "desc",
    });
  });
});

describe("sortAriaValue", () => {
  it("reports none for every column but the active one", () => {
    const state = { key: "name", dir: "asc" } as const;
    expect(sortAriaValue(state, "name")).toBe("ascending");
    expect(sortAriaValue(state, "rank")).toBe("none");
    expect(sortAriaValue(null, "name")).toBe("none");
    expect(sortAriaValue({ key: "name", dir: "desc" }, "name")).toBe("descending");
  });
});

describe("toSortState", () => {
  it("rejects a missing key or an unknown direction", () => {
    expect(toSortState(undefined, "asc")).toBeNull();
    expect(toSortState("  ", "asc")).toBeNull();
    expect(toSortState("name", undefined)).toBeNull();
    expect(toSortState("name", "sideways")).toBeNull();
    expect(toSortState(" name ", "desc")).toEqual({ key: "name", dir: "desc" });
  });
});

describe("sortRows", () => {
  const rows: Row[] = [
    { name: "beta", rank: 2 },
    { name: "alpha", rank: 2 },
    { name: "gamma", rank: 1 },
  ];

  it("returns the incoming order untouched for the default state", () => {
    expect(sortRows(rows, COLUMNS, null).map((r) => r.name)).toEqual([
      "beta",
      "alpha",
      "gamma",
    ]);
  });

  it("sorts by the active column in both directions", () => {
    expect(
      sortRows(rows, COLUMNS, { key: "name", dir: "asc" }).map((r) => r.name),
    ).toEqual(["alpha", "beta", "gamma"]);
    expect(
      sortRows(rows, COLUMNS, { key: "name", dir: "desc" }).map((r) => r.name),
    ).toEqual(["gamma", "beta", "alpha"]);
  });

  it("is stable — equal rows keep their incoming order", () => {
    expect(
      sortRows(rows, COLUMNS, { key: "rank", dir: "desc" }).map((r) => r.name),
    ).toEqual(["beta", "alpha", "gamma"]);
  });

  it("leaves rows alone for an unsortable or unknown column", () => {
    expect(
      sortRows(rows, COLUMNS, { key: "note", dir: "asc" }).map((r) => r.name),
    ).toEqual(["beta", "alpha", "gamma"]);
    expect(
      sortRows(rows, COLUMNS, { key: "nope", dir: "asc" }).map((r) => r.name),
    ).toEqual(["beta", "alpha", "gamma"]);
  });

  it("does not mutate the input", () => {
    const input = [...rows];
    sortRows(input, COLUMNS, { key: "name", dir: "asc" });
    expect(input.map((r) => r.name)).toEqual(["beta", "alpha", "gamma"]);
  });
});
