// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import axe from "axe-core";
import { afterEach, describe, expect, it } from "vitest";
import {
  lineGutterExtension,
  setLineGutterFindings,
} from "./line-gutter.ts";
import { findingGlyph } from "./knowing-gutter-model.ts";

describe("knowing gutter a11y", () => {
  let view: EditorView | undefined;
  afterEach(() => {
    view?.destroy();
    view = undefined;
    document.body.replaceChildren();
  });

  it("axe is green with colour forced off; glyphs stay distinct by shape", async () => {
    const parent = document.createElement("div");
    parent.style.color = "#000";
    parent.style.background = "#fff";
    // Force a monochrome surface so severity cannot rely on colour alone.
    parent.style.filter = "grayscale(1)";
    document.body.appendChild(parent);
    view = new EditorView({
      parent,
      state: EditorState.create({
        doc: "one\ntwo\nthree\n",
        extensions: [lineGutterExtension],
      }),
    });
    setLineGutterFindings(view, [
      {
        line: 1,
        level: "critical",
        findings: [
          {
            rule_id: "r",
            level: "critical",
            message: "c",
            locations: [{ uri: "f", start_line: 1 }],
            fingerprints: { primary: "c" },
            tool: { driver_id: "d", name: "n" },
          },
        ],
      },
      {
        line: 2,
        level: "high",
        findings: [
          {
            rule_id: "r",
            level: "high",
            message: "h",
            locations: [{ uri: "f", start_line: 2 }],
            fingerprints: { primary: "h" },
            tool: { driver_id: "d", name: "n" },
          },
        ],
      },
      {
        line: 3,
        level: "medium",
        findings: [
          {
            rule_id: "r",
            level: "medium",
            message: "m",
            locations: [{ uri: "f", start_line: 3 }],
            fingerprints: { primary: "m" },
            tool: { driver_id: "d", name: "n" },
          },
        ],
      },
    ]);
    const glyphs = [
      ...view.dom.querySelectorAll("[data-testid='files-line-gutter-finding']"),
    ].map((el) => el.textContent);
    expect(new Set(glyphs).size).toBe(3);
    expect(glyphs).toEqual([
      findingGlyph("critical"),
      findingGlyph("high"),
      findingGlyph("medium"),
    ]);

    // Scope axe to the line gutter — CM's contenteditable is labelled by
    // the Files stage host, not this unit fixture.
    const gutter = view.dom.querySelector(".files-line-gutter") as HTMLElement;
    expect(gutter).toBeTruthy();
    const results = await axe.run(gutter, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    expect(results.violations, JSON.stringify(results.violations)).toEqual([]);
  });
});
