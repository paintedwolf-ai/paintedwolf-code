import { describe, expect, it } from "vitest";
import {
  enclosingSymbolAt,
  resolveInlineEditScope,
  symbolSpans,
} from "./inline-edit-scope.ts";

describe("symbolSpans", () => {
  it("closes each symbol at the next start", () => {
    expect(
      symbolSpans(
        [
          { name: "A", line: 1 },
          { name: "B", line: 10 },
          { name: "C", line: 20 },
        ],
        30,
      ),
    ).toEqual([
      { name: "A", startLine: 1, endLine: 9 },
      { name: "B", startLine: 10, endLine: 19 },
      { name: "C", startLine: 20, endLine: 30 },
    ]);
  });
});

describe("enclosingSymbolAt", () => {
  const symbols = [
    { name: "outer", line: 1 },
    { name: "inner", line: 5 },
    { name: "tail", line: 20 },
  ];

  it("picks the innermost span", () => {
    expect(enclosingSymbolAt(symbols, 6, 30)).toEqual({
      name: "inner",
      startLine: 5,
      endLine: 19,
    });
  });

  it("returns null when caret is before any symbol", () => {
    expect(enclosingSymbolAt([{ name: "A", line: 5 }], 2, 10)).toBeNull();
  });
});

describe("resolveInlineEditScope", () => {
  it("uses a non-empty selection", () => {
    expect(
      resolveInlineEditScope({
        selection: { startLine: 3, endLine: 7, empty: false },
        caretLine: 99,
        docLines: 40,
        symbols: [{ name: "f", line: 1 }],
      }),
    ).toEqual({ startLine: 3, endLine: 7, kind: "selection" });
  });

  it("falls back to enclosing symbol when selection is empty", () => {
    expect(
      resolveInlineEditScope({
        selection: { startLine: 8, endLine: 8, empty: true },
        caretLine: 8,
        docLines: 40,
        symbols: [
          { name: "f", line: 1 },
          { name: "g", line: 20 },
        ],
      }),
    ).toEqual({
      startLine: 1,
      endLine: 19,
      kind: "symbol",
      symbolName: "f",
    });
  });

  it("uses ±20 lines at document edges", () => {
    expect(
      resolveInlineEditScope({
        selection: { startLine: 2, endLine: 2, empty: true },
        caretLine: 2,
        docLines: 10,
        symbols: [],
      }),
    ).toEqual({ startLine: 1, endLine: 10, kind: "window" });

    expect(
      resolveInlineEditScope({
        selection: { startLine: 50, endLine: 50, empty: true },
        caretLine: 50,
        docLines: 100,
        symbols: [],
      }),
    ).toEqual({ startLine: 30, endLine: 70, kind: "window" });
  });
});
