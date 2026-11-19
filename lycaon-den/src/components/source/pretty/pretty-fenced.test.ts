// @vitest-environment jsdom
import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { finishFixtureParsing } from "../../../test/codemirror-parsing-fixture.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

const mdExtensions = [
  indentUnit.of("  "),
  languageExtensionForPath("README.md")!,
  createPrettyExtension({ enabled: true, path: "README.md" }),
];

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
    const state = finishFixtureParsing(EditorState.create({ doc, extensions: mdExtensions }));
    expect(breakCount(state)).toBeGreaterThan(2);
  });

  it("lays out JSON containers inside unlabelled fences while leaving prose fences untouched", () => {
    const jsonDoc = ["```", '{"a":1,"b":{"c":2}}', "```"].join("\n");
    const jsonState = finishFixtureParsing(EditorState.create({ doc: jsonDoc, extensions: mdExtensions }));
    expect(breakCount(jsonState)).toBeGreaterThan(0);

    const proseDoc = ["```", "+---+   +---+", "| A |-->| B |", "+---+   +---+", "```"].join("\n");
    const proseState = finishFixtureParsing(EditorState.create({ doc: proseDoc, extensions: mdExtensions }));
    expect(breakCount(proseState)).toBe(0);
  });
});
