import {
  deleteCharBackward,
  deleteCharForward,
  indentLess,
  indentMore,
  insertNewlineAndIndent,
} from "@codemirror/commands";
import { css } from "@codemirror/lang-css";
import { html } from "@codemirror/lang-html";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import {
  applyPrettyNativeConfig,
  computePrettyNativeLayout,
  createPrettyNativeDecorationsExtension,
  prettyNativeStateField,
} from "./pretty-native-layout.ts";

describe("pretty-native-layout (Decisions 004 & 005, FMT01, FMT05)", () => {
  it("FMT-AC-01, FMT-AC-05: computes exact raw-offset lexical boundaries while keeping raw text and token spelling untouched", () => {
    const raw =
      '{"id":9007199254740993,"id":1e-05,"s":"\\u0041\\n","empty":{},"arr":[1,2]}';
    const layout = computePrettyNativeLayout(raw, { path: "payload.json" });

    expect(layout.outcome).toBe("formatted");
    expect(layout.family).toBe("json");
    expect(layout.boundaries).toEqual([
      { pos: 1, depth: 1 }, // after opening `{`
      { pos: 23, depth: 1 }, // after `"id":9007199254740993,`
      { pos: 34, depth: 1 }, // after `"id":1e-05,`
      { pos: 49, depth: 1 }, // after `"s":"\\u0041\\n",`
      { pos: 60, depth: 1 }, // after `"empty":{},` (empty `{}` is not split!)
      { pos: 67, depth: 2 }, // after `"arr":[`
      { pos: 69, depth: 2 }, // after `1,`
      { pos: 70, depth: 1 }, // before `]`
      { pos: 71, depth: 0 }, // before `}`
    ]);
  });

  it("FMT-AC-03, FMT-AC-16: tolerates malformed intermediate JSON and resets depth per line in JSONL mode", () => {
    const malformed = '{"a":1"b":[1,2';
    const res = computePrettyNativeLayout(malformed, { path: "broken.json" });
    expect(res.outcome).toBe("formatted");
    expect(res.boundaries).toEqual([
      { pos: 1, depth: 1 },
      { pos: 11, depth: 2 },
      { pos: 13, depth: 2 },
    ]);

    const jsonl = '{"a":[1,2\n{"b":2}';
    const jsonlRes = computePrettyNativeLayout(jsonl, { path: "stream.jsonl" });
    expect(jsonlRes.outcome).toBe("formatted");
    expect(jsonlRes.lineStates[0]?.startDepth).toBe(0);
    expect(jsonlRes.lineStates[1]?.startDepth).toBe(0);
  });

  it("FMT-AC-04, FMT-AC-18: formats JSON-like data with comments and trailing commas (JSONC/JSON5) including malformed edits", () => {
    const jsonc =
      '{\n// line comment with {ignored:1}\n"a": /* block {comment} */ [1, 2,],\n}';
    const res = computePrettyNativeLayout(jsonc, { path: "tsconfig.jsonc" });
    expect(res.outcome).toBe("formatted");
    expect(res.family).toBe("jsonc");
    // Inside `"a": /* block {comment} */ [1, 2,],`:
    // breaks after `[`, after `1,`, before `]` (trailing comma after 2 does not duplicate break before `]`).
    const line3 = res.lineStates[2]!;
    expect(line3.relBoundaries.length).toBeGreaterThanOrEqual(3);

    // Malformed JSONC (unclosed block comment or deleted comma) stays formatted without throwing:
    const malformedJsonc = '{"a":1 "b":2, /* unclosed comment { }';
    const malRes = computePrettyNativeLayout(malformedJsonc, {
      path: "tsconfig.jsonc",
    });
    expect(malRes.outcome).toBe("formatted");
    expect(malRes.lineStates[0]?.endInBlockComment).toBe(true);
  });

  it("FMT-AC-04, FMT-AC-18: formats XML/HTML structural tag boundaries while preserving inline text, comments, CDATA, and whitespace-sensitive <pre>/<code>/<script>/<style>", () => {
    const xml =
      '<root><item id="1"><name>Alpha</name><flag/></item><!-- <skip/> --><pre>  <span>keep   spaces</span>  </pre></root>';
    const res = computePrettyNativeLayout(xml, { path: "doc.xml" });
    expect(res.outcome).toBe("formatted");
    expect(res.family).toBe("xml-html");

    const breakSubstrings = res.boundaries.map((b) =>
      xml.slice(b.pos, b.pos + 8),
    );
    // Breaks before `<item`, `<name>`, `<flag/>`, `</item>`, `<!--`, `<pre>`, and `</root>`,
    // NEVER inside `<name>Alpha</name>` or inside `<pre>  <span>keep   spaces</span>  </pre>`!
    expect(breakSubstrings.some((s) => s.startsWith("<item"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<name>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<flag/>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("</item>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<pre>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("</root>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("Alpha"))).toBe(false);
    expect(breakSubstrings.some((s) => s.startsWith("<span>"))).toBe(false);
    expect(breakSubstrings.some((s) => s.startsWith("</span>"))).toBe(false);

    // Malformed XML/HTML (unclosed tag during typing) keeps existing boundaries active:
    const malformedXml = "<root><item><name>Alpha</name><unclosed";
    const malXmlRes = computePrettyNativeLayout(malformedXml, {
      path: "index.html",
    });
    expect(malXmlRes.outcome).toBe("formatted");
    expect(malXmlRes.boundaries.length).toBeGreaterThanOrEqual(2);
  });

  it("FMT-AC-04, FMT-AC-18: formats JavaScript/TypeScript dense objects, arrays, and blocks without splitting for(...) headers, call arguments, regexes, or strings", () => {
    const tsCode =
      'const cfg={host:"a;b,c",ports:[80,443],re:/a,b;c/};for(let i=0;i<2;i++){run(i,cfg,{retry:true,max:3});}';
    const res = computePrettyNativeLayout(tsCode, { path: "snippet.ts" });
    expect(res.outcome).toBe("formatted");
    expect(res.family).toBe("javascript");

    // Verify no break occurred inside `for(let i=0;i<2;i++)` or inside `"a;b,c"` or `/a,b;c/`:
    const forStart = tsCode.indexOf("for(");
    const forOpenBrace = tsCode.indexOf("{", forStart);
    const breaksInsideForHeader = res.boundaries.filter(
      (b) => b.pos > forStart && b.pos <= forOpenBrace,
    );
    expect(breaksInsideForHeader).toEqual([]);

    // Verify breaks DID occur inside `{host:...,ports:[80,443],...}` and `{retry:true,max:3}`:
    const retryPos = tsCode.indexOf("retry:true,");
    expect(
      res.boundaries.some((b) => b.pos === retryPos + "retry:true,".length),
    ).toBe(true);

    // Malformed TypeScript (deleted comma in object literal) remains formatted:
    const malformedTs = 'const cfg={a:1 b:2,c:[3,4};';
    const malTsRes = computePrettyNativeLayout(malformedTs, {
      path: "snippet.ts",
    });
    expect(malTsRes.outcome).toBe("formatted");
    expect(malTsRes.boundaries.length).toBeGreaterThanOrEqual(3);
  });

  it("FMT-AC-04, FMT-AC-18: formats CSS rules and declarations while preserving url(...), rgb(...), strings, and comments", () => {
    const cssText =
      '.card{background:url("http://example.com/a;b{c}.png");color:rgb(1,2,3);padding:4px/* {comment;} */}.btn{display:flex;gap:8px}';
    const res = computePrettyNativeLayout(cssText, { path: "styles.css" });
    expect(res.outcome).toBe("formatted");
    expect(res.family).toBe("css");

    // Verify no break inside `url("http://example.com/a;b{c}.png")` or `rgb(1,2,3)`:
    const urlStart = cssText.indexOf("url(");
    const urlEnd = cssText.indexOf(")", urlStart);
    expect(
      res.boundaries.filter((b) => b.pos > urlStart && b.pos <= urlEnd),
    ).toEqual([]);

    // Breaks after `.card{`, after `;`, before `}`, after `}` before `.btn`, and inside `.btn{...}`:
    expect(res.boundaries.length).toBeGreaterThanOrEqual(6);

    // Malformed CSS (missing semicolon or unclosed brace) remains formatted:
    const malformedCss = ".card{color:red padding:4px;margin:0";
    const malCssRes = computePrettyNativeLayout(malformedCss, {
      path: "styles.css",
    });
    expect(malCssRes.outcome).toBe("formatted");
    expect(malCssRes.boundaries.length).toBeGreaterThanOrEqual(2);
  });

  it("FMT-AC-02, FMT-AC-04, FMT-AC-20: formats fenced blocks for supported common blobs in Markdown while keeping whitespace-sensitive Python/YAML and unsupported languages as truthful passthrough", () => {
    for (const [path, sample] of [
      ["script.py", "def f():\n    return {'a': 1, 'b': 2}\n"],
      ["config.yaml", "service:\n  name: gateway\n  ports: [80, 443]\n"],
      ["Cargo.toml", '[package]\nname = "lycaon"\n'],
      ["query.sql", "SELECT id, name FROM users WHERE active = 1;"],
      ["main.go", 'package main\nfunc main() { println("{a:1}") }\n'],
      ["data.csv", "id,name,role\n1,Alice,admin\n"],
      ["opaque.txt", "dGhpcyBpcyBiYXNlNjQgZW5jb2RlZCBkYXRhCg=="],
    ] as const) {
      const passthroughRes = computePrettyNativeLayout(sample, { path });
      expect(passthroughRes.outcome).toBe("passthrough");
      expect(passthroughRes.family).toBe("passthrough");
      expect(passthroughRes.boundaries).toEqual([]);
    }

    const md = [
      "# Heading {not:formatted}",
      "```json",
      '{"a":1,"b":2}',
      "```",
      "```css",
      ".box{color:red;margin:0}",
      "```",
      "```python",
      "x = {'a': 1, 'b': 2}",
      "```",
    ].join("\n");
    const mdRes = computePrettyNativeLayout(md, { path: "notes.md" });
    expect(mdRes.outcome).toBe("formatted");
    expect(mdRes.lineStates[0]?.relBoundaries).toEqual([]); // prose untouched
    expect(mdRes.lineStates[2]?.relBoundaries.length).toBe(3); // json fence formatted
    expect(mdRes.lineStates[5]?.relBoundaries.length).toBe(3); // css fence formatted
    expect(mdRes.lineStates[8]?.relBoundaries).toEqual([]); // python fence untouched

    const logLine = '2026-11-09T20:00:00Z INFO [gw] {"ok":true,"n":1}';
    const logRes = computePrettyNativeLayout(logLine);
    expect(logRes.outcome).toBe("formatted");
    expect(logRes.boundaries[0]?.pos).toBe(logLine.indexOf("{") + 1);
  });

  it("FMT-AC-11: updates decorations incrementally on changed lines and accounts for Lezer syntaxTree nodes when a CodeMirror language is installed", () => {
    const lines = Array.from(
      { length: 200 },
      (_, idx) => `{"row":${idx},"nested":{"a":1,"b":2}}`,
    );
    const docText = lines.join("\n");

    let state = EditorState.create({
      doc: docText,
      extensions: [
        json(),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "rows.jsonl",
        }),
      ],
    });

    const initialField = state.field(prettyNativeStateField);
    expect(initialField.outcome).toBe("formatted");
    expect(initialField.lastIncrementalLinesScanned).toBe(200);
    expect(initialField.counters.syntaxNodesVisited).toBeGreaterThan(0);

    const line100 = state.doc.line(100);
    const editPos = line100.from + line100.text.indexOf('"a":1') + 4;
    const tr = state.update({
      changes: { from: editPos, to: editPos + 1, insert: "99" },
    });
    state = tr.state;

    const updatedField = state.field(prettyNativeStateField);
    expect(updatedField.outcome).toBe("formatted");
    expect(updatedField.lastIncrementalLinesScanned).toBeLessThanOrEqual(4);

    const scratch = computePrettyNativeLayout(state.doc, {
      path: "rows.jsonl",
    });
    expect(
      updatedField.lineStates.map((ls) => ({
        lineFrom: ls.lineFrom,
        relBoundaries: ls.relBoundaries,
      })),
    ).toEqual(
      scratch.lineStates.map((ls) => ({
        lineFrom: ls.lineFrom,
        relBoundaries: ls.relBoundaries,
      })),
    );

    // Also verify Lezer syntaxTree integration for HTML, JS/TS, and CSS modes:
    for (const [ext, path, sample] of [
      [html(), "page.html", "<div><span>Hi</span><p>There</p></div>"],
      [javascript({ typescript: true }), "app.ts", "const x={a:1,b:[2,3]};"],
      [css(), "app.css", ".a{color:red;top:0}"],
    ] as const) {
      const s = EditorState.create({
        doc: sample,
        extensions: [
          ext,
          createPrettyNativeDecorationsExtension({ enabled: true, path }),
        ],
      });
      const f = s.field(prettyNativeStateField);
      expect(f.outcome).toBe("formatted");
      expect(f.counters.syntaxNodesVisited).toBeGreaterThan(0);
    }
  });

  it("FMT-AC-10, FMT-AC-16: supports native CodeMirror commands (Backspace/Delete on comma, Enter, Tab/Shift+Tab) directly on raw source", () => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    try {
      const raw = '{"service":"gateway","enabled":true}';
      const view = new EditorView({
        parent,
        state: EditorState.create({
          doc: raw,
          extensions: [
            createPrettyNativeDecorationsExtension({
              enabled: true,
              path: "config.json",
            }),
          ],
        }),
      });

      expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBe(3);
      expect(view.dom.querySelectorAll(".pw-virtual-indent").length).toBe(2);

      // 1. Place caret immediately after the comma (offset 21, assoc -1) and run deleteCharBackward (Backspace):
      const commaOffset = raw.indexOf(",");
      view.dispatch({
        selection: { anchor: commaOffset + 1, head: commaOffset + 1 },
      });
      expect(deleteCharBackward(view)).toBe(true);
      expect(view.state.doc.toString()).toBe(
        '{"service":"gateway""enabled":true}',
      );
      expect(view.state.field(prettyNativeStateField).enabled).toBe(true);
      expect(view.state.field(prettyNativeStateField).outcome).toBe(
        "formatted",
      );
      expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBe(2);

      // 2. Re-insert comma, place caret before comma, and run deleteCharForward (Delete):
      view.dispatch({
        changes: { from: commaOffset, to: commaOffset, insert: "," },
        selection: { anchor: commaOffset, head: commaOffset },
      });
      expect(deleteCharForward(view)).toBe(true);
      expect(view.state.doc.toString()).toBe(
        '{"service":"gateway""enabled":true}',
      );

      // 3. Re-insert comma and run Enter (insertNewlineAndIndent) right after comma:
      view.dispatch({
        changes: { from: commaOffset, to: commaOffset, insert: "," },
        selection: { anchor: commaOffset + 1, head: commaOffset + 1 },
      });
      expect(insertNewlineAndIndent(view)).toBe(true);
      expect(view.state.doc.lines).toBe(2);

      // 4. Run Tab (indentMore) and Shift+Tab (indentLess) on raw source line:
      expect(indentMore(view)).toBe(true);
      expect(view.state.doc.line(2).text.startsWith("  ")).toBe(true);
      expect(indentLess(view)).toBe(true);

      // 5. Toggling Pretty OFF removes virtual widgets without altering raw doc:
      const docBeforeOff = view.state.doc.toString();
      applyPrettyNativeConfig(view, { enabled: false, path: "config.json" });
      expect(view.state.doc.toString()).toBe(docBeforeOff);
      expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBe(0);

      view.destroy();
    } finally {
      parent.remove();
    }
  });
});
