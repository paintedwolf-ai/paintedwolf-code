import { readFileSync } from "node:fs";
import { Text } from "@codemirror/state";
import { Chunk } from "@codemirror/merge";
import { expect, it } from "vitest";
import { SOURCE_DIFF_CONFIG } from "./source-text-diff.ts";
import { normalizeEolForEditor } from "../editor/eol.ts";

type Fixture = { name: string; before: string; after: string; added: number; removed: number };
const cases: Fixture[] = JSON.parse(readFileSync(new URL("../../../../../lycaon/internal/sourcecomparison/testdata/semantics.json", import.meta.url), "utf8"));
const lines = (text: string) => (text.match(/[^\n]*\n|[^\n]+$/g) ?? []).length;
it.each(cases)("shares host line semantics: $name", fixture => {
  const before = Text.of(normalizeEolForEditor(fixture.before).text.split("\n"));
  const after = Text.of(normalizeEolForEditor(fixture.after).text.split("\n"));
  const chunks = Chunk.build(before, after, SOURCE_DIFF_CONFIG);
  expect(chunks.reduce((sum, chunk) => sum + lines(after.sliceString(chunk.fromB, Math.min(after.length, chunk.toB))), 0)).toBe(fixture.added);
  expect(chunks.reduce((sum, chunk) => sum + lines(before.sliceString(chunk.fromA, Math.min(before.length, chunk.toA))), 0)).toBe(fixture.removed);
});
