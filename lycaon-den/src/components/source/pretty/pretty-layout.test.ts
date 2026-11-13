import { indentUnit } from "@codemirror/language";
import { EditorState, type Extension } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import {
  createPrettyExtension,
  PRETTY_MAX_INPUT,
  type PrettyConfig,
  prettyOutcomeOf,
  prettyWorkOf,
} from "./pretty-layout.ts";
import { PRETTY_CHECKPOINT_INTERVAL } from "./pretty-scan-state.ts";
import { PrettyBreakWidget, prettyLayoutDecorations } from "./pretty-view.ts";

type Layout = { breaks: number[]; lines: Array<[number, number, number]> };

function open(doc: string, config: Omit<PrettyConfig, "enabled">, unit = "  ", extra: Extension = []): EditorState {
  return EditorState.create({
    doc,
    extensions: [indentUnit.of(unit), EditorState.tabSize.of(4), createPrettyExtension({ enabled: true, ...config }), extra],
  });
}

function layoutOf(state: EditorState): Layout {
  const breaks: number[] = [];
  const lines: Array<[number, number, number]> = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to) {
      expect(value.spec.widget).toBeInstanceOf(PrettyBreakWidget);
      breaks.push(from);
    } else {
      const columns = /pw-pretty-line-(\d+)/.exec(String(value.spec.class))?.[1];
      expect(columns).toBeDefined();
      lines.push([from, to, Number(columns)]);
    }
  });
  return { breaks, lines };
}

/** The source with each break written out as a newline and its visual line's indentation. */
function writtenOut(state: EditorState): string {
  const { breaks, lines } = layoutOf(state);
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

function pretty(doc: string, path: string, unit = "  "): string {
  return writtenOut(open(doc, { path }, unit));
}

function rebuilt(state: EditorState): Layout {
  return layoutOf(EditorState.create({
    doc: state.doc,
    extensions: [indentUnit.of(state.facet(indentUnit)), EditorState.tabSize.of(state.tabSize), createPrettyExtension(currentConfig(state))],
  }));
}

const configs = new WeakMap<EditorState, PrettyConfig>();
function currentConfig(state: EditorState): PrettyConfig {
  return configs.get(state) ?? { enabled: true };
}

function edit(state: EditorState, from: number, to: number, insert: string): EditorState {
  const next = state.update({ changes: { from, to, insert } }).state;
  configs.set(next, currentConfig(state));
  return next;
}

describe("defined layout", () => {
  it("expands every non-empty data container and leaves empty ones", () => {
    expect(pretty('{"name":"pw","tags":["a","b"],"deps":{"x":1,"y":[]}}', "data.json")).toBe([
      "{",
      '  "name":"pw",',
      '  "tags":[',
      '    "a",',
      '    "b"',
      "  ],",
      '  "deps":{',
      '    "x":1,',
      '    "y":[]',
      "  }",
      "}",
    ].join("\n"));
  });

  it("matches the indented serialisation of JSON in line structure and columns", () => {
    const value = { service: "gateway", ports: [80, 443], nested: { list: [{ a: 1 }, { b: [true, null] }], empty: {} } };
    const written = pretty(JSON.stringify(value), "data.json");
    const expected = JSON.stringify(value, null, 2).replace(/": /g, '":');
    expect(written).toBe(expected);
  });

  it("adds nothing to a formatted source", () => {
    const formatted = JSON.stringify({ a: 1, b: { c: [1, 2] } }, null, 2);
    expect(layoutOf(open(formatted, { path: "data.json" })).breaks).toEqual([]);
    const module = [
      'import { a, b } from "x";',
      "type Item = { id: string; name: string };",
      "export function f(o: { id: string; name: string }) { if (o) { return o.id; } return a + b; }",
      "const table = { alpha: 1, beta: [1, 2, 3], gamma: { delta: true } };",
    ].join("\n");
    expect(layoutOf(open(module, { path: "module.ts" })).breaks).toEqual([]);
    expect(layoutOf(open(".a { color: red; } .b { margin: 0 }\n", { path: "s.css" })).breaks).toEqual([]);
  });

  it("lays out a code line wider than 240 columns and leaves one at 240", () => {
    const call = (width: number) => {
      const items: string[] = [];
      for (let i = 0; 5 + items.join(",").length < width - 40; i++) items.push(`a${i}`);
      const text = `call(${items.join(",")}`;
      return `${text}${"x".repeat(width - text.length - 2)});`;
    };
    expect(call(240)).toHaveLength(240);
    expect(layoutOf(open(call(240), { path: "a.js" })).breaks).toEqual([]);
    expect(layoutOf(open(call(241), { path: "a.js" })).breaks.length).toBeGreaterThan(0);
  });

  it("breaks minified script at statements, wide groups and after blocks", () => {
    const source = 'function a(t,e){if(!t)throw new Error("missing");const n=new Map,r=e.items.filter(i=>i.ok);for(const i of r){n.set(i.id,i)}return n}export function b(t){return a(t,{items:[{id:1,ok:!0},{id:2,ok:!1}]})}const c={mode:"fast",retries:3,timeoutMs:30000,onError:function(t){console.error("failed",t)}};export default c;';
    expect(source.length).toBeGreaterThan(240);
    expect(pretty(source, "bundle.js")).toBe([
      "function a(t,e){",
      '  if(!t)throw new Error("missing");',
      "  const n=new Map,r=e.items.filter(i=>i.ok);",
      "  for(const i of r){n.set(i.id,i)}",
      "  return n",
      "}",
      "export function b(t){return a(t,{items:[{id:1,ok:!0},{id:2,ok:!1}]})}",
      "const c={",
      '  mode:"fast",',
      "  retries:3,",
      "  timeoutMs:30000,",
      '  onError:function(t){console.error("failed",t)}',
      "};",
      "export default c;",
    ].join("\n"));
  });

  it("keeps a continuation keyword after a block on its line", () => {
    const source = `if(a){${"x();".repeat(30)}}else{${"y();".repeat(30)}}try{z()}catch(e){w()}`;
    const written = pretty(source, "a.js");
    expect(written).toMatch(/^\}else\{$/m);
    expect(written).toMatch(/try\{z\(\)\}catch\(e\)\{w\(\)\}/);
  });

  it("starts every style rule on its own line", () => {
    const rules = ".btn{color:#fff;background:#0a66c2}.btn:hover{background:#004182}@media (max-width:600px){.btn{padding:4px 8px;font-size:12px;border-radius:2px}.nav{display:none}}:root{--gap:8px}";
    const source = rules + "a{b:c}".repeat(20);
    expect(source.length).toBeGreaterThan(240);
    expect(pretty(source, "site.css").split("\n").slice(0, 7)).toEqual([
      ".btn{color:#fff;background:#0a66c2}",
      ".btn:hover{background:#004182}",
      "@media (max-width:600px){",
      "  .btn{padding:4px 8px;font-size:12px;border-radius:2px}",
      "  .nav{display:none}",
      "}",
      ":root{--gap:8px}",
    ]);
  });

  it("breaks between adjacent tags on a wide markup line", () => {
    const source = `<ul>${'<li class="item">one</li>'.repeat(12)}</ul>`;
    const written = pretty(source, "page.html");
    expect(written.split("\n")).toEqual(["<ul>", ...Array.from({ length: 12 }, () => '  <li class="item">one</li>'), "</ul>"]);
  });

  it("indents synthetic lines from the real line's indentation by the detected unit", () => {
    const item = (i: number) => `{"id":${i},"tags":["a","b"]}`;
    for (const [lead, unit] of [["\t", "\t"], ["    ", "    "], ["  ", "  "]] as const) {
      const source = `{\n${lead}"items":[${item(1)},${item(2)}]\n}`;
      const state = open(source, { path: "data.json" }, unit);
      const leadColumns = lead === "\t" ? 4 : lead.length;
      const unitColumns = unit === "\t" ? 4 : unit.length;
      const { lines } = layoutOf(state);
      const columns = new Set(lines.map(([, , c]) => c));
      expect(columns).toEqual(new Set([0, 1, 2, 3].map((depth) => leadColumns + depth * unitColumns)));
    }
  });

  it("aligns items whether or not separators are followed by spaces", () => {
    for (const source of ['{"a": 1, "b": 2, "c": [1, 2]}', '{"a":1,"b":2,"c":[1,2]}']) {
      const written = pretty(source, "data.json").split("\n");
      expect(written.slice(1, 4).map((line) => line.length - line.trimStart().length)).toEqual([2, 2, 2]);
      expect(written.every((line) => !line.startsWith("   ") || line.startsWith("    "))).toBe(true);
    }
  });

  it("adds no break where the source already breaks more finely", () => {
    const source = "{\n  \"a\":\n    1,\n  \"b\": 2\n}";
    expect(layoutOf(open(source, { path: "data.json" })).breaks).toEqual([]);
  });

  it("only adds: every decoration is a break widget or an indentation mark", () => {
    const state = open('{"a":[1,2,{"b":"c"}]}', { path: "data.json" });
    state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
      if (from === to) expect(value.spec.widget).toBeInstanceOf(PrettyBreakWidget);
      else expect(value.spec.widget).toBeUndefined();
    });
  });

  it("adds nothing on a second pass over its written-out result", () => {
    const sources: Array<[string, string]> = [
      ['{"a":[1,2,{"b":"c","d":[{"e":1},{"f":[]}]}],"g":"h"}', "data.json"],
      ['function a(t,e){if(!t)throw new Error("x");const n=new Map,r=e.filter(i=>i);for(const i of r){n.set(i,i)}return n}' + "export const z=1;".repeat(20), "bundle.js"],
      [".a{b:c;d:e}.f:hover{g:h}@media (x){.i{j:k}.l{m:n}}" + ".o{p:q}".repeat(30), "s.css"],
      [`<a>${"<b><c>x</c></b>".repeat(20)}</a>`, "doc.xml"],
    ];
    for (const [source, path] of sources) {
      const once = pretty(source, path);
      expect(layoutOf(open(once, { path })).breaks, path).toEqual([]);
    }
  });
});

describe("recognition", () => {
  const laidOut = (text: string, config: Omit<PrettyConfig, "enabled"> = {}) => layoutOf(open(text, config)).breaks.length > 0;

  it("leaves unknown text without positive evidence alone", () => {
    for (const text of [
      "src/a.ts(12,5): error TS2345: Argument of type '{ id: number; }' is not assignable to parameter of type 'User'.",
      "FAIL src/x.test.ts > suite > case [1, 2, 3]\n  expected [ 1, 2 ] to equal [ 1, 3 ]",
      "Ship it (soon), then tell [the team], {maybe} tomorrow.",
      "key:\n  - [a, b]\n  - {c: d}",
      "def f(x):\n    return {'a': [1, 2]}",
      '{"unbalanced": [1, 2}',
      "{ not json at all }",
    ]) {
      expect(laidOut(text), text).toBe(false);
    }
  });

  it("lays out a JSON container at a line start or after a log prefix", () => {
    expect(laidOut('{"a":1,"b":[1,2]}')).toBe(true);
    expect(laidOut('2026-11-10T10:00:00Z INFO request {"id":7,"tags":["a"]}')).toBe(true);
    expect(laidOut('note: plain words\n[{"a":1},{"b":2}]\nmore words')).toBe(true);
  });

  it("uses a known path or language and refuses a path without a layout", () => {
    expect(prettyOutcomeOf(open('{"a":[1,2]}', { path: "notes.txt" })).kind).toBe("unavailable");
    expect(prettyOutcomeOf(open("package main\nfunc main() {}\n", { path: "main.go" })).kind).toBe("unavailable");
    expect(laidOut('{"a":[1,2]}', { languageHint: "json" })).toBe(true);
    expect(laidOut('x {"a":[1,2]}', { languageHint: "go" })).toBe(false);
  });

  it("lays out fenced regions by their language", () => {
    const md = "# Notes\n\n```json\n{\"a\":[1,2]}\n```\n\n```sh\necho {a,b}\n```\n";
    const state = open(md, { path: "notes.md" });
    const { breaks } = layoutOf(state);
    const fence = md.indexOf('{"a"');
    expect(breaks.length).toBeGreaterThan(0);
    expect(breaks.every((pos) => pos > fence && pos < md.indexOf("```", fence))).toBe(true);
  });
});

function oneLineJson(target: number): string {
  const items: string[] = [];
  let length = 2;
  for (let i = 0; length < target; i++) {
    const item = `{"id":${i},"name":"item ${i}","tags":["a","b"],"ok":${i % 2 === 0}}`;
    items.push(item);
    length += item.length + 1;
  }
  return `[${items.join(",")}]`;
}

function oneLineScript(target: number): string {
  const parts: string[] = [];
  let length = 0;
  for (let i = 0; length < target; i++) {
    const part = `function f${i}(a,b){const c=a.map(x=>x*${i}),d={k:${i},v:[a,b]};if(c.length){return d}return null}`;
    parts.push(part);
    length += part.length;
  }
  return parts.join("");
}

describe("bounded work", () => {
  it("rescans a bounded window after a one-character edit in a 1 MB line", () => {
    for (const [doc, path] of [[oneLineJson(1_000_000), "big.json"], [oneLineScript(1_000_000), "bundle.js"]] as const) {
      let state = open(doc, { path });
      configs.set(state, { enabled: true, path });
      expect(prettyOutcomeOf(state).kind).toBe("active");
      const middle = doc.indexOf("7", doc.length >> 1);
      state = edit(state, middle, middle + 1, "8");
      const work = prettyWorkOf(state);
      expect(work.visits, path).toBeLessThan(4 * PRETTY_CHECKPOINT_INTERVAL);
      expect(work.to - work.from, path).toBeLessThan(3 * PRETTY_CHECKPOINT_INTERVAL);
      expect(layoutOf(state), path).toEqual(rebuilt(state));
    }
  });

  it("formats a 2 MB document at the input cap and reports a larger one", () => {
    const atCap = oneLineJson(PRETTY_MAX_INPUT - 200);
    expect(atCap.length).toBeLessThanOrEqual(PRETTY_MAX_INPUT);
    expect(prettyOutcomeOf(open(atCap, { path: "big.json" })).kind).toBe("active");
    const over = oneLineJson(PRETTY_MAX_INPUT + 100);
    const outcome = prettyOutcomeOf(open(over, { path: "big.json" }));
    expect(outcome).toMatchObject({ kind: "limit", reason: "size" });
  });

  it("repeats no scan after a limit until the document is shorter", () => {
    const over = oneLineJson(PRETTY_MAX_INPUT + 100);
    let state = open(over, { path: "big.json" });
    configs.set(state, { enabled: true, path: "big.json" });
    state = edit(state, 1, 1, " ");
    expect(prettyOutcomeOf(state).kind).toBe("limit");
    expect(prettyWorkOf(state).visits).toBe(0);
    state = edit(state, 1, state.doc.length - PRETTY_MAX_INPUT + 1000, "");
    expect(prettyOutcomeOf(state).kind).toBe("active");
  });

  it("reports nesting beyond the depth limit", () => {
    const deep = "[".repeat(600) + "]".repeat(600);
    expect(prettyOutcomeOf(open(deep, { path: "deep.json" }))).toMatchObject({ kind: "limit", reason: "depth" });
  });
});

function random(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 0x100000000;
  };
}

describe("incremental layout", () => {
  const families: Array<[string, string, string[]]> = [
    ["data.json", oneLineJson(30_000), ["1", ",", "{", "}", "[", "]", '"', " ", "a", "\n"]],
    ["bundle.js", oneLineScript(30_000), ["x", ";", "{", "}", "(", ")", ",", '"', "`", "/", " ", "\n", "else"]],
    ["site.css", ".a{b:c;d:e}.f:hover{g:h}@media (x){.i{j:k}}".repeat(400), ["a", ";", "{", "}", ",", "/*", "*/", " ", "\n"]],
    ["page.html", `<ul>${'<li class="x">one</li><!-- c -->'.repeat(600)}</ul>`, ["<", ">", "/", "a", '"', " ", "\n", "<!--", "-->"]],
    ["notes.md", `# t\n\n\`\`\`json\n${oneLineJson(8_000)}\n\`\`\`\n\ntext {a}\n`, ["`", "{", "}", ",", "\n", "a"]],
  ];
  for (const [path, doc, tokens] of families) {
    it(`matches a full layout after random edits (${path})`, () => {
      const next = random(path.length * 7919);
      let state = open(doc, { path });
      configs.set(state, { enabled: true, path });
      for (let round = 0; round < 400; round++) {
        const length = state.doc.length;
        const from = Math.floor(next() * length);
        const to = Math.min(length, from + (next() < 0.3 ? Math.floor(next() * 6) : 0));
        const insert = next() < 0.8 ? tokens[Math.floor(next() * tokens.length)]! : "";
        state = edit(state, from, to, insert);
        if (round % 20 === 19 || round > 380) {
          expect(layoutOf(state), `${path} round ${round}`).toEqual(rebuilt(state));
        }
      }
    });
  }
});
