// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import path from "node:path";
import { forceParsing, indentUnit } from "@codemirror/language";
import { EditorState, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import {
  formattersForBuffer,
  languageExtensionForBuffer,
  languageExtensionForPath,
  prettyLayoutCatalog,
  prettyLayoutForBuffer,
  prettyLayoutForPath,
} from "../editor/codemirror-lang.ts";
import { createPrettyExtension, prettyOutcomeOf } from "./pretty-layout.ts";
import { PrettyBreakWidget, prettyLayoutDecorations } from "./pretty-view.ts";

function parseSupportedLanguages(): string[] {
  const goPath = path.resolve(process.cwd(), "../lycaon/internal/filekind/supported.go");
  const source = readFileSync(goPath, "utf8");
  const block = /var\s+supportedLanguages\s*=\s*\[\]string\s*\{([\s\S]*?)\}/.exec(source);
  expect(block, `supportedLanguages in ${goPath}`).not.toBeNull();
  return Array.from((block?.[1] ?? "").matchAll(/"([^"]+)"/g), (match) => match[1]!);
}

const REPRESENTATIVE_PATHS: Record<string, string> = {
  apex: "ApexClass.cls",
  bash: "script.sh",
  c: "main.c",
  c_sharp: "Program.cs",
  cairo: "contract.cairo",
  circom: "circuit.circom",
  clojure: "core.clj",
  commonlisp: "core.lisp",
  cpp: "main.cpp",
  dart: "main.dart",
  dockerfile: "Dockerfile",
  elixir: "app.ex",
  go: "main.go",
  groovy: "build.groovy",
  hack: "index.hack",
  hcl: "main.tf",
  html: "index.html",
  java: "Main.java",
  javascript: "index.js",
  json: "data.json",
  jsonnet: "config.jsonnet",
  julia: "calc.jl",
  kotlin: "Main.kt",
  lua: "init.lua",
  move: "module.move",
  ocaml: "main.ml",
  perl: "script.pl",
  php: "index.php",
  powershell: "script.ps1",
  promql: "rules.promql",
  proto: "service.proto",
  python: "app.py",
  ql: "query.ql",
  r: "analysis.r",
  ruby: "app.rb",
  rust: "lib.rs",
  scala: "Main.scala",
  scheme: "core.scm",
  solidity: "Token.sol",
  swift: "App.swift",
  typescript: "index.ts",
  vue: "App.vue",
  xml: "config.xml",
  yaml: "config.yaml",
};

const EXTRA_PATHS = [
  "App.tsx",
  "site.css",
  "data.edn",
  "query.sql",
  "App.svelte",
  "Page.astro",
  "icon.svg",
  "README.md",
  "server.log",
];

function open(doc: string, filePath: string, language?: string): EditorState {
  const ext = languageExtensionForBuffer(filePath, language);
  const extensions: Extension[] = [
    indentUnit.of("  "),
    EditorState.tabSize.of(4),
    createPrettyExtension({ enabled: true, path: filePath, languageHint: language }),
  ];
  return EditorState.create({ doc, extensions: ext ? [ext, ...extensions] : extensions });
}

function openForced(doc: string, filePath: string, language?: string): EditorState {
  const view = new EditorView({ state: open(doc, filePath, language) });
  try {
    forceParsing(view, doc.length, 5_000);
    return view.state;
  } finally {
    view.destroy();
  }
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget instanceof PrettyBreakWidget) out.push(from);
  });
  return out;
}

function columnsAt(state: EditorState): Map<number, number> {
  const out = new Map<number, number>();
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (to > from) out.set(from, Number(/pw-pretty-line-(\d+)/.exec(String(value.spec.class))?.[1] ?? 0));
  });
  return out;
}

function writtenOut(state: EditorState): string {
  const text = state.doc.toString();
  const columns = columnsAt(state);
  let out = "";
  let last = 0;
  for (const pos of breaks(state)) {
    out += text.slice(last, pos).replace(/[ \t]+$/, "") + "\n" + " ".repeat(columns.get(pos) ?? 0);
    last = pos;
  }
  return out + text.slice(last);
}

const HASH_COMMENTS = new Set([
  "py", "yaml", "rb", "ex", "r", "jl", "pl", "promql", "ps1", "sh", "tf",
]);
const DASH_COMMENTS = new Set(["lua", "sql"]);
const ML_COMMENTS = new Set(["ml"]);
const SEMI_COMMENTS = new Set(["clj", "lisp", "scm", "edn"]);
const XML_MARKUP = new Set(["html", "xml", "vue", "svelte", "astro", "svg"]);

function commentLiteralFor(ext: string): string {
  if (HASH_COMMENTS.has(ext)) return "# c, (d); [e]";
  if (DASH_COMMENTS.has(ext)) return "-- c, (d); [e]";
  if (ML_COMMENTS.has(ext)) return "(* c, (d); [e] *)";
  if (SEMI_COMMENTS.has(ext)) return "; c, (d); [e]";
  if (XML_MARKUP.has(ext)) return "<!-- c, (d); [e] -->";
  return "// c, (d); [e]";
}

interface FixturePair {
  dense: string;
  formatted: string;
  protectedRanges: Array<[number, number]>;
}

function rangesOf(dense: string, literals: string[]): Array<[number, number]> {
  return literals.map((lit) => {
    const start = dense.indexOf(lit);
    expect(start, `literal ${lit} in fixture`).toBeGreaterThanOrEqual(0);
    return [start, start + lit.length];
  });
}

function fixtureForPath(filePath: string): FixturePair {
  const base = path.basename(filePath);
  const ext = path.extname(filePath).slice(1).toLowerCase();
  const items = Array.from({ length: 28 }, (_, i) => `item${i}`);
  const args = items.join(", ");
  const strLit = '"s, (t); [u]"';
  const commentLit = commentLiteralFor(ext);

  if (base === "Dockerfile") {
    const chain = items.map((item) => `echo ${item}`).join(" && ");
    const dense = `RUN ${chain} && echo ${strLit}`;
    return {
      dense,
      formatted: "RUN echo item0 &&\n  echo item1\n",
      protectedRanges: rangesOf(dense, [strLit]),
    };
  }
  switch (ext) {
    case "sh": {
      const chain = items.map((item) => `echo ${item}`).join(" && ");
      const dense = `${chain} && echo ${strLit} ${commentLit}`;
      return { dense, formatted: "echo item0 &&\n  echo item1\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "clj":
    case "lisp":
    case "scm": {
      const dense = `(defn process-items [xs] ${items.map((item) => `(step ${item} ${strLit})`).join(" ")}) ${commentLit}`;
      return { dense, formatted: "(defn process-items [xs]\n  (step item0)\n  (step item1))\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "edn": {
      const dense = `{:items [${items.map((item) => `{:name ${strLit} :id ${item}}`).join(" ")}]} ${commentLit}`;
      return { dense, formatted: '{:items\n  [\n    "a"\n    "b"\n  ]}\n', protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "sql": {
      const cols = items.slice(0, 12).join(", ");
      const conds = items.slice(12, 22).map((item) => `${item} = ${strLit}`).join(" AND ");
      const dense = `SELECT ${cols} FROM users JOIN orders ON users.id = orders.user_id WHERE ${conds} ${commentLit}`;
      return { dense, formatted: "SELECT item0,\n  item1\nFROM users\nWHERE item2 = 1\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "vue": {
      const spans = items.slice(0, 14).map((item) => `<span data-id="${item}">${strLit}</span>`).join("");
      const dense = `<template><div class="root">${spans}</div></template>`;
      return { dense, formatted: "<template>\n  <div>\n    <span>ok</span>\n  </div>\n</template>\n", protectedRanges: rangesOf(dense, [strLit]) };
    }
    case "html":
    case "xml":
    case "svelte":
    case "astro":
    case "svg": {
      const children = items.slice(0, 14).map((item) => `<span data-id="${item}">${item}</span>`).join("");
      const dense = `<div class="container" data-note=${strLit}>${children}</div>${commentLit}`;
      return { dense, formatted: "<div>\n  <span>ok</span>\n</div>\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "tsx": {
      const children = items.slice(0, 12).map((item) => `<Item key="${item}" label=${strLit} />`).join("");
      const dense = `export function View() { return (<Section>${children}</Section>); } ${commentLit}`;
      return { dense, formatted: "export function View() {\n  return (\n    <Section>\n      <Item />\n    </Section>\n  );\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "css": {
      const rules = items.slice(0, 14).map((item) => `.${item}{color:red;content:${strLit}}`).join("");
      const dense = `${rules} /* c, (d); {e} */`;
      return { dense, formatted: ".item0 {\n  color: red;\n}\n", protectedRanges: rangesOf(dense, [strLit, "/* c, (d); {e} */"]) };
    }
    case "md": {
      const jsonRow = `{${items.map((item) => `"${item}": ${strLit}`).join(", ")}}`;
      const dense = `# Notes\n\n\`\`\`json\n${jsonRow}\n\`\`\`\n`;
      return { dense, formatted: '# Notes\n\n```json\n{\n  "a": 1\n}\n```\n', protectedRanges: rangesOf(dense, [strLit]) };
    }
    case "log": {
      const jsonRow = `{${items.map((item) => `"${item}": ${strLit}`).join(", ")}}`;
      const dense = `2026-11-14T10:00:00Z INFO request ${jsonRow}`;
      return { dense, formatted: "2026-11-14T10:00:00Z INFO ok\n2026-11-14T10:00:01Z INFO done\n", protectedRanges: rangesOf(dense, [strLit]) };
    }
    case "json":
    case "jsonnet": {
      const pairs = items.map((item) => `"${item}": 1`).join(", ");
      const dense = `{"data": [${items.map((item) => `"${item}"`).join(", ")}], "note": ${strLit}, ${pairs}}`;
      return { dense, formatted: '{\n  "a": 1,\n  "b": 2\n}\n', protectedRanges: rangesOf(dense, [strLit]) };
    }
    case "yaml": {
      const longItems = Array.from({ length: 36 }, (_, i) => `item${i}`);
      const dense = `value: [${longItems.slice(0, 18).join(", ")}, [${longItems.slice(18).join(", ")}], ${strLit}] ${commentLit}`;
      return { dense, formatted: "value:\n  - a\n  - b\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "py": {
      const dense = `def compute_all(inputs): return compute(${args}, ${strLit}) ${commentLit}`;
      return { dense, formatted: "def compute_all(inputs):\n  return [\n    item0,\n    item1,\n  ]\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "rb": {
      const dense = `def compute_all(inputs); render(${args}, ${strLit}); end ${commentLit}`;
      return { dense, formatted: "def compute_all(inputs)\n  render(\n    item0,\n    item1\n  )\nend\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "ex": {
      const dense = `defmodule App do def run(inputs), do: compute(${args}, ${strLit}) end ${commentLit}`;
      return { dense, formatted: "defmodule App do\n  def run(inputs) do\n    compute(item0, item1)\n  end\nend\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "jl": {
      const dense = `function compute_all(inputs) return compute(${args}, ${strLit}) end ${commentLit}`;
      return { dense, formatted: "function compute_all(inputs)\n  return compute(item0, item1)\nend\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "lua": {
      const dense = `local function compute_all(inputs) return compute(${args}, ${strLit}) end ${commentLit}`;
      return { dense, formatted: "local function compute_all(inputs)\n  return compute(item0, item1)\nend\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "ml": {
      const dense = `let compute_all inputs = compute (${args}, ${strLit}) ${commentLit}`;
      return { dense, formatted: "let compute_all inputs =\n  compute (item0, item1)\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "pl": {
      const dense = `sub compute_all { my ($x) = @_; return compute(${args}, ${strLit}); } ${commentLit}`;
      return { dense, formatted: "sub compute_all {\n  my ($x) = @_;\n  return compute(item0, item1);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "r": {
      const dense = `compute_all <- function(inputs) { list(${args}, label = ${strLit}) } ${commentLit}`;
      return { dense, formatted: "compute_all <- function(inputs) {\n  list(item0, item1)\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "promql": {
      const labels = items.slice(0, 18).map((item) => `${item}="ok"`).join(", ");
      const dense = `sum by (job, instance) (rate(http_requests_total{${labels}, note=${strLit}}[5m])) ${commentLit}`;
      return { dense, formatted: 'sum by (job) (\n  rate(http_requests_total{job="api"}[5m])\n)\n', protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "tf": {
      const dense = `resource "aws_instance" "web" { tags = { ${items.slice(0, 16).map((item) => `${item} = 1`).join(", ")}, note = ${strLit} } } ${commentLit}`;
      return { dense, formatted: 'resource "aws_instance" "web" {\n  ami = "ami-1"\n}\n', protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "proto": {
      const fields = items.slice(0, 14).map((item, idx) => `string ${item} = ${idx + 1};`).join(" ");
      const dense = `syntax = "proto3"; message Request { string note = 99 [default = ${strLit}]; ${fields} } ${commentLit}`;
      return { dense, formatted: 'syntax = "proto3";\nmessage Request {\n  string item0 = 1;\n}\n', protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "ql": {
      const dense = `from Function f where f.hasName(${strLit}) and exists(Expr e | call(${args})) select f ${commentLit}`;
      return { dense, formatted: "from Function f\nwhere f.isPublic()\nselect f\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "php": {
      const dense = `<?php function run_all($in) { $note = ${strLit}; return compute(${args}, $note); } ${commentLit}`;
      return { dense, formatted: "<?php\nfunction run_all($in) {\n  return compute(1, 2);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "hack": {
      const dense = `<?hh function run_all(vec<int> $in): void { $note = ${strLit}; compute(${args}, $note); } ${commentLit}`;
      return { dense, formatted: "<?hh\nfunction run_all(): void {\n  compute(1, 2);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "sol": {
      const dense = `pragma solidity ^0.8.0; contract Token { function transfer() public { emit Log(${args}, ${strLit}); } } ${commentLit}`;
      return { dense, formatted: "pragma solidity ^0.8.0;\ncontract Token {\n  uint256 public total;\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "move": {
      const dense = `module 0x1::token { public fun run() { let s = ${strLit}; compute(${args}, s); } } ${commentLit}`;
      return { dense, formatted: "module 0x1::token {\n  public fun run() {\n    compute(1, 2);\n  }\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "cairo": {
      const dense = `fn run_contract() { let note = ${strLit}; compute(${args}, note); } ${commentLit}`;
      return { dense, formatted: "fn run_contract() {\n  compute(1, 2);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "circom": {
      const dense = `template Circuit() { signal input a; log(${args}, ${strLit}); } ${commentLit}`;
      return { dense, formatted: "template Circuit() {\n  signal input a;\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "cls": {
      const dense = `public class ApexClass { public static void run() { String s = 's, (t); [u]'; compute(${args}, s); } } ${commentLit}`;
      return { dense, formatted: "public class ApexClass {\n  public static void run() {\n    compute(1, 2);\n  }\n}\n", protectedRanges: rangesOf(dense, ["'s, (t); [u]'", commentLit]) };
    }
    case "ps1": {
      const dense = `function Invoke-Build { Invoke-Step -Items @(${args}) -Note ${strLit}; } ${commentLit}`;
      return { dense, formatted: "function Invoke-Build {\n  Invoke-Step -Items @(1, 2)\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "js":
    case "ts": {
      const dense = `export function run() { const note = ${strLit}; return compute(${args}, note); } ${commentLit}`;
      return { dense, formatted: "export function run() {\n  return compute(item0, item1);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "go":
    case "swift": {
      const dense = `func run() { let note = ${strLit}; compute(${args}, note) } ${commentLit}`;
      return { dense, formatted: "func run() {\n  compute(\n    item0,\n    item1\n  )\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "rs": {
      const dense = `pub fn run() { let note = ${strLit}; compute(${args}, note); } ${commentLit}`;
      return { dense, formatted: "pub fn run() {\n  compute(item0, item1);\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "kt": {
      const dense = `fun run() { val note = ${strLit}; compute(${args}, note) } ${commentLit}`;
      return { dense, formatted: "fun run() {\n  compute(item0, item1)\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "scala": {
      const dense = `object Main { def run(): Unit = { val note = ${strLit}; compute(${args}, note) } } ${commentLit}`;
      return { dense, formatted: "object Main {\n  def run(): Unit = {\n    compute(item0, item1)\n  }\n}\n", protectedRanges: rangesOf(dense, [strLit, commentLit]) };
    }
    case "c":
    case "cpp":
    case "java":
    case "cs":
    case "dart":
    case "groovy": {
      const prefix = ext === "java" || ext === "cs" ? "class Main { void run() {" : "void run() {";
      const suffix = ext === "java" || ext === "cs" ? "} }" : "}";
      const dense = `${prefix} auto_call(${args}, ${strLit}); ${suffix} ${commentLit}`;
      return {
        dense,
        formatted: `${prefix}\n  auto_call(item0, item1);\n${suffix}\n`,
        protectedRanges: rangesOf(dense, [strLit, commentLit]),
      };
    }
    default:
      throw new Error(`Unhandled fixture extension: ${filePath}`);
  }
}

describe("bidirectional supportedLanguages and Pretty catalog matrix", () => {
  const supported = parseSupportedLanguages();

  it("maps all 44 supportedLanguages to grammars and Pretty layouts, and every active catalog entry to a grammar", () => {
    expect(supported).toHaveLength(44);
    for (const lang of supported) {
      expect(languageExtensionForBuffer("unknown", lang), lang).not.toBeNull();
      const layout = prettyLayoutForBuffer("unknown", lang);
      expect(layout.kind, lang).not.toBe("none");
      expect(layout.kind, lang).not.toBe("containers");
      expect(Array.isArray(formattersForBuffer("unknown", lang)), lang).toBe(true);

      const repPath = REPRESENTATIVE_PATHS[lang];
      expect(repPath, `representative path for ${lang}`).toBeDefined();
      expect(languageExtensionForPath(repPath!), repPath).not.toBeNull();
      expect(prettyLayoutForPath(repPath!).kind, repPath).toBe(layout.kind);
    }

    const catalog = prettyLayoutCatalog();
    const coveredKinds = new Set<string>();
    for (const { ext, pretty } of catalog) {
      coveredKinds.add(pretty.kind);
      if (pretty.kind !== "none" && pretty.kind !== "containers") {
        expect(
          languageExtensionForPath(`sample.${ext}`),
          `catalog ext .${ext} (${pretty.kind}) must mount a grammar so parsed rules never stall`,
        ).not.toBeNull();
      }
    }
    const matrixKinds = new Set<string>(
      [...Object.values(REPRESENTATIVE_PATHS), ...EXTRA_PATHS].map((p) => prettyLayoutForPath(p).kind),
    );
    for (const kind of coveredKinds) {
      if (kind !== "none") expect(matrixKinds.has(kind), `matrix covers ${kind}`).toBe(true);
    }
  });

  it("passes dense, formatted, idempotence, and malformed-edit convergence across all 44 supported languages and extras", () => {
    const cases = [
      ...supported.map((lang) => ({ label: lang, filePath: REPRESENTATIVE_PATHS[lang]!, language: lang })),
      ...EXTRA_PATHS.map((filePath) => ({ label: filePath, filePath, language: undefined })),
    ];

    for (const { label, filePath, language } of cases) {
      const { dense, formatted, protectedRanges } = fixtureForPath(filePath);
      const denseState = openForced(dense, filePath, language);
      expect(prettyOutcomeOf(denseState).kind, `${label}: dense outcome`).toBe("active");
      const denseBreaks = breaks(denseState);
      expect(denseBreaks.length, `${label}: dense breaks`).toBeGreaterThan(0);
      for (const pos of denseBreaks) {
        for (const [from, to] of protectedRanges) {
          expect(pos > from && pos < to, `${label}: break at ${pos} inside [${from}, ${to}]`).toBe(false);
        }
      }

      const formattedState = openForced(formatted, filePath, language);
      expect(breaks(formattedState), `${label}: formatted breaks`).toEqual([]);

      const roundTrip = openForced(writtenOut(denseState), filePath, language);
      expect(breaks(roundTrip), `${label}: second-pass idempotence`).toEqual([]);

      // Malformed edit convergence: incremental state matches a fresh build on the broken document.
      const view = new EditorView({ state: open(dense, filePath, language) });
      try {
        forceParsing(view, dense.length, 5_000);
        const mid = Math.floor(dense.length / 2);
        view.dispatch({ changes: { from: mid, insert: ' "unclosed(' } });
        const brokenDoc = view.state.doc.toString();
        forceParsing(view, brokenDoc.length, 5_000);
        const freshBroken = openForced(brokenDoc, filePath, language);
        expect(prettyOutcomeOf(view.state), `${label}: malformed outcome`).toEqual(prettyOutcomeOf(freshBroken));
        expect(breaks(view.state), `${label}: malformed edit matches fresh build`).toEqual(breaks(freshBroken));
      } finally {
        view.destroy();
      }
    }
  });

  it("protects strings and comments past column 11,000 and converges to a fresh build on edits", () => {
    const modes: Array<{ filePath: string; commentPrefix: string; isSexp?: boolean }> = [
      { filePath: "long.go", commentPrefix: "//" },
      { filePath: "long.py", commentPrefix: "#" },
      { filePath: "long.rs", commentPrefix: "//" },
      { filePath: "long.sql", commentPrefix: "--" },
      { filePath: "Long.java", commentPrefix: "//" },
      { filePath: "long.rb", commentPrefix: "#" },
      { filePath: "long.tf", commentPrefix: "#" },
      { filePath: "long.clj", commentPrefix: ";", isSexp: true },
      { filePath: "long.ps1", commentPrefix: "#" },
    ];

    for (const { filePath, commentPrefix, isSexp } of modes) {
      const chunk = isSexp ? "(step alpha beta) " : "alpha(beta, gamma), ";
      const repeatCount = Math.ceil(15_100 / chunk.length);
      const prefix = (isSexp ? "(begin " : "call(") + chunk.repeat(repeatCount);
      const stringTrap = '"trap, (x); [y] {z}"';
      const middle = isSexp ? " (after one two) " : ", after(one, two)) ";
      const commentTrap = `${commentPrefix} tail, (a); [b] {c}`;
      const doc = `${prefix}${stringTrap}${middle}${commentTrap}`;
      expect(doc.length, filePath).toBeGreaterThan(15_000);

      const strStart = prefix.length;
      const strEnd = strStart + stringTrap.length;
      const commentStart = strStart + stringTrap.length + middle.length;
      const commentEnd = doc.length;
      expect(strStart, filePath).toBeGreaterThan(11_000);

      const view = new EditorView({ state: open(doc, filePath) });
      try {
        forceParsing(view, doc.length, 5_000);
        const posList = breaks(view.state);
        expect(posList.length, `${filePath}: total breaks`).toBeGreaterThan(10);
        expect(posList.some((p) => p > 11_000), `${filePath}: breaks past 11k`).toBe(true);
        for (const p of posList) {
          expect(p > strStart && p < strEnd, `${filePath}: break inside string at ${p}`).toBe(false);
          expect(p > commentStart && p < commentEnd, `${filePath}: break inside comment at ${p}`).toBe(false);
        }

        view.dispatch({ changes: { from: strEnd + 2, insert: " " } });
        const afterDoc = view.state.doc.toString();
        forceParsing(view, afterDoc.length, 5_000);
        const editedBreaks = breaks(view.state);
        const freshState = openForced(afterDoc, filePath);
        expect(editedBreaks, `${filePath}: incremental edit matches fresh build`).toEqual(breaks(freshState));
      } finally {
        view.destroy();
      }
    }
  });

  it("snapshots SQL predicateDepth, betweenPending, and caseStack across mid-line checkpoints", () => {
    const conds = Array.from(
      { length: 90 },
      (_, i) => `col_${i} BETWEEN 10 AND 20 AND CASE WHEN flag_${i} = 1 THEN 'a' ELSE 'b' END = 'a'`,
    ).join(" AND ");
    const sql = `SELECT id, account_id, created_at FROM events JOIN accounts ON events.account_id = accounts.id ON DELETE CASCADE WHERE ${conds};`;
    expect(sql.length).toBeGreaterThan(6_500);

    const view = new EditorView({ state: open(sql, "query.sql") });
    try {
      forceParsing(view, sql.length, 5_000);
      // Edit past two 2,048-char checkpoints inside the WHERE clause and compare to a fresh build.
      const editPos = sql.indexOf("col_70");
      expect(editPos).toBeGreaterThan(4_500);
      view.dispatch({ changes: { from: editPos, to: editPos + 6, insert: "updated_col_70" } });
      const afterSql = view.state.doc.toString();
      forceParsing(view, afterSql.length, 5_000);
      const freshSql = openForced(afterSql, "query.sql");
      expect(breaks(view.state)).toEqual(breaks(freshSql));
    } finally {
      view.destroy();
    }
  });
});
