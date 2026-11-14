// @vitest-environment jsdom
import { forceParsing, indentUnit, syntaxTree } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { applyPrettyConfig, createPrettyExtension, prettyWorkOf } from "./pretty-layout.ts";
import { PRETTY_CHECKPOINT_INTERVAL } from "./pretty-scan-state.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function lastDecoration(view: EditorView): number {
  let last = -1;
  view.state.facet(prettyLayoutDecorations).between(0, view.state.doc.length, (from) => {
    last = Math.max(last, from);
  });
  return last;
}

describe("layout over the parsed range", () => {
  it("lays out only parsed text, extends as parsing advances, and never forces a parse", () => {
    const line = (i: number) => `result${i} := compute(${Array.from({ length: 40 }, (_, k) => `arg${k}`).join(", ")}, "s, (t)") // c, (d)`;
    const doc = Array.from({ length: 6_000 }, (_, i) => line(i)).join("\n");
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      parent,
      state: EditorState.create({
        doc,
        extensions: [languageExtensionForPath("big.go")!, indentUnit.of("\t"), createPrettyExtension({ enabled: false, path: "big.go" })],
      }),
    });
    try {
      const parsedBefore = syntaxTree(view.state).length;
      expect(parsedBefore).toBeLessThan(doc.length);
      applyPrettyConfig(view, { enabled: true, path: "big.go" });
      const parsed = syntaxTree(view.state).length;
      expect(parsed).toBe(parsedBefore);
      expect(lastDecoration(view)).toBeGreaterThan(0);
      expect(lastDecoration(view)).toBeLessThan(parsed);

      const target = Math.min(doc.length, parsed + 200_000);
      expect(forceParsing(view, target, 5_000)).toBe(true);
      const reached = syntaxTree(view.state).length;
      expect(reached).toBeGreaterThan(parsed);
      const work = prettyWorkOf(view.state);
      expect(work.from).toBeGreaterThanOrEqual(view.state.doc.lineAt(parsed).from - 2 * PRETTY_CHECKPOINT_INTERVAL);
      expect(work.visits).toBeLessThan(3 * (reached - parsed) + 4 * PRETTY_CHECKPOINT_INTERVAL);
      expect(lastDecoration(view)).toBeGreaterThan(parsed);
      expect(lastDecoration(view)).toBeLessThan(reached);
    } finally {
      view.destroy();
      parent.remove();
    }
  });
});
