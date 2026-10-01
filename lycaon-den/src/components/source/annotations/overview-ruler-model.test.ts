import { describe, expect, it } from "vitest";
import { search, SearchQuery, setSearchQuery } from "@codemirror/search";
import { EditorState, Text } from "@codemirror/state";
import {
  collectCurrentTicks,
  collectDiffTicks,
  collectFindMatchTicks,
  collectMatchTicks,
  collectOccurrenceMatchTicks,
  collectSymbolTicks,
  layoutOverviewTicks,
  overviewHasOverflow,
  overviewMatchKey,
  overviewHandleLength,
  overviewSearchKey,
  trackYForOffset,
} from "./overview-ruler-model.ts";

describe("overview ruler model", () => {
  it("maps the caret and only touched selection lines to current focus", () => {
    const caret = EditorState.create({
      doc: "a\nb\nc\n",
      selection: { anchor: 0 },
    });
    expect(collectCurrentTicks(caret)).toEqual([
      { kind: "current", fromLine: 1, toLine: 1 },
    ]);

    const selection = EditorState.create({
      doc: "a\nb\nc\n",
      selection: { anchor: 0, head: 4 },
    });
    expect(collectCurrentTicks(selection)).toEqual([
      { kind: "current", fromLine: 1, toLine: 2 },
    ]);
  });

  it("lists other whole-word occurrence lines without the caret target", () => {
    const state = EditorState.create({
      doc: "alpha\nbeta\nalpha\n",
      selection: { anchor: 0 },
    });
    expect(collectOccurrenceMatchTicks(state)).toEqual([
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
  });

  it("lists literal occurrences of selected text across the file", () => {
    const state = EditorState.create({
      doc: "cat\nconcatenate\ncat\n",
      selection: { anchor: 0, head: 3 },
    });
    expect(collectOccurrenceMatchTicks(state)).toEqual([
      { kind: "match", fromLine: 2, toLine: 2 },
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
  });

  it("uses Find as the match source and omits its current result", () => {
    const state = EditorState.create({
      doc: "foo\nbar\nfoo foo\nbar\n",
      selection: { anchor: 4, head: 7 },
      extensions: [search()],
    });
    const next = state.update({
      effects: setSearchQuery.of(new SearchQuery({ search: "foo", literal: true })),
    }).state;

    expect(overviewSearchKey(next)).toContain("foo");
    expect(overviewMatchKey(next)).toContain("find:");
    expect(collectFindMatchTicks(next)).toEqual([
      { kind: "match", fromLine: 1, toLine: 1 },
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
    expect(collectMatchTicks(next)).toEqual(collectFindMatchTicks(next));

    const active = next.update({ selection: { anchor: 0, head: 3 } }).state;
    expect(collectFindMatchTicks(active)).toEqual([
      { kind: "match", fromLine: 3, toLine: 3 },
    ]);
  });

  it("deduplicates symbols by line and gives every declaration one style", () => {
    expect(
      collectSymbolTicks([
        { line: 3 },
        { line: 3 },
        { line: 10 },
        { line: 20 },
        { line: 0 },
      ]),
    ).toEqual([
      { kind: "symbol", fromLine: 3, toLine: 3 },
      { kind: "symbol", fromLine: 10, toLine: 10 },
      { kind: "symbol", fromLine: 20, toLine: 20 },
    ]);
  });

  it("maps comparison chunks to added spans and deletion points", () => {
    // Lines: 1 "a", 2 "b", 3 "c", 4 "d" (offsets 0, 2, 4, 6; length 7).
    const doc = Text.of(["a", "b", "c", "d"]);
    expect(collectDiffTicks(null, doc)).toEqual([]);
    expect(collectDiffTicks([], doc)).toEqual([]);
    expect(
      collectDiffTicks(
        [
          // Lines 2-3 replaced one original line.
          { fromA: 2, toA: 4, fromB: 2, toB: 6 },
          // A pure deletion before line 4 holds no line on this side.
          { fromA: 8, toA: 10, fromB: 6, toB: 6 },
          // An append past the end of the file clamps to the last line.
          { fromA: 12, toA: 12, fromB: 6, toB: 9 },
        ],
        doc,
      ),
    ).toEqual([
      { kind: "add", fromLine: 2, toLine: 3 },
      { kind: "del", fromLine: 2, toLine: 2 },
      { kind: "del", fromLine: 4, toLine: 4 },
      { kind: "add", fromLine: 4, toLine: 4 },
    ]);
    expect(
      collectDiffTicks([{ fromA: 0, toA: 4, fromB: 0, toB: 0 }], Text.empty),
    ).toEqual([]);
  });

  it("sizes the handle from the viewport's share of the track and the theme minimum", () => {
    const base = { trackLength: 100, scrollHeight: 1000, clientHeight: 200 };
    expect(overviewHandleLength({ ...base, minHandleLength: 0 })).toBe(20);
    expect(overviewHandleLength({ ...base, minHandleLength: 40 })).toBe(40);
    expect(
      overviewHandleLength({ ...base, clientHeight: 1000, minHandleLength: 40 }),
    ).toBe(100);
    expect(
      overviewHandleLength({ ...base, scrollHeight: 0, minHandleLength: 40 }),
    ).toBe(100);
    expect(
      overviewHandleLength({ ...base, trackLength: 0, minHandleLength: 40 }),
    ).toBe(0);
  });

  it("places an offset where the handle's top edge rests when it heads the viewport", () => {
    // A proportional handle: 1000px of content on a 100px track.
    const proportional = {
      trackLength: 100,
      handleLength: 20,
      scrollHeight: 1000,
      clientHeight: 200,
    };
    expect(trackYForOffset(0, proportional)).toBe(0);
    expect(trackYForOffset(500, proportional)).toBe(50);
    expect(trackYForOffset(800, proportional)).toBe(80);
    expect(trackYForOffset(1000, proportional)).toBe(100);
    expect(trackYForOffset(-5, proportional)).toBe(0);
    expect(trackYForOffset(5000, proportional)).toBe(100);

    // The final line ends at the handle's leading edge.
    const pastEnd = {
      trackLength: 100,
      handleLength: 50,
      scrollHeight: 140,
      clientHeight: 70,
    };
    expect(trackYForOffset(70, pastEnd)).toBe(50);
    expect(trackYForOffset(140, pastEnd)).toBe(100);

    // Minimum handle size preserves the final scroll destination.
    const minimum = {
      trackLength: 100,
      handleLength: 40,
      scrollHeight: 10_000,
      clientHeight: 100,
    };
    expect(trackYForOffset(9900, minimum)).toBe(60);
    expect(trackYForOffset(9950, minimum)).toBe(80);
    expect(trackYForOffset(10_000, minimum)).toBe(100);

    // Nothing to scroll falls back to a plain proportion.
    expect(
      trackYForOffset(50, {
        trackLength: 100,
        handleLength: 100,
        scrollHeight: 100,
        clientHeight: 100,
      }),
    ).toBe(50);
  });

  it("paints ticks over their line spans, coalesces adjacent symbol pixels, and paints focus last", () => {
    const spans = layoutOverviewTicks(
      [
        { kind: "symbol", fromLine: 1, toLine: 1 },
        { kind: "current", fromLine: 3, toLine: 5 },
      ],
      {
        height: 100,
        spanForLine: (line) => ({ top: (line - 1) * 10, bottom: line * 10 }),
      },
    );
    expect(spans).toEqual([
      { kind: "symbol", y: 0, h: 10 },
      { kind: "current", y: 20, h: 30 },
    ]);

    // A tick on a long file keeps its minimum height and stays on the track.
    const floor = layoutOverviewTicks(
      [{ kind: "current", fromLine: 1000, toLine: 1000 }],
      {
        height: 100,
        spanForLine: () => ({ top: 99.9, bottom: 100 }),
      },
    );
    expect(floor).toEqual([{ kind: "current", y: 97, h: 3 }]);

    const ops = layoutOverviewTicks(
      [
        { kind: "symbol", fromLine: 1, toLine: 1 },
        { kind: "symbol", fromLine: 2, toLine: 2 },
        { kind: "symbol", fromLine: 3, toLine: 3 },
        { kind: "match", fromLine: 4, toLine: 4 },
        { kind: "match", fromLine: 4, toLine: 4 },
        { kind: "current", fromLine: 6, toLine: 8 },
        { kind: "add", fromLine: 9, toLine: 11 },
        { kind: "del", fromLine: 9, toLine: 9 },
      ],
      {
        height: 20,
        spanForLine: (line) => ({ top: line - 1, bottom: line }),
      },
    );

    expect(ops.filter((op) => op.kind === "symbol")).toHaveLength(2);
    expect(ops.filter((op) => op.kind === "match")).toHaveLength(1);
    const kinds = ops.map((op) => op.kind);
    expect(kinds.indexOf("add")).toBeLessThan(kinds.indexOf("del"));
    expect(kinds.indexOf("del")).toBeLessThan(kinds.indexOf("match"));
    expect(ops[ops.length - 1]).toEqual({ kind: "current", y: 5, h: 3 });
  });

  it("hides the strip when the buffer fits the viewport or nothing is laid out", () => {
    expect(overviewHasOverflow(400, 200)).toBe(true);
    expect(overviewHasOverflow(200, 400)).toBe(false);
    expect(overviewHasOverflow(201, 200)).toBe(false);
    expect(overviewHasOverflow(80, 0)).toBe(false);
    expect(overviewHasOverflow(0, 0)).toBe(false);
  });
});
