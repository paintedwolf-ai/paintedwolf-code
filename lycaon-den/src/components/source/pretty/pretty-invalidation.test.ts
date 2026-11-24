import { EditorState, type ChangeSpec } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { createPrettyExtension, prettyLayoutField, prettyOutcomeOf, prettyWorkOf } from "./pretty-layout.ts";
import { ScriptLexer } from "./pretty-lexers.ts";
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

function compare(state: EditorState, changes: ChangeSpec, path = "bundle.js", sample = path): EditorState {
  const next = state.update({ changes }).state;
  expect(layout(next), sample).toEqual(layout(open(next.doc.toString(), path)));
  return next;
}

describe("incremental block facts", () => {
  const fields = Array.from({ length: 480 }, (_, i) => `key${i}:value${i},`).join("");
  const source = `function outer(){ const obj={${fields}result:retu /x;y/ + 1, end:1} }`;
  const at = source.indexOf("retu") + 4;

  it("revises breaks before the checkpoint when keyword completion protects a semicolon", () => {
    const state = open(source);
    const completed = compare(state, { from: at, insert: "rn" });
    expect(layout(completed).ranges.filter(([from]) => from < at).length)
      .toBeGreaterThan(layout(state).ranges.filter(([from]) => from < at).length);
    compare(completed, { from: at, to: at + 2 });
  });

  it("stores one fact per opening after an enclosing group is replayed", () => {
    const text = `function f(){const earlier={${fields}end:1}, result=retu /x;y/ + 1, z=2}`;
    const pos = text.indexOf("retu") + 4;
    let state = open(text);
    const count = state.field(prettyLayoutField).facts.size;
    expect(count).toBeGreaterThan(1);
    for (let i = 0; i < 3; i++) {
      state = compare(state, { from: pos, insert: "rn" });
      state = compare(state, { from: pos, to: pos + 2 });
      expect(state.field(prettyLayoutField).facts.size).toBe(count);
    }
  });

  it("ends unfinished embedded groups at their closing tag", () => {
    const before = `<style>.rule{${"a:b,c:d,".repeat(500)}`;
    const text = before + "</style><p>text}</p>";
    const state = open(text, "page.html");
    const edited = compare(state, { from: text.indexOf("text}"), insert: ";" }, "page.html");
    expect(layout(edited).ranges.filter(([from]) => from < before.length))
      .toEqual(layout(state).ranges.filter(([from]) => from < before.length));
    compare(edited, { from: before.length + 3, insert: "x" }, "page.html");
  });

  it("rewinds past JSX disambiguation that read across a checkpoint", () => {
    const generic = "Type".repeat(1500);
    const text = `const id=<${generic},>(x)=>x;const lookup={${fields}result:1}`;
    const comma = text.indexOf(",");
    const tag = compare(open(text, "component.tsx"), { from: comma, to: comma + 1 }, "component.tsx");
    compare(tag, { from: comma, insert: "," }, "component.tsx");
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

  it.each([
    ["bundle.js", "function f(x){const h=x++ / 2;"],
    ["bundle.ts", "function f(x?: number){const h=x! / 2;"],
    ["bundle.tsx", "function f(x: number){const h=<Tag value={x! / 2}/>;"],
  ])("matches a fresh build after each context edit in %s", (path, prelude) => {
    const before = `${prelude}const table={a:1,b:2,pad:"${"p".repeat(PRETTY_CHECKPOINT_INTERVAL)}",v:`;
    const after = " /x;y/ + 1,end:2};return table}";
    const fixture = (word: string) => before + word + after;
    const original = fixture("retu");
    const operators = ["!", "++", "--", "/", "\\\n", "`", "${", "}", ";", "/*", "*/"];
    const jsx = path.endsWith(".tsx") ? ["<", "/>", "</", "<Tag", "{expr}"] : [];
    const vocabulary = new Set([...operators, ...jsx, ...ScriptLexer.regexAfterWords]);
    let edits = 0;
    let state = open(original, path);
    const checkpoint = state.field(prettyLayoutField).checkpoints.iter(1);
    expect(checkpoint.value).toBeTruthy();
    expect(checkpoint.from).toBeLessThan(before.length);
    function apply(changes: ChangeSpec): void {
      state = compare(state, changes, path, `${path} edit ${++edits}`);
    }
    function removeAndRestore(from: number, text: string): void {
      expect(state.doc.sliceString(from, from + text.length)).toBe(text);
      apply({ from, to: from + text.length });
      apply({ from, insert: text });
    }

    // Complete and undo every split at the slash whose interpretation the word controls.
    for (const word of ScriptLexer.regexAfterWords) {
      state = open(fixture(word), path);
      removeAndRestore(before.length, word);
      for (let cut = 1; cut < word.length; cut++) {
        const prefix = word.slice(0, cut), suffix = word.slice(cut);
        vocabulary.add(prefix);
        vocabulary.add(suffix);
        removeAndRestore(before.length, prefix);
        removeAndRestore(before.length + cut, suffix);
      }
    }

    // Mutate each punctuation token at least once before the seeded edit chains.
    for (const token of operators) {
      state = open(fixture(token), path);
      removeAndRestore(before.length, token);
    }
    if (jsx.length) {
      const element = "<Tag>{expr}<Leaf/></Tag>";
      state = open(fixture(element), path);
      for (const token of jsx) removeAndRestore(before.length + element.indexOf(token), token);
    }

    const tokens = [...vocabulary];
    let seed = 7919;
    for (const char of path) seed = (Math.imul(seed, 31) + char.charCodeAt(0)) >>> 0;
    const random = (n: number) => {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      return Math.floor(seed / 0x100000000 * n);
    };
    for (let sample = 0; sample < 2000; sample++) {
      if (sample % 50 === 0) state = open(original, path);
      // Sample the syntax on both sides of the padding and edits near the saved checkpoint.
      const ends = Math.min(180, state.doc.length);
      const from = random(3) === 0 ? random(ends) : state.doc.length - 1 - random(ends);
      const op = random(3);
      const to = op === 0 ? from : Math.min(state.doc.length, from + 1 + random(7));
      apply({ from, to, insert: op === 1 ? "" : tokens[random(tokens.length)]! });
    }
  });
});
