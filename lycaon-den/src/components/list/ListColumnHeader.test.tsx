import { beforeEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render } from "@solidjs/testing-library";
import { ListColumnHeader } from "./ListColumnHeader.tsx";
import type { ColumnSpec } from "../../list/list-columns.ts";
import { compareStrings, type SortState } from "../../list/list-sort.ts";
import {
  listPanePrefs,
  resetLayoutStoreForTests,
} from "../../shell/layout-store.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";

type Row = { name: string };

const COLUMNS: readonly ColumnSpec<Row>[] = [
  {
    key: "name",
    label: "Name",
    width: { basis: 120, min: 60, max: 240 },
    compare: (a, b, dir) => compareStrings(a.name, b.name, dir),
  },
  {
    key: "body",
    label: "Body",
    width: { basis: 200, min: 120, max: 400, grow: true },
  },
  {
    key: "extra",
    label: "Extra",
    width: { basis: 90, min: 60, max: 180 },
    optional: true,
  },
];

const WIDTHS = { name: 120, body: 200, extra: 90 };

function renderHeader(initial: SortState = null) {
  const [sort, setSort] = createSignal<SortState>(initial);
  const widths = () => ({
    ...WIDTHS,
    ...(listPanePrefs("test-list").columnWidths ?? {}),
  });
  const onSort = vi.fn((next: SortState) => setSort(next));
  const view = render(() => (
    <ListColumnHeader
      surface="test-list"
      columns={COLUMNS}
      widths={widths()}
      sort={sort()}
      onSort={onSort}
      testId="hdr"
    />
  ));
  return { ...view, sort, widths, onSort };
}

describe("ListColumnHeader", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests();
    resetLayoutStoreForTests();
  });
  // Fixed tracks are minmax(0, width).
  it("lays the header on the shared column template", () => {
    const { getByTestId } = renderHeader();
    expect(getByTestId("hdr").getAttribute("style")).toContain(
      "minmax(0, 120px) minmax(120px, 1fr) minmax(0, 90px)",
    );
  });

  it("advances aria-sort through asc, desc, then back to default", () => {
    const { getByTestId, container, sort } = renderHeader();
    const cell = () =>
      container.querySelector('[data-column="name"]')?.getAttribute("aria-sort");
    expect(cell()).toBe("none");
    fireEvent.click(getByTestId("list-sort-name"));
    expect(cell()).toBe("ascending");
    fireEvent.click(getByTestId("list-sort-name"));
    expect(cell()).toBe("descending");
    fireEvent.click(getByTestId("list-sort-name"));
    expect(cell()).toBe("none");
    expect(sort()).toBeNull();
  });

  it("renders no sort control for a column with no comparator", () => {
    const { queryByTestId, container } = renderHeader();
    expect(queryByTestId("list-sort-body")).toBeNull();
    expect(
      container.querySelector('[data-column="body"]')?.getAttribute("aria-sort"),
    ).toBeNull();
  });

  it("marks optional columns so the stylesheet can shed them when narrow", () => {
    const { container } = renderHeader();
    expect(
      container.querySelector('[data-column="extra"]')?.getAttribute("data-optional"),
    ).toBe("true");
    expect(
      container.querySelector('[data-column="name"]')?.getAttribute("data-optional"),
    ).toBeNull();
  });

  it("gives every column but the grow one and the last a resize grip", () => {
    const { container } = renderHeader();
    const gripIn = (key: string) =>
      Boolean(
        container.querySelector(`[data-column="${key}"] .den-list-header__grip`),
      );
    expect(gripIn("name")).toBe(true);
    expect(gripIn("body")).toBe(false);
    expect(gripIn("extra")).toBe(false);
  });

  it("commits a column width from the grip's keyboard gesture", () => {
    const { container, widths } = renderHeader();
    const grip = container.querySelector(
      '[data-column="name"] .den-list-header__grip',
    ) as HTMLElement;
    fireEvent.keyDown(grip, { key: "ArrowRight" });
    expect(widths().name).toBe(136);
  });

  it("accumulates repeated keyboard resizes and clamps at the column floor", () => {
    const { container, widths, getByTestId } = renderHeader();
    const grip = container.querySelector(
      '[data-column="name"] .den-list-header__grip',
    ) as HTMLElement;
    fireEvent.keyDown(grip, { key: "ArrowLeft" });
    fireEvent.keyDown(grip, { key: "ArrowLeft" });
    expect(widths().name).toBe(88);
    for (let i = 0; i < 20; i += 1) fireEvent.keyDown(grip, { key: "ArrowLeft" });
    expect(widths().name).toBe(60);
    // The template follows the accumulated width, so rows stay in step.
    expect(getByTestId("hdr").getAttribute("style")).toContain("60px");
  });
});
