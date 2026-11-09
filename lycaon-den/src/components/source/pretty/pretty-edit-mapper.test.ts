import { describe, expect, it } from "vitest";
import {
  extractCopySelection,
  extractRawSelection,
  mapDisplayEditToSource,
} from "./pretty-edit-mapper.ts";
import { formatPrettyProjection } from "./pretty-segmenter.ts";

describe("pretty-edit-mapper (FMT-AC-10, FMT-AC-16)", () => {
  it("Case 1 (FMT-AC-10): maps token replacement directly while preserving untouched numeric lexemes and compact separators", () => {
    const raw = '{"a":1,"b":1.20e+3}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case1.json",
      sourceRevision: 1,
      path: "case1.json",
    });
    expect(proj.displayText).toBe('{\n  "a": 1,\n  "b": 1.20e+3\n}');

    const oneDispFrom = proj.displayText.indexOf("1,");
    expect(oneDispFrom).toBe(9);
    const oneRawFrom = raw.indexOf("1,");
    expect(oneRawFrom).toBe(5);

    // Replace `1` at disp [9, 10) with `42`.
    const mapped = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: oneDispFrom, to: oneDispFrom + 1, insert: "42" }],
      intent: "type",
      segments: proj.segments,
    });

    expect(mapped.changes).toEqual([{ from: oneRawFrom, to: oneRawFrom + 1, insert: "42" }]);
    expect(mapped.requiresRawFallback).toBe(false);
    expect(mapped.movedToRawBoundary).toBe(false);

    // Typing a leading zero (`01.20e+3`) produces invalid strict JSON and triggers raw fallback.
    const numDispFrom = proj.displayText.indexOf("1.20e+3");
    const numRawFrom = raw.indexOf("1.20e+3");
    const leadingZeroEdit = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: numDispFrom, to: numDispFrom, insert: "0" }],
      intent: "type",
      segments: proj.segments,
    });
    expect(leadingZeroEdit.changes).toEqual([{ from: numRawFrom, to: numRawFrom, insert: "0" }]);
    expect(leadingZeroEdit.requiresRawFallback).toBe(true);
  });

  it("Case 2a & 2b (FMT-AC-10): replaces enclosing source interval across tokens or deletes zero tokens on virtual-only selection", () => {
    const raw = '{"a": 1,   "b": 2}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case2.json",
      sourceRevision: 1,
      path: "case2.json",
    });
    expect(proj.displayText).toBe('{\n  "a": 1,\n  "b": 2\n}');

    // Case 2a: Select from `1` through `"b"` and replace with `10, "c"`.
    const selFrom = proj.displayText.indexOf("1,");
    const selTo = proj.displayText.indexOf('"b"') + '"b"'.length;
    expect(selFrom).toBe(9);
    expect(selTo).toBe(17);

    const crossTokens = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: selFrom, to: selTo, insert: '10, "c"' }],
      intent: "replace",
      segments: proj.segments,
    });
    expect(crossTokens.changes).toEqual([
      { from: raw.indexOf("1,"), to: raw.indexOf('"b"') + '"b"'.length, insert: '10, "c"' },
    ]);
    expect(crossTokens.changes).toEqual([{ from: 6, to: 14, insert: '10, "c"' }]);
    expect(crossTokens.requiresRawFallback).toBe(false);

    // Case 2b: Select only `\n  ` between `,` and `"b"` (disp [11, 14), virtual-layout) and delete/cut.
    const virtFrom = proj.displayText.indexOf(",\n  ") + 1;
    const virtTo = proj.displayText.indexOf('"b"');
    expect(virtFrom).toBe(11);
    expect(virtTo).toBe(14);

    const virtualOnlyDelete = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: virtFrom, to: virtTo, insert: "" }],
      intent: "cut",
      segments: proj.segments,
    });
    expect(virtualOnlyDelete.changes).toEqual([]);
    expect(virtualOnlyDelete.copiedDisplayText).toBe("\n  ");
    expect(virtualOnlyDelete.copiedRawText).toBe("");
    expect(
      extractCopySelection(proj.sourceMap, raw, proj.displayText, [
        { anchor: virtFrom, head: virtTo },
      ]),
    ).toEqual({ displayText: "\n  ", rawText: "" });
    expect(extractRawSelection(proj.sourceMap, raw, [{ anchor: selFrom, head: selTo }])).toBe(
      '1,   "b"',
    );
  });

  it("Case 3 (FMT-AC-10, FMT-AC-16): collapsed Backspace/Delete in virtual indentation moves to raw boundary with affinity first, then deletes on second step", () => {
    const raw = '{"a":1}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case3.json",
      sourceRevision: 1,
      path: "case3.json",
    });

    // Step 1 Backspace at disp = 4 (end of `\n  ` before `"a"`): moves to disp = 1 (src = 1) with caretAffinity: "before".
    const bs1 = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: 3, to: 4, insert: "" }],
      intent: "delete.backward",
      segments: proj.segments,
    });
    expect(bs1.changes).toEqual([]);
    expect(bs1.movedToRawBoundary).toBe(true);
    expect(bs1.caretAffinity).toBe("before");
    expect(bs1.rawSelection).toEqual([{ anchor: 1, head: 1 }]);
    expect(bs1.displaySelection).toEqual([{ anchor: 1, head: 1 }]);

    // Step 2 Backspace at disp = 1 (immediately after `{`): deletes `{` at src [0, 1).
    const bs2 = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: 0, to: 1, insert: "" }],
      intent: "delete.backward",
      segments: proj.segments,
    });
    expect(bs2.changes).toEqual([{ from: 0, to: 1, insert: "" }]);
    expect(bs2.movedToRawBoundary).toBe(false);
    expect(bs2.requiresRawFallback).toBe(true);

    // Step 1 Delete (forward) at disp = 1 (before `\n  `): moves to disp = 4 (src = 1) with caretAffinity: "after".
    const del1 = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: 1, to: 2, insert: "" }],
      intent: "delete.forward",
      segments: proj.segments,
    });
    expect(del1.changes).toEqual([]);
    expect(del1.movedToRawBoundary).toBe(true);
    expect(del1.caretAffinity).toBe("after");
    expect(del1.rawSelection).toEqual([{ anchor: 1, head: 1 }]);
    expect(del1.displaySelection).toEqual([{ anchor: 4, head: 4 }]);

    // Step 2 Delete (forward) at disp = 4 (before `"a"`): deletes opening `"` at src [1, 2).
    const del2 = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: 4, to: 5, insert: "" }],
      intent: "delete.forward",
      segments: proj.segments,
    });
    expect(del2.changes).toEqual([{ from: 1, to: 2, insert: "" }]);
    expect(del2.movedToRawBoundary).toBe(false);
    expect(del2.requiresRawFallback).toBe(true);
  });

  it("Case 4 (FMT-AC-10, FMT-AC-16): deliberate pasted whitespace at virtual boundary flags raw fallback, while space inside string does not", () => {
    const raw = '{"a":"hello"}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case4.json",
      sourceRevision: 1,
      path: "case4.json",
    });

    // Paste `"   "` at disp = 4 (before `"a"`, insertOffset = 1).
    const keyDispFrom = proj.displayText.indexOf('"a"');
    expect(keyDispFrom).toBe(4);
    const pastedWs = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: keyDispFrom, to: keyDispFrom, insert: "   " }],
      intent: "paste",
      segments: proj.segments,
    });
    expect(pastedWs.changes).toEqual([{ from: 1, to: 1, insert: "   " }]);
    expect(pastedWs.requiresRawFallback).toBe(true);

    // Typing a space inside `"hello"` (inside string token) stays formatted.
    const helloDispIdx = proj.displayText.indexOf("hello") + 5;
    const helloRawIdx = raw.indexOf("hello") + 5;
    expect(helloRawIdx).toBe(11);
    const stringSpace = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: helloDispIdx, to: helloDispIdx, insert: " world" }],
      intent: "type",
      segments: proj.segments,
    });
    expect(stringSpace.changes).toEqual([{ from: helloRawIdx, to: helloRawIdx, insert: " world" }]);
    expect(stringSpace.requiresRawFallback).toBe(false);
  });

  it("Case 5 (FMT-AC-10): JSONL record-delimiter deletion deletes actual source newline at disp [12, 13)", () => {
    const raw = '{"a":1}\n{"b":2}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case5.jsonl",
      sourceRevision: 1,
      path: "case5.jsonl",
    });

    // Record 0 display text `{\n  "a": 1\n}` is 12 UTF-16 units; record delimiter `\n` is at disp [12, 13) -> src [7, 8).
    const delimRun = proj.sourceMap.runs.find((r) => r.kind === "record-delimiter")!;
    expect(delimRun.dispFrom).toBe(12);
    expect(delimRun.dispTo).toBe(13);
    expect(delimRun.srcFrom).toBe(7);
    expect(delimRun.srcTo).toBe(8);

    const delSep = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: delimRun.dispFrom, to: delimRun.dispTo, insert: "" }],
      intent: "delete.forward",
      segments: proj.segments,
    });
    expect(delSep.changes).toEqual([{ from: 7, to: 8, insert: "" }]);
    expect(delSep.movedToRawBoundary).toBe(false);
    expect(delSep.requiresRawFallback).toBe(true);
  });

  it("Case 6a, 6b, 7 & 8 (FMT-AC-10, FMT-AC-16): multi-cursor coalescence/conflicts, IME composition, and malformed intermediate recovery", () => {
    const raw = '{"a":1}';
    const proj = formatPrettyProjection(raw, {
      sourceIdentity: "case6.json",
      sourceRevision: 1,
      path: "case6.json",
    });

    // Case 6a: Two cursors at disp = 2 and disp = 3 (both inside `\n  ` mapping to insertOffset = 1) typing identical `"x":0,`.
    const coalesced = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [
        { from: 2, to: 2, insert: '"x":0,' },
        { from: 3, to: 3, insert: '"x":0,' },
      ],
      intent: "type",
      segments: proj.segments,
    });
    expect(coalesced.changes).toEqual([{ from: 1, to: 1, insert: '"x":0,' }]);
    expect(coalesced.conflictRequiresRawFallback).toBe(false);

    // Case 6b: Conflicting insertions at the same raw offset 1 (`"x"` vs `"y"`) retain ordered intentions without mutating raw source.
    const conflict = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [
        { from: 2, to: 2, insert: "x" },
        { from: 3, to: 3, insert: "y" },
      ],
      intent: "type",
      segments: proj.segments,
    });
    expect(conflict.changes).toEqual([]);
    expect(conflict.conflictRequiresRawFallback).toBe(true);
    expect(conflict.requiresRawFallback).toBe(true);
    expect(conflict.rawSelection).toEqual([{ anchor: 1, head: 1 }]);
    expect(conflict.conflict).toEqual({
      sourceRevision: 1,
      reason: "same-raw-offset-distinct-insert",
      intentions: [
        { order: 0, dispFrom: 2, dispTo: 2, rawFrom: 1, rawTo: 1, insert: "x" },
        { order: 1, dispFrom: 3, dispTo: 3, rawFrom: 1, rawTo: 1, insert: "y" },
      ],
    });

    // Case 7: IME composition pins raw fallback.
    const valDispFrom = proj.displayText.indexOf("1");
    expect(valDispFrom).toBe(9);
    const ime = mapDisplayEditToSource({
      map: proj.sourceMap,
      rawText: raw,
      displayText: proj.displayText,
      changes: [{ from: valDispFrom, to: valDispFrom + 1, insert: "1" }],
      intent: "type",
      composing: true,
      segments: proj.segments,
    });
    expect(ime.requiresRawFallback).toBe(true);
  });
});
