import { describe, expect, it } from "vitest";
import { wordChanges } from "./word-changes.ts";

describe("word changes", () => {
  it("keeps marks on a line that also keeps a word", () => {
    const text = "const originalName = value;";
    expect(wordChanges(text, [{ from: 6, to: 18 }])).toEqual([{ from: 6, to: 18 }]);
  });

  it("drops marks on a line whose words all changed, even around shared punctuation", () => {
    expect(wordChanges("  return (bests);", [{ from: 2, to: 8 }, { from: 10, to: 15 }])).toEqual([]);
  });

  it("decides each line of a multi-line change on its own", () => {
    const text = "alpha beta\ngamma\n  delta epsilon";
    const marks = wordChanges(text, [{ from: 6, to: 16 }, { from: 19, to: 24 }]);
    expect(marks).toEqual([{ from: 6, to: 10 }, { from: 19, to: 24 }]);
  });

  it("measures words by code point", () => {
    expect(wordChanges("🙂 名前 x", [{ from: 3, to: 5 }])).toEqual([{ from: 3, to: 5 }]);
    expect(wordChanges("🙂 名前", [{ from: 3, to: 5 }])).toEqual([]);
  });
});
