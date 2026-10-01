import { expect, it } from "vitest";
import { ReaderDocument, readerGap, type ReaderSlot, MAIN_SECTION } from "./source-reader-document.ts";

const line = (index: number, text: string, extra: Partial<ReaderSlot> = {}): ReaderSlot => ({ index, end: index + 1, text, kind: "equal", before_line: index + 1, after_line: index + 1, changed: [], ...extra });

it("keeps original coordinates when most of the source is absent from the editor", () => {
  const document = new ReaderDocument([readerGap(0, 99999), line(99999, "requested\n"), readerGap(100000, 1000000)]);
  expect(document.text.length).toBeLessThan(20);
  const entry = document.rowAt(MAIN_SECTION, 99999)!;
  expect(document.boundary(entry.from + 2, 1)).toEqual({ section: MAIN_SECTION, row: 99999, offset: 2, side: "after" });
  expect(document.at(entry.from)?.row?.after_line).toBe(100000);
});

it("stitches long-line fragments without introducing source newlines", () => {
  const document = new ReaderDocument([line(0, "🙂"), line(1, "tail\n", { before_line: 1, after_line: 1, column: 2 }), line(2, "next\n")]);
  expect(document.text).toBe("🙂tail\nnext\n");
  expect(document.boundary(3, 1)).toEqual({ section: MAIN_SECTION, row: 1, offset: 1, side: "after" });
  expect(document.boundary(2, -1)).toEqual({ section: MAIN_SECTION, row: 0, offset: 2, side: "after" });
});

it("separates an unterminated deletion from its replacement", () => {
  const document = new ReaderDocument([line(0, "old", {kind: "delete", after_line: 0}), line(1, "new", {kind: "insert", before_line: 0})]);
  expect(document.text).toBe("old\nnew");
  expect(document.boundary(3, -1)).toEqual({ section: MAIN_SECTION, row: 0, offset: 3, side: "before" });
  expect(document.boundary(4, 1)).toEqual({ section: MAIN_SECTION, row: 1, offset: 0, side: "after" });
});

it("maps both panes of a paired change to their original rows", () => {
  const peer = line(1, "new\n", { kind: "insert", before_line: 0, after_line: 1 });
  const rows = [line(0, "old\n", { kind: "delete", after_line: 0, peer })];
  const before = new ReaderDocument(rows, "before"), after = new ReaderDocument(rows, "after");
  expect(before.text).toBe("old\n"); expect(after.text).toBe("new\n");
  expect(before.boundary(2, 1)).toEqual({ section: MAIN_SECTION, row: 0, offset: 2, side: "before" });
  expect(after.boundary(2, 1)).toEqual({ section: MAIN_SECTION, row: 1, offset: 2, side: "after" });
});

it("visits only entries intersecting a viewport", () => {
  const document = new ReaderDocument(Array.from({ length: 1000 }, (_, index) => line(index, "line\n")));
  expect([...document.entriesBetween(4501, 4514)].map(entry => entry.slot.index)).toEqual([900, 901, 902]);
});
