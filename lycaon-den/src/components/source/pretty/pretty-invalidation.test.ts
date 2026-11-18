import { EditorState, type ChangeSpec } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { createPrettyExtension, prettyOutcomeOf, prettyWorkOf } from "./pretty-layout.ts";
import { PRETTY_CHECKPOINT_INTERVAL } from "./pretty-scan-state.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, path = "bundle.js"): EditorState {
  return EditorState.create({ doc, extensions: createPrettyExtension({ path, enabled: true }) });
}

function layout(state: EditorState) {
  const ranges: Array<[number, number, string]> = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, decoration) => {
    ranges.push([from, to, String(decoration.spec.class ?? "break")]);
  });
  return { ranges, outcome: prettyOutcomeOf(state) };
}

function compare(state: EditorState, changes: ChangeSpec, path = "bundle.js"): EditorState {
  const next = state.update({ changes }).state;
  expect(layout(next)).toEqual(layout(open(next.doc.toString(), path)));
  return next;
}

describe("incremental block facts", () => {
  const fields = Array.from({ length: 480 }, (_, i) => `key${i}:value${i},`).join("");
  const source = `function outer(){ const obj={${fields}result:retu /x;y/ / 1, end:1} }`;
  const at = source.indexOf("retu") + 4;

  it("revises breaks before the checkpoint when keyword completion protects a semicolon", () => {
    const state = open(source);
    const completed = compare(state, { from: at, insert: "rn" });
    expect(layout(completed).ranges.filter(([from]) => from < at).length)
      .toBeGreaterThan(layout(state).ranges.filter(([from]) => from < at).length);
    compare(completed, { from: at, to: at + 2 });
  });

  it("maps delimiter dependencies across replacements, line edits and disjoint changes", () => {
    const semicolon = source.indexOf(";");
    const close = source.indexOf("}", semicolon);
    for (const changes of [
      { from: semicolon, to: semicolon + 2, insert: "y;" },
      { from: close, to: close + 1 },
      [{ from: at, insert: "rn" }, { from: close, insert: "\n" }],
    ]) compare(open(source), changes);
    const split = compare(open(source), { from: at, insert: "\n" });
    compare(split, { from: at, to: at + 1 });
  });

  it("keeps harmless length changes inside long script facts local", () => {
    const text = `function f(){const obj={${fields}name:"blue",value:3,${fields}last:42} }`;
    const at = text.indexOf("blue") + 2;
    let state = open(text);
    for (const changes of [{ from: at, insert: "a" }, { from: at, to: at + 1 }]) {
      state = compare(state, changes);
      const work = prettyWorkOf(state);
      expect(work.visits).toBeLessThan(4 * PRETTY_CHECKPOINT_INTERVAL);
      expect(work.to - work.from).toBeLessThan(3 * PRETTY_CHECKPOINT_INTERVAL);
    }
  });

  it.each(["bundle.js", "bundle.ts", "bundle.tsx"])("matches a fresh build after each context edit in %s", path => {
    const words = ["retu", "rn", "typeof", "throw", "instanceof", "++", "!", "--", "/", "\\\n", "`", "${", "}", ";", "/*", "*/"];
    if (path.endsWith("tsx")) words.push("<Tag", "/>", "</", "{expr}");
    const doc = `function f(){const table={${"k:12,".repeat(75)}v:retu /x;y/ / 1};return table}`;
    let seed = path.length * 7919;
    const random = (n: number) => {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      return Math.floor(seed / 0x100000000 * n);
    };
    let state = open(doc, path);
    for (let i = 0; i < 2000; i++) {
      if (i % 50 === 0) state = open(doc, path);
      const from = random(state.doc.length);
      const to = Math.min(state.doc.length, from + random(8));
      state = compare(state, { from, to, insert: words[random(words.length)]! }, path);
    }
  });
});
