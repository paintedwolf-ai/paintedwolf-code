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
  it("lays out only parsed text, extends as parsing advances, and never forces a parse (Lezer and StreamLanguage)", () => {
    const cases = [
      {
        path: "big.go",
        unit: "\t",
        line: (i: number) =>
          `result${i} := compute(${Array.from({ length: 40 }, (_, k) => `arg${k}`).join(", ")}, "s, (t)") // c, (d)`,
      },
      {
        path: "Big.java",
        unit: "  ",
        line: (i: number) =>
          `int r${i} = compute(${Array.from({ length: 40 }, (_, k) => `arg${k}`).join(", ")}, "s, (t)"); // c, (d)`,
      },
    ];

    for (const { path, unit, line } of cases) {
      const doc = Array.from({ length: 6_000 }, (_, i) => line(i)).join("\n");
      const parent = document.createElement("div");
      document.body.appendChild(parent);
      const view = new EditorView({
        parent,
        state: EditorState.create({
          doc,
          extensions: [
            languageExtensionForPath(path)!,
            indentUnit.of(unit),
            createPrettyExtension({ enabled: false, path }),
          ],
        }),
      });
      try {
        const parsedBefore = syntaxTree(view.state).length;
        expect(parsedBefore, path).toBeLessThan(doc.length);
        applyPrettyConfig(view, { enabled: true, path });
        const parsed = syntaxTree(view.state).length;
        expect(parsed, path).toBe(parsedBefore);
        expect(lastDecoration(view), path).toBeGreaterThan(0);
        expect(lastDecoration(view), path).toBeLessThan(parsed);

        const target = Math.min(doc.length, parsed + 200_000);
        expect(forceParsing(view, target, 5_000), path).toBe(true);
        const reached = syntaxTree(view.state).length;
        expect(reached, path).toBeGreaterThan(parsed);
        const work = prettyWorkOf(view.state);
        expect(work.from, path).toBeGreaterThanOrEqual(
          view.state.doc.lineAt(parsed).from - 2 * PRETTY_CHECKPOINT_INTERVAL,
        );
        expect(work.visits, path).toBeLessThan(
          3 * (reached - parsed) + 4 * PRETTY_CHECKPOINT_INTERVAL,
        );
        expect(lastDecoration(view), path).toBeGreaterThan(parsed);
        expect(lastDecoration(view), path).toBeLessThan(reached);
      } finally {
        view.destroy();
        parent.remove();
      }
    }
  });

  it("waits for syntaxTree on fenced Markdown instead of scanning bracketed fences with Tree.empty", () => {
    const args = Array.from({ length: 30 }, (_, i) => `arg_${i}`).join(", ");
    const trap = '"keep, (together)"';
    const doc = ["```python", `result = compute(${args}, ${trap})`, "```"].join("\n");

    const unparsed = new EditorView({
      state: EditorState.create({
        doc,
        extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path: "notes.md" })],
      }),
    });
    try {
      expect(lastDecoration(unparsed)).toBe(-1);
    } finally {
      unparsed.destroy();
    }

    const parsed = new EditorView({
      state: EditorState.create({
        doc,
        extensions: [
          languageExtensionForPath("notes.md")!,
          indentUnit.of("  "),
          createPrettyExtension({ enabled: true, path: "notes.md" }),
        ],
      }),
    });
    try {
      forceParsing(parsed, doc.length, 5_000);
      expect(lastDecoration(parsed)).toBeGreaterThan(0);
      const trapStart = doc.indexOf(trap);
      const trapEnd = trapStart + trap.length;
      const breaksInTrap: number[] = [];
      parsed.state.facet(prettyLayoutDecorations).between(trapStart + 1, trapEnd - 1, (from, to) => {
        if (from === to) breaksInTrap.push(from);
      });
      expect(breaksInTrap).toEqual([]);
    } finally {
      parsed.destroy();
    }
  });
});
