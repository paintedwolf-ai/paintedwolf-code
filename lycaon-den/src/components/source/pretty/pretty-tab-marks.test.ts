// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { MARKED_TAB_CLASS } from "../editor/editor-theme-rules.ts";
import { applyPrettyConfig, createPrettyExtension } from "./pretty-layout.ts";
import { leadingTabMarks } from "./pretty-tab-marks.ts";

function marked(doc: string, ranges = [{ from: 0, to: doc.length }]): number[] {
  const out: number[] = [];
  leadingTabMarks(EditorState.create({ doc }), ranges).between(0, doc.length, (from, to) => {
    expect(to).toBe(from + 1);
    out.push(from);
  });
  return out;
}

const tabsIn = (doc: string, positions: number[]) => positions.map((at) => doc[at]);

describe("leading tab marks", () => {
  it("marks every tab before the first non-blank character", () => {
    const doc = "\t\tcall()\n  \tmixed\nnone\n\t \tdeep";
    const at = marked(doc);
    expect(at).toEqual([0, 1, 11, 23, 25]);
    expect(tabsIn(doc, at).every((c) => c === "\t")).toBe(true);
  });

  it("leaves tabs after the indentation and on blank lines alone", () => {
    expect(marked("a\tb\t// note\n\t\t\n \t \nx")).toEqual([]);
  });

  it("finds indentation longer than one read", () => {
    const doc = `${" ".repeat(600)}\t${"x".repeat(10)}`;
    expect(marked(doc)).toEqual([600]);
  });

  it("marks a line once when several ranges reach it", () => {
    const doc = "\tfirst line here\n\tsecond";
    expect(marked(doc, [{ from: 0, to: 4 }, { from: 8, to: doc.length }])).toEqual([0, 17]);
  });
});

describe("in the editor", () => {
  const views: EditorView[] = [];
  afterEach(() => {
    for (const view of views.splice(0)) {
      view.dom.remove();
      view.destroy();
    }
  });

  function open(doc: string, enabled: boolean): EditorView {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      parent,
      state: EditorState.create({ doc, extensions: [createPrettyExtension({ enabled, path: "data.json" })] }),
    });
    views.push(view);
    return view;
  }

  const drawn = (view: EditorView) => view.contentDOM.querySelectorAll(`.${MARKED_TAB_CLASS}`).length;

  it("draws the marks only while layout is active and follows edits", () => {
    const doc = '{\n\t"a": 1,\n\t"b": [\n\t\t2\n\t]\n}';
    const view = open(doc, false);
    expect(drawn(view)).toBe(0);
    applyPrettyConfig(view, { enabled: true, path: "data.json" });
    expect(drawn(view)).toBe(5);
    view.dispatch({ changes: { from: 2, insert: "\t" } });
    expect(drawn(view)).toBe(6);
    applyPrettyConfig(view, { enabled: false, path: "data.json" });
    expect(drawn(view)).toBe(0);
  });
});
