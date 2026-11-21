import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { RuneOffsets } from "../../chat/transcript/content/rune-offsets.ts";
import { inlineRange } from "./chat-content-viewport.ts";

describe("chat reader coordinates", () => {
  it("rounds inside surrogate pairs down and clamps to the available text", () => {
    const offsets = RuneOffsets.of("x🌲abc🌱");
    expect([-5, 0, 1, 2, 3, 6, 7, 8, 99].map(n => offsets.toRune(n))).toEqual([0, 0, 1, 1, 2, 5, 5, 6, 6]);
    for (let i = 0; i <= offsets.runes; i++) expect(offsets.toRune(offsets.toUtf16(i))).toBe(i);
  });

  it("maps a receipt row and a retained absolute offset to the same Unicode text", () => {
    const doc = Text.of(["🌲\r", "🌱 abc"]);
    expect(inlineRange(doc, { row: 1, column: 2, length: 3 })).toEqual({ anchor: 7, head: 10 });
    expect(inlineRange(doc, { offset: 5, length: 3 })).toEqual({ anchor: 7, head: 10 });
    const receipt = Text.of(["🌲", ""]);
    expect(inlineRange(receipt, { row: 0, column: 2, length: 0 })).toEqual({ anchor: 3, head: 3 });
  });
});
