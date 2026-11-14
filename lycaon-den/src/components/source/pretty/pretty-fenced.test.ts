import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function breakCount(state: EditorState): number {
  let count = 0;
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) count += 1;
  });
  return count;
}

describe("Markdown fenced code improvements", () => {
  it("lays out blockquoted and 6-space list-indented fences", () => {
    const doc = [
      "> ```json",
      '> {"a":1,"b":{"c":2,"d":[3,4]}}',
      "> ```",
      "",
      "- item",
      "      ```ts",
      `      const x = { a: "${"v".repeat(250)}", b: [1, 2, 3] };`,
      "      ```",
    ].join("\n");
    const state = EditorState.create({
      doc,
      extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path: "README.md" })],
    });
    expect(breakCount(state)).toBeGreaterThan(2);
  });

  it("lays out JSON containers inside unlabelled fences while leaving prose fences untouched", () => {
    const jsonDoc = ["```", '{"a":1,"b":{"c":2}}', "```"].join("\n");
    const jsonState = EditorState.create({
      doc: jsonDoc,
      extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path: "README.md" })],
    });
    expect(breakCount(jsonState)).toBeGreaterThan(0);

    const proseDoc = ["```", "+---+   +---+", "| A |-->| B |", "+---+   +---+", "```"].join("\n");
    const proseState = EditorState.create({
      doc: proseDoc,
      extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path: "README.md" })],
    });
    expect(breakCount(proseState)).toBe(0);
  });
});
