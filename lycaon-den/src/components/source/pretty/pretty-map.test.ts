import { describe, expect, it } from "vitest";
import {
  createRuneOffsetTable,
  displayOffsetToSourceLineCol,
  displayToSourceOffset,
  mapRuneSpansToDisplay,
  mapSourceUtf16RangeToDisplay,
  sourceLineColToDisplay,
  sourceToDisplayOffset,
} from "./pretty-map.ts";
import { formatPrettyProjection } from "./pretty-segmenter.ts";

describe("pretty-map (FMT-AC-05, FMT-AC-13)", () => {
  it("FMT-AC-05: emits exact runs, whitespace intervals, and insertOffsets matching design packet Case 1", () => {
    const raw = '{"a":1,"b":1.20e+3}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "case1.json",
      sourceRevision: 1,
      path: "case1.json",
    });

    const map = res.sourceMap;
    expect(map.runs.slice(0, 6)).toEqual([
      {
        kind: "identity",
        dispFrom: 0,
        dispTo: 1,
        srcFrom: 0,
        srcTo: 1,
        insertOffset: 0,
        segmentIndex: 0,
      },
      {
        kind: "virtual-layout",
        dispFrom: 1,
        dispTo: 4,
        srcFrom: 1,
        srcTo: 1,
        insertOffset: 1,
        segmentIndex: 0,
      },
      {
        kind: "identity",
        dispFrom: 4,
        dispTo: 7,
        srcFrom: 1,
        srcTo: 4,
        insertOffset: 1,
        segmentIndex: 0,
      },
      {
        kind: "identity",
        dispFrom: 7,
        dispTo: 8,
        srcFrom: 4,
        srcTo: 5,
        insertOffset: 4,
        segmentIndex: 0,
      },
      {
        kind: "virtual-layout",
        dispFrom: 8,
        dispTo: 9,
        srcFrom: 5,
        srcTo: 5,
        insertOffset: 5,
        segmentIndex: 0,
      },
      {
        kind: "identity",
        dispFrom: 9,
        dispTo: 10,
        srcFrom: 5,
        srcTo: 6,
        insertOffset: 5,
        segmentIndex: 0,
      },
    ]);

    // Closing virtual newline before `}` has insertOffset = srcFrom (raw.length - 1 = 18).
    const closingVirtual = map.runs[map.runs.length - 2]!;
    expect(closingVirtual.kind).toBe("virtual-layout");
    expect(closingVirtual.insertOffset).toBe(raw.length - 1);
    expect(closingVirtual.insertOffset).toBe(18);
  });

  it("FMT-AC-05: round-trips every retained token character and resolves virtual-layout biases deterministically", () => {
    const raw = '{"a": 1,   "b": 2}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "case2.json",
      sourceRevision: 1,
    });
    const map = res.sourceMap;

    // Every character inside an identity run round-trips exactly.
    for (const run of map.runs) {
      if (run.kind !== "identity") continue;
      for (let d = run.dispFrom; d < run.dispTo; d++) {
        const s = displayToSourceOffset(map, d, "left");
        expect(s).toBe(run.srcFrom + (d - run.dispFrom));
        expect(res.displayText[d]).toBe(raw[s]);
      }
    }

    // Virtual layout `\n  ` after `,` replaces source whitespace `src [8, 11)` at `disp [11, 14)`.
    expect(displayToSourceOffset(map, 12, "left")).toBe(8);
    expect(displayToSourceOffset(map, 12, "right")).toBe(11);
    expect(displayToSourceOffset(map, 12, "insert")).toBe(11);

    // Source offset 9 (inside removed `   ` whitespace) maps to disp 11 (left) and 14 (right).
    expect(sourceToDisplayOffset(map, 9, "left")).toBe(11);
    expect(sourceToDisplayOffset(map, 9, "right")).toBe(14);
  });

  it("FMT-AC-13: tracks sourceLine and isContinuation per display line and converts 1-based line+col with astral runes", () => {
    const raw = '{"emoji":"🦊🌟","val":42}\n{"second":true}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "lines.jsonl",
      sourceRevision: 2,
      path: "lines.jsonl",
    });
    const map = res.sourceMap;

    // Record 1 expands to 4 display lines (sourceLine 1: first false, then 3 continuations);
    // Record 2 expands to 3 display lines (sourceLine 2: first false, then 2 continuations).
    expect(map.displayLines.map((l) => ({ s: l.sourceLine, c: l.isContinuation }))).toEqual([
      { s: 1, c: false },
      { s: 1, c: true },
      { s: 1, c: true },
      { s: 1, c: true },
      { s: 2, c: false },
      { s: 2, c: true },
      { s: 2, c: true },
    ]);

    // "val" in raw is on line 1 after the two astral emojis (each 2 UTF-16 units).
    const valRawIndex = raw.indexOf('"val"');
    const dispLoc = sourceLineColToDisplay(map, raw, 1, valRawIndex + 1);
    expect(dispLoc.line).toBe(3);
    expect(dispLoc.col).toBe(3); // after two leading indent spaces

    const backToRaw = displayOffsetToSourceLineCol(map, raw, dispLoc.offset);
    expect(backToRaw.line).toBe(1);
    expect(backToRaw.col).toBe(valRawIndex + 1);
    expect(backToRaw.offset).toBe(valRawIndex);
  });

  it("FMT-AC-05: converts rune spans (SecretScreen and RedactedSpan) accurately across astral characters and virtual layout", () => {
    // "🔒" is 1 rune (2 UTF-16 units), "sk-secret" is 9 runes (9 UTF-16 units).
    const raw = '{"note":"🔒","token":   "sk-secret"}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "secrets.json",
      sourceRevision: 1,
    });

    const table = createRuneOffsetTable(raw);
    const secretUtf16Start = raw.indexOf("sk-secret");
    const secretUtf16End = secretUtf16Start + "sk-secret".length;
    const startRune = table.utf16ToRuneOffset(secretUtf16Start);
    const endRune = table.utf16ToRuneOffset(secretUtf16End);

    // UTF-16 index is 1 higher than rune index because of "🔒".
    expect(secretUtf16Start - startRune).toBe(1);

    // Project both SecretScreen ({start, end}) and RedactedSpan ({start, length}) shapes.
    const projected = mapRuneSpansToDisplay(res.sourceMap, table, [
      { start: startRune, end: endRune },
      { start: startRune, length: endRune - startRune },
    ]);

    expect(projected).toHaveLength(2);
    expect(res.displayText.slice(projected[0]!.from, projected[0]!.to)).toBe("sk-secret");
    expect(res.displayText.slice(projected[1]!.from, projected[1]!.to)).toBe("sk-secret");

    // A source range starting inside removed whitespace `   ` expands conservatively across the virtual layout run.
    const wsStart = raw.indexOf(":   ") + 2;
    const conservative = mapSourceUtf16RangeToDisplay(
      res.sourceMap,
      wsStart,
      secretUtf16End,
    );
    expect(res.displayText.slice(conservative.from, conservative.to)).toBe(' "sk-secret');
  });
});
