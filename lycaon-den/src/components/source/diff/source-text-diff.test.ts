import { describe, expect, it, vi } from "vitest";
import { Text } from "@codemirror/state";
import { Chunk } from "@codemirror/merge";
import { diffSourceText, SOURCE_DIFF_CONFIG } from "./source-text-diff.ts";

function verifyReconstruction(before: string, after: string): void {
  const changes = diffSourceText(before, after);
  let cursorA = 0, cursorB = 0, rebuilt = "";
  for (const change of changes) {
    expect(before.slice(cursorA, change.fromA)).toBe(after.slice(cursorB, change.fromB));
    expect(change.fromA).toBeGreaterThanOrEqual(cursorA);
    expect(change.fromB).toBeGreaterThanOrEqual(cursorB);
    rebuilt += before.slice(cursorA, change.fromA) + after.slice(change.fromB, change.toB);
    cursorA = change.toA;
    cursorB = change.toB;
  }
  rebuilt += before.slice(cursorA);
  expect(rebuilt).toBe(after);
}

describe("source text diff", () => {
  it.each([3, 30])("keeps edits every %i lines separate beyond the character diff's coarse limit", (stride) => {
    const lines = Array.from({ length: 1200 }, (_, i) => `export const value${i} = ${i};`);
    const before = lines.join("\n");
    const after = lines.map((line, i) => i % stride === 0 ? line.replace(`= ${i};`, `= ${-i - 1};`) : line).join("\n");
    const chunks = Chunk.build(Text.of(before.split("\n")), Text.of(after.split("\n")), SOURCE_DIFF_CONFIG);
    expect(chunks).toHaveLength(1200 / stride);
    expect(chunks.every(chunk => !before.slice(chunk.fromA, chunk.endA).includes("\n"))).toBe(true);
    verifyReconstruction(before, after);
  });

  it("keeps unchanged anchors even when character refinement exhausts its time budget", () => {
    const before = "one\nanchor a\ntwo\nanchor b\nthree\n";
    const after = "ONE\nanchor a\nTWO\nanchor b\nTHREE\n";
    const clock = vi.spyOn(performance, "now").mockReturnValueOnce(0).mockReturnValue(100);
    try {
      const changes = diffSourceText(before, after);
      expect(changes).toHaveLength(3);
      expect(changes.map(change => before.slice(change.fromA, change.toA))).toEqual(["one\n", "two\n", "three\n"]);
      verifyReconstruction(before, after);
    } finally { clock.mockRestore(); }
  });

  it("does not mark identical repeated gaps after refinement expires", () => {
    const before = "first\nrepeat\nrepeat\nmiddle\nold\nrepeat\nlast\n";
    const after = before.replace("old", "new");
    const clock = vi.spyOn(performance, "now").mockReturnValueOnce(0).mockReturnValue(100);
    try {
      const changes = diffSourceText(before, after);
      expect(changes.map(change => before.slice(change.fromA, change.toA))).toEqual(["old\nrepeat\n"]);
    } finally { clock.mockRestore(); }
  });

  it.each([
    ["", "new\n"], ["old\n", ""], ["same", "same"], ["a\nb\n", "a\nB\n"],
    ["a\nb\n", "a\n"], ["a\n", "a\nb\n"], ["a\n", "a"], ["a", "a\n"],
    ["🐺 wolf\n🐕 dog\n", "🐺 wolves\n🐕 dog\nnew 🐾"],
    ["x\nx\ny\nx\n", "x\ny\nx\nx\n"], ["a\r\nb\r\n", "a\r\nB\r\n"],
  ])("reconstructs %j → %j without losing unchanged text", verifyReconstruction);

  it("preserves the source across many insertions, removals, and replacements", () => {
    let seed = 42;
    const random = () => (seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0);
    let before = Array.from({ length: 100 }, (_, i) => `line ${i} 🐺`).join("\n");
    for (let step = 0; step < 100; step++) {
      const lines = before.split("\n");
      lines.splice(random() % lines.length, random() % 3, `edit ${step}`);
      const after = lines.join("\n");
      verifyReconstruction(before, after);
      before = after;
    }
  });
});
