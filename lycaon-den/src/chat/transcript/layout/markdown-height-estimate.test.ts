import { expect, it } from "vitest";
import { markdownHeightEstimate } from "./markdown-height-estimate.ts";
import { transcriptRowContentEstimate } from "./transcript-row-content-estimate.ts";

it("counts padded table rows and does not count the delimiter as a rendered row", () => {
  const table = "| Name | Value |\n| --- | --- |\n" + "| short | text |\n".repeat(30);
  const height = markdownHeightEstimate(table, 600, 14);
  expect(height).toBeGreaterThan(31 * 14 * 2.3);
  expect(height).toBeLessThan(31 * 14 * 2.8);
});

it("keeps wide code lines horizontal while reserving every vertical line", () => {
  const code = "```ts\n" + `${"x".repeat(400)}\n`.repeat(80) + "```";
  expect(markdownHeightEstimate(code, 300, 14)).toBe(markdownHeightEstimate(code, 900, 14));
  expect(markdownHeightEstimate(code, 300, 14)).toBeGreaterThan(80 * 14);
});

it("sizes a plan from its checklist and a later progress row as one line", () => {
  const metrics = { widthPx: 800, remPx: 16, bodyPx: 14 };
  const steps = Array.from({ length: 8 }, (_, index) => ({ label: `Step ${index}`, state: "done" as const }));
  const plan = (count: number) =>
    transcriptRowContentEstimate({ kind: "progress_update", key: "p", initial: true, steps: steps.slice(0, count), changes: [] }, metrics, []);
  expect(plan(8)).toBeGreaterThan(190);
  expect(plan(1)).toBeLessThan(plan(8)!);
  const done = (count: number) =>
    transcriptRowContentEstimate({ kind: "progress_complete", key: "d", steps: steps.slice(0, count) }, metrics, []);
  expect(done(8)).toBe(done(1));
  expect(done(8)).toBeLessThan(plan(1)!);
});

it("accounts for wider inline code spans and CJK glyphs when estimating line wraps", () => {
  const plain = "a".repeat(36);
  const inlineCode = "`" + "a".repeat(36) + "`";
  const cjk = "漢".repeat(36);
  const plainHeight = markdownHeightEstimate(plain, 280, 14);
  const codeHeight = markdownHeightEstimate(inlineCode, 280, 14);
  const cjkHeight = markdownHeightEstimate(cjk, 280, 14);
  expect(codeHeight).toBeGreaterThan(plainHeight);
  expect(cjkHeight).toBeGreaterThan(plainHeight);
});
