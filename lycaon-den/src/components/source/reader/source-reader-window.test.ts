import { expect, it } from "vitest";
import { comparisonWindow } from "./source-reader-window.ts";
import type { SourceComparisonFrame } from "../../../api/types.ts";

const frame = (start: number, source: number, count = 200): SourceComparisonFrame => ({ kind: "comparison", view_id: "view",
  intent_revision: "intent", projection_revision: "projection", extent: { rows: 10_000_000, complete: true },
  span: { start, end: start + count }, anchor: { row: source }, rows: Array.from({ length: count }, (_, index) => ({
    index: source + index, end: source + index + 1, kind: "equal", text: "line\n", before_line: source + index + 1,
    after_line: source + index + 1, changed: [],
  })) });

it("reserves distant display ranks with bounded geometry and source retention", () => {
  const slots = comparisonWindow([frame(8_000_000, 8_500_000)], 11_000_000, 20);
  expect(slots).toHaveLength(202);
  expect(slots[0]).toMatchObject({ pending: true, index: 0, end: 8_500_000, displayIndex: 0, displayEnd: 8_000_000 });
  expect(slots.at(-1)).toMatchObject({ pending: true, displayIndex: 8_000_200, displayEnd: 10_000_000, end: 11_000_000 });
  expect(slots.reduce((sum, slot) => sum + (slot.pending ? slot.lines ?? 1 : 1) * 20, 0)).toBeLessThan(2_010_000);
});

it("counts an unchanged fold as one display row", () => {
  const page = frame(0, 0, 2);
  page.rows[0] = { ...page.rows[0]!, kind: "gap", text: "", end: 50_000 };
  page.rows[1] = { ...page.rows[1]!, index: 50_000, end: 50_001 };
  const slots = comparisonWindow([page], 11_000_000, 20);
  expect(slots[0]).toMatchObject({ index: 0, end: 50_000, displayIndex: 0, displayEnd: 1 });
  expect(slots[1]).toMatchObject({ index: 50_000, displayIndex: 1, displayEnd: 2 });
});

it("preserves total extent when distant pages arrive out of order", () => {
  const last = { ...frame(900, 900, 100), extent: { rows: 1000, complete: true } };
  const middle = { ...frame(500, 500, 100), extent: last.extent };
  const initial = comparisonWindow([last], 1000, 20);
  const loaded = comparisonWindow([last, middle], 1000, 20);
  const extent = (slots: typeof loaded) => slots.reduce((sum, slot) => sum + (slot.pending ? slot.lines ?? 1 : 1), 0);
  expect(extent(initial)).toBe(1000);
  expect(extent(loaded)).toBe(1000);
  expect(loaded[0]).toMatchObject({ index: 0, end: 500, lines: 500, pending: true });
  expect(loaded[101]).toMatchObject({ index: 600, end: 900, lines: 300, pending: true });
  expect(loaded.at(-1)).toMatchObject({ index: 999, end: 1000 });
});

it("keeps compressed geometry stable as pages arrive and leave the viewport", () => {
  const page = frame(8_000_000, 8_000_000);
  const neighboring = frame(8_000_200, 8_000_200);
  const overlapping = frame(8_000_100, 8_000_100);
  const tail = frame(9_999_900, 9_999_900, 100);
  for (const pages of [[page], [page, neighboring], [neighboring, overlapping, page], [tail]]) {
    const slots = comparisonWindow(pages, 10_000_000, 20);
    const height = slots.reduce((sum, slot) => sum + (slot.pending ? slot.lines ?? 1 : 1) * 20, 0);
    expect(height).toBeCloseTo(2_000_000, 5);
    expect(slots.reduce((sum, slot) => sum + slot.displayEnd! - slot.displayIndex!, 0)).toBe(10_000_000);
  }
});

it("counts a missing range once when the host returns an empty boundary frame", () => {
  const initial = { ...frame(0, 0), extent: { rows: 1000, complete: true } };
  const boundary = { ...frame(1000, 1000, 0), extent: initial.extent };
  const slots = comparisonWindow([initial, boundary], 1000, 20);
  expect(slots.filter(slot => slot.pending)).toHaveLength(1);
  expect(slots.at(-1)).toMatchObject({ displayIndex: 200, displayEnd: 1000, lines: 800 });
});

it.each([
  { view_id: "other" }, { intent_revision: "other" }, { projection_revision: "other" },
  { extent: { rows: 9_000_000, complete: true } },
])("rejects pages from different snapshots: %j", changed => {
  expect(() => comparisonWindow([frame(0, 0), { ...frame(200, 200), ...changed }], 10_000_000, 20))
    .toThrow("different presentations");
});
