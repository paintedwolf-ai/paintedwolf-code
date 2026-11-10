// @vitest-environment jsdom
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
import { forceParsing, syntaxTreeAvailable } from "@codemirror/language";
import { Compartment, EditorState } from "@codemirror/state";
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
    const line3 = res.lineStates[2]!;
    expect(line3.relBoundaries.length).toBeGreaterThanOrEqual(3);

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
    expect(breakSubstrings.some((s) => s.startsWith("<item"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<name>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<flag/>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("</item>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("<pre>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("</root>"))).toBe(true);
    expect(breakSubstrings.some((s) => s.startsWith("Alpha"))).toBe(false);
    expect(breakSubstrings.some((s) => s.startsWith("<span>"))).toBe(false);
    expect(breakSubstrings.some((s) => s.startsWith("</span>"))).toBe(false);

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

    const forStart = tsCode.indexOf("for(");
    const forOpenBrace = tsCode.indexOf("{", forStart);
    const breaksInsideForHeader = res.boundaries.filter(
      (b) => b.pos > forStart && b.pos <= forOpenBrace,
    );
    expect(breaksInsideForHeader).toEqual([]);

    const retryPos = tsCode.indexOf("retry:true,");
    expect(
      res.boundaries.some((b) => b.pos === retryPos + "retry:true,".length),
    ).toBe(true);

    const malformedTs = "const cfg={a:1 b:2,c:[3,4};";
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

    const urlStart = cssText.indexOf("url(");
    const urlEnd = cssText.indexOf(")", urlStart);
    expect(
      res.boundaries.filter((b) => b.pos > urlStart && b.pos <= urlEnd),
    ).toEqual([]);
    expect(res.boundaries.length).toBeGreaterThanOrEqual(6);

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
    expect(mdRes.lineStates[0]?.relBoundaries).toEqual([]);
    expect(mdRes.lineStates[2]?.relBoundaries.length).toBe(3);
    expect(mdRes.lineStates[5]?.relBoundaries.length).toBe(3);
    expect(mdRes.lineStates[8]?.relBoundaries).toEqual([]);

    const logLine = '2026-11-09T20:00:00Z INFO [gw] {"ok":true,"n":1}';
    const logRes = computePrettyNativeLayout(logLine);
    expect(logRes.outcome).toBe("formatted");
    expect(logRes.boundaries[0]?.pos).toBe(logLine.indexOf("{") + 1);
  });

  it("FMT-AC-04, FMT-AC-11: preserves multiline template literals, multiline parenthesized calls, multiline XML CDATA/comments, and regexes across lines and incremental edits", () => {
    // 1. Multiline JS template literal with `{`, `,`, `}` and nested `${`inner`}` +
    // multiline parenthesized call + return regex:
    const jsDoc = [
      "const tpl = `header ${`inner`} start",
      '{"literal":1,"ignored":2}',
      "footer`;",
      "callFn(",
      "  firstArg, secondArg,",
      "  {nestedA:1,nestedB:2},",
      "  thirdArg, fourthArg",
      ");",
      "function check(){return /a,b{1,2}/;}",
      "const tail = {x:10,y:20};",
    ].join("\n");

    let jsState = EditorState.create({
      doc: jsDoc,
      extensions: [
        javascript({ typescript: true }),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "multiline.ts",
        }),
      ],
    });

    const f0 = jsState.field(prettyNativeStateField);
    expect(f0.outcome).toBe("formatted");
    // Line 2 (`{"literal":1,"ignored":2}`) is inside multiline backtick template -> 0 boundaries!
    expect(f0.lineStates[0]?.endQuoteCode).toBe(0x60);
    expect(f0.lineStates[1]?.startQuoteCode).toBe(0x60);
    expect(f0.lineStates[1]?.relBoundaries).toEqual([]);
    expect(f0.lineStates[2]?.endQuoteCode).toBe(0);

    // Line 5 (`  firstArg, secondArg,`) and Line 7 (`  thirdArg, fourthArg`) are directly inside `callFn(` -> 0 boundaries!
    expect(f0.lineStates[3]?.endDelimSignature).toBe("(");
    expect(f0.lineStates[4]?.startDelimSignature).toBe("(");
    expect(f0.lineStates[4]?.relBoundaries).toEqual([]);
    // Line 6 (`  {nestedA:1,nestedB:2},`) has an inner object `{...}` inside `callFn(` -> formats `{nestedA:1,nestedB:2}`!
    expect(f0.lineStates[5]?.relBoundaries.length).toBe(3);
    expect(f0.lineStates[6]?.relBoundaries).toEqual([]);
    expect(f0.lineStates[7]?.endDelimSignature).toBe("");

    // Line 9 (`function check(){return /a,b{1,2}/;}`) formats `{` and `;}` without splitting inside `/a,b{1,2}/`:
    expect(f0.lineStates[8]?.relBoundaries.length).toBe(2);
    // Line 10 (`const tail = {x:10,y:20};`) formats `{x:10,y:20}`:
    expect(f0.lineStates[9]?.relBoundaries.length).toBe(3);
    const tailLineStateBefore = f0.lineStates[9];

    // Incremental edit inside Line 2 of the template literal converges and reuses suffix line 10 by reference:
    const line2 = jsState.doc.line(2);
    jsState = jsState.update({
      changes: {
        from: line2.from + 2,
        to: line2.from + 9,
        insert: "updated",
      },
    }).state;
    const f1 = jsState.field(prettyNativeStateField);
    expect(f1.lineStates[1]?.relBoundaries).toEqual([]);
    expect(f1.lastIncrementalLinesScanned).toBeLessThanOrEqual(3);
    expect(f1.lineStates[9]).toBe(tailLineStateBefore);
    const jsScratch = computePrettyNativeLayout(
      jsState.doc,
      { path: "multiline.ts" },
      jsState,
    );
    expect(f1.lineStates).toEqual(jsScratch.lineStates);
    expect(f1.counters).toEqual(jsScratch.counters);

    // 2. Multiline XML CDATA and multiline XML comments:
    const xmlDoc = [
      "<root><item><![CDATA[",
      "<a><b>literal-not-markup</b></a>",
      "]]></item><!--",
      "<c><d>commented-out</d></c>",
      "--><after><x>1</x><y>2</y></after></root>",
    ].join("\n");

    let xmlState = EditorState.create({
      doc: xmlDoc,
      extensions: [
        html(),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "cdata.xml",
        }),
      ],
    });
    const xf0 = xmlState.field(prettyNativeStateField);
    expect(xf0.outcome).toBe("formatted");
    // Line 1 enters CDATA (`endInXmlCdata === true`):
    expect(xf0.lineStates[0]?.endInXmlCdata).toBe(true);
    // Line 2 (`<a><b>literal-not-markup</b></a>`) is inside CDATA -> 0 boundaries!
    expect(xf0.lineStates[1]?.startInXmlCdata).toBe(true);
    expect(xf0.lineStates[1]?.endInXmlCdata).toBe(true);
    expect(xf0.lineStates[1]?.relBoundaries).toEqual([]);
    // Line 3 closes `]]></item>` and opens `<!--` (`endInBlockComment === true`):
    expect(xf0.lineStates[2]?.endInXmlCdata).toBe(false);
    expect(xf0.lineStates[2]?.endInBlockComment).toBe(true);
    // Line 4 (`<c><d>commented-out</d></c>`) is inside XML comment -> 0 boundaries!
    expect(xf0.lineStates[3]?.startInBlockComment).toBe(true);
    expect(xf0.lineStates[3]?.relBoundaries).toEqual([]);
    // Line 5 (`--><after><x>1</x><y>2</y></after></root>`) formats normal XML tags after `-->`:
    expect(xf0.lineStates[4]?.relBoundaries.length).toBeGreaterThanOrEqual(4);

    // Incremental edit on Line 2 inside CDATA keeps Line 2 unformatted and matches fresh construction:
    const xmlLine2 = xmlState.doc.line(2);
    xmlState = xmlState.update({
      changes: {
        from: xmlLine2.from + 6,
        to: xmlLine2.from + 13,
        insert: "changed",
      },
    }).state;
    const xf1 = xmlState.field(prettyNativeStateField);
    expect(xf1.lineStates[1]?.relBoundaries).toEqual([]);
    const xmlScratch = computePrettyNativeLayout(
      xmlState.doc,
      { path: "cdata.xml" },
      xmlState,
    );
    expect(xf1.lineStates).toEqual(xmlScratch.lineStates);
    expect(xf1.counters).toEqual(xmlScratch.counters);
  });

  it("FMT-AC-07, FMT-AC-11: enforces aggregate document-wide resource limits across incremental edits (non-ASCII UTF-8 growth & cumulative synthetic rows) identically to fresh construction", () => {
    // 1. Repeated non-ASCII UTF-8 growth against `maxInputUtf8Bytes`:
    const initialJson = ['{"row":1,"msg":"aa"}', '{"row":2,"msg":"bb"}'].join(
      "\n",
    );
    const utf8Limits = { maxInputUtf8Bytes: 52 };
    let utf8State = EditorState.create({
      doc: initialJson,
      extensions: [
        json(),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "utf8.json",
          limits: utf8Limits,
        }),
      ],
    });
    expect(utf8State.field(prettyNativeStateField).outcome).toBe("formatted");

    // Insert "🎉🎉" (8 UTF-8 bytes, 4 UTF-16 units) on line 1 -> stays within 52 UTF-8 bytes (41 + 8 = 49 <= 52):
    const pos1 = utf8State.doc.line(1).from + 17;
    utf8State = utf8State.update({
      changes: { from: pos1, to: pos1, insert: "🎉🎉" },
    }).state;
    const step1Field = utf8State.field(prettyNativeStateField);
    const step1Fresh = computePrettyNativeLayout(
      utf8State.doc,
      { path: "utf8.json", limits: utf8Limits },
      utf8State,
    );
    expect(step1Field.outcome).toBe("formatted");
    expect(step1Field.counters).toEqual(step1Fresh.counters);

    // Insert another "🎉" (4 UTF-8 bytes -> 49 + 4 = 53 > 52) on line 2 ->
    // must transition to limit-exceeded (maxInputUtf8Bytes):
    const pos2 = utf8State.doc.line(2).from + 17;
    utf8State = utf8State.update({
      changes: { from: pos2, to: pos2, insert: "🎉" },
    }).state;
    const step2Field = utf8State.field(prettyNativeStateField);
    const step2Fresh = computePrettyNativeLayout(
      utf8State.doc,
      { path: "utf8.json", limits: utf8Limits },
      utf8State,
    );
    expect(step2Field.outcome).toBe("limit-exceeded");
    expect(step2Field.limitReason).toBe("maxInputUtf8Bytes");
    expect(step2Fresh.outcome).toBe("limit-exceeded");
    expect(step2Fresh.limitReason).toBe("maxInputUtf8Bytes");

    // 2. Cumulative synthetic-row growth across multiple edits against `maxOutputLines`:
    // Initial doc has 2 physical lines + 6 synthetic breaks (3 per line) = 8 outputLines.
    const rowLimits = { maxOutputLines: 9 };
    let rowState = EditorState.create({
      doc: ['{"a":1,"b":2}', '{"c":3,"d":4}'].join("\n"),
      extensions: [
        json(),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "rows.jsonl",
          limits: rowLimits,
        }),
      ],
    });
    expect(rowState.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(rowState.field(prettyNativeStateField).counters.outputLines).toBe(8);

    // Edit 1: Add `"e":5` on line 1 -> adds 1 synthetic break -> total outputLines = 9 (<= 9):
    const line1EndBrace = rowState.doc.line(1).to - 1;
    rowState = rowState.update({
      changes: { from: line1EndBrace, to: line1EndBrace, insert: ',"e":5' },
    }).state;
    const rowStep1 = rowState.field(prettyNativeStateField);
    const rowFresh1 = computePrettyNativeLayout(
      rowState.doc,
      { path: "rows.jsonl", limits: rowLimits },
      rowState,
    );
    expect(rowStep1.outcome).toBe("formatted");
    expect(rowStep1.counters.outputLines).toBe(9);
    expect(rowStep1.counters).toEqual(rowFresh1.counters);

    // Edit 2: Add `"f":6` on line 2 -> adds 1 more synthetic break -> total outputLines = 10 (> 9):
    const line2EndBrace = rowState.doc.line(2).to - 1;
    rowState = rowState.update({
      changes: { from: line2EndBrace, to: line2EndBrace, insert: ',"f":6' },
    }).state;
    const rowStep2 = rowState.field(prettyNativeStateField);
    const rowFresh2 = computePrettyNativeLayout(
      rowState.doc,
      { path: "rows.jsonl", limits: rowLimits },
      rowState,
    );
    expect(rowStep2.outcome).toBe("limit-exceeded");
    expect(rowStep2.limitReason).toBe("maxOutputLines");
    expect(rowFresh2.outcome).toBe("limit-exceeded");
    expect(rowFresh2.limitReason).toBe("maxOutputLines");
  });

  it("FMT-AC-11: updates decorations incrementally on changed lines, reuses suffix line states by reference, and accounts for Lezer syntaxTree nodes", () => {
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
    expect(updatedField.lastIncrementalLinesScanned).toBeLessThanOrEqual(3);
    expect(
      updatedField.lastIncrementalSuffixLinesReused,
    ).toBeGreaterThanOrEqual(99);
    // Unchanged prefix and suffix lineStates are reused by reference without reallocation:
    expect(updatedField.lineStates[0]).toBe(initialField.lineStates[0]);
    expect(updatedField.lineStates[150]).toBe(initialField.lineStates[150]);

    const scratch = computePrettyNativeLayout(
      state.doc,
      { path: "rows.jsonl" },
      state,
    );
    expect(updatedField.lineStates).toEqual(scratch.lineStates);
    expect(updatedField.counters).toEqual(scratch.counters);

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

      view.dispatch({
        changes: { from: commaOffset, to: commaOffset, insert: "," },
        selection: { anchor: commaOffset, head: commaOffset },
      });
      expect(deleteCharForward(view)).toBe(true);
      expect(view.state.doc.toString()).toBe(
        '{"service":"gateway""enabled":true}',
      );

      view.dispatch({
        changes: { from: commaOffset, to: commaOffset, insert: "," },
        selection: { anchor: commaOffset + 1, head: commaOffset + 1 },
      });
      expect(insertNewlineAndIndent(view)).toBe(true);
      expect(view.state.doc.lines).toBe(2);

      expect(indentMore(view)).toBe(true);
      expect(view.state.doc.line(2).text.startsWith("  ")).toBe(true);
      expect(indentLess(view)).toBe(true);

      const docBeforeOff = view.state.doc.toString();
      applyPrettyNativeConfig(view, { enabled: false, path: "config.json" });
      expect(view.state.doc.toString()).toBe(docBeforeOff);
      expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBe(0);

      view.destroy();
    } finally {
      parent.remove();
    }
  });

  it("FMT-AC-05, FMT-AC-10: protected Lezer RegExp/String/Comment spans with interior quotes or comment markers never mutate fallback lexical state across fresh and incremental updates", () => {
    const cases = [
      'const r=/"/;const x={a:1,b:2};',
      "const r=/'/;const x={a:1,b:2};",
      "const r=/`/;const x={a:1,b:2};",
      "const r=/\\/*/;const x={a:1,b:2};",
      "const r=/\\/\\//;const x={a:1,b:2};",
    ];

    for (const raw of cases) {
      let state = EditorState.create({
        doc: raw,
        extensions: [
          javascript(),
          createPrettyNativeDecorationsExtension({
            enabled: true,
            path: "regex-protected.js",
          }),
        ],
      });

      const field = state.field(prettyNativeStateField);
      expect(field.outcome).toBe("formatted");
      expect(field.counters.syntaxNodesVisited).toBeGreaterThan(0);

      const semiIdx = raw.indexOf(";") + 1;
      const openBraceIdx = raw.indexOf("{", semiIdx) + 1;
      const commaIdx = raw.indexOf(",", openBraceIdx) + 1;
      const closeBraceIdx = raw.indexOf("}", commaIdx);
      expect(field.lineStates[0]?.relBoundaries).toEqual([
        { relPos: semiIdx, depth: 0 },
        { relPos: openBraceIdx, depth: 1 },
        { relPos: commaIdx, depth: 1 },
        { relPos: closeBraceIdx, depth: 0 },
      ]);

      // Verify incremental update matches fresh computePrettyNativeLayout:
      const multiRaw = `${raw}\nconst tail={c:3,d:4};`;
      state = EditorState.create({
        doc: multiRaw,
        extensions: [
          javascript(),
          createPrettyNativeDecorationsExtension({
            enabled: true,
            path: "regex-protected.js",
          }),
        ],
      });
      const valIdx = multiRaw.indexOf("a:1") + 2;
      state = state.update({
        changes: { from: valIdx, to: valIdx + 1, insert: "99" },
      }).state;

      const updatedField = state.field(prettyNativeStateField);
      const fresh = computePrettyNativeLayout(
        state.doc,
        { path: "regex-protected.js" },
        state,
      );
      expect(updatedField.lineStates).toEqual(fresh.lineStates);
      expect(updatedField.counters).toEqual(fresh.counters);
      expect(updatedField.lineStates[1]?.relBoundaries.length).toBe(3);
    }
  });

  it("FMT-AC-09, FMT-AC-10: invalidates and updates layout across intervening blank/unchanged lines when an earlier edit changes later syntax context and on language-compartment reconfiguration", () => {
    const langCompartment = new Compartment();
    const raw = [
      'const a=1 / 2;const b="/* not a comment */";const c={x:1,y:2};',
      'const d=/"/;const e={m:3,n:4};',
    ].join("\n");

    let state = EditorState.create({
      doc: raw,
      extensions: [
        langCompartment.of([]),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "reconfig.js",
        }),
      ],
    });

    const beforeParse = state.field(prettyNativeStateField);
    expect(beforeParse.counters.syntaxNodesVisited).toBe(0);
    expect(beforeParse.lineStates[0]?.syntaxSpanSignature).toBe("");

    // Reconfigure language compartment without docChanged (exercises treeChanged invalidation path):
    state = state.update({
      effects: langCompartment.reconfigure(javascript()),
    }).state;

    const afterParse = state.field(prettyNativeStateField);
    expect(afterParse.counters.syntaxNodesVisited).toBeGreaterThan(0);
    expect(afterParse.lineStates[0]?.syntaxSpanSignature).not.toBe("");
    expect(afterParse.lineStates[1]?.syntaxSpanSignature).not.toBe("");

    const freshAfterParse = computePrettyNativeLayout(
      state.doc,
      { path: "reconfig.js" },
      state,
    );
    expect(afterParse.lineStates).toEqual(freshAfterParse.lineStates);
    expect(afterParse.counters).toEqual(freshAfterParse.counters);

    // Multi-line syntax invalidation across intervening blank and unchanged lines:
    // Line 1: "const v = 1 < 2"
    // Line 2: 'id = "x" > c;'
    // Line 3: "" (blank intervening line)
    // Line 4: "const unchanged = 42;" (unchanged intervening line)
    // Line 5: "" (blank intervening line)
    // Line 6: 'if (cond) /; const x = {a: 1, b: 2};/i;' (affected JSX body line)
    // Line 7: "</tag>; // /"
    // Line 8: "const mid = 1;" (normalizes lastSigCode to `;` after Line 7)
    // Line 9: "const suffix = {m: 1, n: 2};" (unaffected suffix line reused by reference)
    const jsxDoc = [
      "const v = 1 < 2",
      'id = "x" > c;',
      "",
      "const unchanged = 42;",
      "",
      "if (cond) /; const x = {a: 1, b: 2};/i;",
      "</tag>; // /",
      "const mid = 1;",
      "const suffix = {m: 1, n: 2};",
    ].join("\n");
    let jsxState = EditorState.create({
      doc: jsxDoc,
      extensions: [
        javascript({ jsx: true }),
        createPrettyNativeDecorationsExtension({
          enabled: true,
          path: "comp.jsx",
        }),
      ],
    });
    const beforeField = jsxState.field(prettyNativeStateField);
    const sigLine6Before = beforeField.lineStates[5]?.syntaxSpanSignature;
    const suffixRefBefore = beforeField.lineStates[8];
    expect(beforeField.lineStates[5]?.relBoundaries.length).toBe(0);

    // Direction 1: Edit Line 1 ("const v = 1 < 2" -> "const v = <tag") across blank/unchanged Lines 3-5:
    jsxState = jsxState.update({
      changes: {
        from: 0,
        to: "const v = 1 < 2".length,
        insert: "const v = <tag",
      },
    }).state;
    const jsxAfterToTag = jsxState.field(prettyNativeStateField);
    const jsxFreshToTag = computePrettyNativeLayout(
      jsxState.doc,
      { path: "comp.jsx" },
      jsxState,
    );
    expect(jsxAfterToTag.lineStates[5]?.syntaxSpanSignature).not.toBe(
      sigLine6Before,
    );
    expect(jsxAfterToTag.lineStates[5]?.relBoundaries.length).toBe(5);
    expect(jsxAfterToTag.lineStates).toEqual(jsxFreshToTag.lineStates);
    expect(jsxAfterToTag.counters).toEqual(jsxFreshToTag.counters);
    expect(jsxAfterToTag.lastIncrementalSuffixLinesReused).toBe(1);
    expect(jsxAfterToTag.lineStates[8]).toBe(suffixRefBefore);

    // Direction 2: Edit Line 1 back ("const v = <tag" -> "const v = 1 < 2") across blank/unchanged Lines 3-5:
    jsxState = jsxState.update({
      changes: {
        from: 0,
        to: "const v = <tag".length,
        insert: "const v = 1 < 2",
      },
    }).state;
    const jsxAfterToExpr = jsxState.field(prettyNativeStateField);
    const jsxFreshToExpr = computePrettyNativeLayout(
      jsxState.doc,
      { path: "comp.jsx" },
      jsxState,
    );
    expect(jsxAfterToExpr.lineStates[5]?.syntaxSpanSignature).toBe(
      sigLine6Before,
    );
    expect(jsxAfterToExpr.lineStates[5]?.relBoundaries.length).toBe(0);
    expect(jsxAfterToExpr.lineStates).toEqual(jsxFreshToExpr.lineStates);
    expect(jsxAfterToExpr.counters).toEqual(jsxFreshToExpr.counters);
    expect(jsxAfterToExpr.lastIncrementalSuffixLinesReused).toBe(1);
    expect(jsxAfterToExpr.lineStates[8]).toBe(suffixRefBefore);
  });

  it("FMT-AC-09, FMT-AC-10: updates layout when CodeMirror partial initial parse (doc.length > 3000) advances to full syntaxTree completion without docChanged", () => {
    // CodeMirror's LanguageState.init stops initial synchronous parsing at Work.InitViewport (3000 chars).
    // Build a >3,500-char JS document so the tail line after offset 3,000 is initially unparsed by Lezer:
    const prefixLines = Array.from(
      { length: 70 },
      (_, i) => `const padLine_${i} = {alpha: ${i}, beta: ${i + 1}, gamma: "padding_value_${i}"};`,
    );
    const tailLine = 'if (cond) /"/; const tailObj = {a: 1, b: 2};';
    const largeDoc = [...prefixLines, tailLine].join("\n");
    expect(largeDoc.length).toBeGreaterThan(3200);

    const parent = document.createElement("div");
    const view = new EditorView({
      parent,
      state: EditorState.create({
        doc: largeDoc,
        extensions: [
          javascript(),
          createPrettyNativeDecorationsExtension({
            enabled: true,
            path: "partial-parse.js",
          }),
        ],
      }),
    });

    try {
      const tailIndex = view.state.doc.lines - 1;
      const initialFullParseAvailable = syntaxTreeAvailable(
        view.state,
        view.state.doc.length,
      );
      expect(initialFullParseAvailable).toBe(false);

      const fieldBeforeFullParse = view.state.field(prettyNativeStateField);
      // Before Lezer parses past offset 3,000, the tail line has no syntax-tree protected spans
      // (`/"/` after `if (cond)` is seen by fallback lexical scan as division followed by an unclosed quote):
      expect(
        fieldBeforeFullParse.lineStates[tailIndex]?.syntaxSpanSignature,
      ).toBe("");
      expect(
        fieldBeforeFullParse.lineStates[tailIndex]?.relBoundaries.length,
      ).toBe(0);

      // Advance CodeMirror's Lezer parser to the end of the document without changing the document:
      const parsed = forceParsing(view, view.state.doc.length, 500);
      expect(parsed).toBe(true);
      expect(syntaxTreeAvailable(view.state, view.state.doc.length)).toBe(true);

      // Dispatch a non-docChanged transaction (just as ParseWorker's setState effect does)
      // if forceParsing did not already dispatch:
      view.dispatch({});

      const fieldAfterFullParse = view.state.field(prettyNativeStateField);
      const freshAfterFullParse = computePrettyNativeLayout(
        view.state.doc,
        { path: "partial-parse.js" },
        view.state,
      );
      expect(
        fieldAfterFullParse.lineStates[tailIndex]?.syntaxSpanSignature,
      ).not.toBe("");
      expect(
        fieldAfterFullParse.lineStates[tailIndex]?.relBoundaries.length,
      ).toBe(4);
      expect(fieldAfterFullParse.lineStates).toEqual(
        freshAfterFullParse.lineStates,
      );
      expect(fieldAfterFullParse.counters).toEqual(
        freshAfterFullParse.counters,
      );
    } finally {
      view.destroy();
    }
  });
});
