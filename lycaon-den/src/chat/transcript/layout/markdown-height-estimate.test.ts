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

it("sizes progress snapshots from their displayed checklist", () => {
  const metrics = { widthPx: 800, remPx: 16, bodyPx: 14 };
  const steps = Array.from({ length: 8 }, (_, index) => ({ label: `Step ${index}`, state: "done" as const }));
  const height = transcriptRowContentEstimate({ kind: "progress_complete", key: "p", steps }, metrics, []);
  expect(height).toBeGreaterThan(190);
  expect(transcriptRowContentEstimate({ kind: "progress_complete", key: "p", steps: steps.slice(0, 1) }, metrics, [])).toBeLessThan(height!);
});
