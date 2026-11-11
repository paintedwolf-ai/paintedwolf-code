import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { createPrettyExtension } from "./pretty-layout.ts";
import type { PrettyConfig } from "./pretty-rescan.ts";
import { PrettyBreakWidget, prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, lineWidth?: number, path = "a.ts"): EditorState {
  const config: PrettyConfig = { enabled: true, path, ...(lineWidth ? { lineWidth } : {}) };
  return EditorState.create({
    doc,
    extensions: [indentUnit.of("  "), EditorState.tabSize.of(4), createPrettyExtension(config)],
  });
}

function layout(state: EditorState): { breaks: number[]; lines: Array<[number, number, number]> } {
  const breaks: number[] = [];
  const lines: Array<[number, number, number]> = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget instanceof PrettyBreakWidget) breaks.push(from);
    else lines.push([from, to, Number(/pw-pretty-line-(\d+)/.exec(String(value.spec.class))?.[1])]);
  });
  return { breaks, lines };
}

function writtenOut(state: EditorState): string {
  const { breaks, lines } = layout(state);
  const columnsAt = new Map(lines.map(([from, , columns]) => [from, columns]));
  const text = state.doc.toString();
  let out = "";
  let last = 0;
  for (const pos of breaks) {
    out += text.slice(last, pos).replace(/[ \t]+$/, "") + "\n" + " ".repeat(columnsAt.get(pos) ?? 0);
    last = pos;
  }
  return out + text.slice(last);
}

const item = (n: number) => `entry${n}(${Array.from({ length: 9 }, (_, i) => `argument${i}`).join(", ")})`;

describe("declared line width", () => {
  const call = `run(${Array.from({ length: 12 }, (_, i) => `value${i}`).join(", ")});`;

  it("lays out a line whose code passes the declared width, and only then", () => {
    expect(call.length).toBeGreaterThan(60);
    expect(call.length).toBeLessThan(240);
    expect(layout(open(call)).breaks).toEqual([]);
    expect(layout(open(call, 120)).breaks).toEqual([]);
    expect(layout(open(call, 60)).breaks.length).toBeGreaterThan(0);
  });

  it("leaves a line that is long only for its strings and comments", () => {
    const line = `log("${"a long message ".repeat(12)}", value); // ${"note ".repeat(10)}`;
    expect(line.length).toBeGreaterThan(200);
    expect(layout(open(line, 80)).breaks).toEqual([]);
  });

  it("fits groups to the declared width instead of 80 columns", () => {
    const line = `const table = [${[1, 2, 3].map(item).join(", ")}];`;
    expect(item(1).length).toBeGreaterThan(80);
    expect(item(1).length + 2).toBeLessThan(120);
    expect(line.length).toBeGreaterThan(240);
    expect(writtenOut(open(line, 120)).split("\n")).toEqual([
      "const table = [",
      `  ${item(1)},`,
      `  ${item(2)},`,
      `  ${item(3)},`,
      "];",
    ]);
    expect(writtenOut(open(line)).split("\n").length).toBeGreaterThan(5);
  });

  it("adds nothing on a second pass at the same width", () => {
    const line = `export const config = {${Array.from({ length: 14 }, (_, i) => `key${i}: call(alpha${i}, beta${i})`).join(", ")}};`;
    const first = open(line, 70);
    expect(layout(first).breaks.length).toBeGreaterThan(0);
    expect(layout(open(writtenOut(first), 70)).breaks).toEqual([]);
  });

  it("keeps incremental layout equal to a fresh one when an edit changes whether a line is laid out", () => {
    let state = open(`${call}\n${call}\n`, 60);
    const fresh = (s: EditorState) => layout(open(s.doc.toString(), 60));
    // Quoting the arguments leaves too little code to lay out, and unquoting restores it.
    for (const change of [{ from: 4, insert: '"' }, { from: call.length - 2, insert: '"' }]) {
      state = state.update({ changes: change }).state;
      expect(layout(state)).toEqual(fresh(state));
    }
    expect(layout(state).breaks.every((pos) => pos > call.length + 1)).toBe(true);
    state = state.update({ changes: { from: 4, to: 5 } }).state;
    expect(layout(state)).toEqual(fresh(state));
  });
});
