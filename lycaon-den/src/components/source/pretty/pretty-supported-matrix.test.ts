import { readFileSync } from "node:fs";
import path from "node:path";
import { indentUnit } from "@codemirror/language";
import { EditorState, type Extension } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import {
  formattersForBuffer,
  languageExtensionForBuffer,
  languageExtensionForPath,
  prettyLayoutForBuffer,
  prettyLayoutForPath,
} from "../editor/codemirror-lang.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { PrettyBreakWidget, prettyLayoutDecorations } from "./pretty-view.ts";

function parseSupportedLanguages(): string[] {
  const goPath = path.resolve(process.cwd(), "../lycaon/internal/filekind/supported.go");
  const source = readFileSync(goPath, "utf8");
  const block = /var\s+supportedLanguages\s*=\s*\[\]string\s*\{([\s\S]*?)\}/.exec(source);
  if (!block) throw new Error(`Could not locate supportedLanguages in ${goPath}`);
  return Array.from(block[1]!.matchAll(/"([^"]+)"/g), (match) => match[1]!);
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

function fixtureForPath(filePath: string): FixturePair {
  const base = path.basename(filePath);
  const ext = path.extname(filePath).slice(1).toLowerCase();
  const items = Array.from({ length: 28 }, (_, i) => `item${i}`);
  const strLit = '"s, (t); [u]"';
  const commentLit = commentLiteralFor(ext);

  if (base === "Dockerfile") {
    const chain = items.map((item) => `echo ${item}`).join(" && ");
    return {
      dense: `RUN ${chain} && echo ${strLit}`,
      formatted: "RUN echo item0 &&\n  echo item1\n",
      protectedRanges: [[`RUN ${chain} && echo `.length, `RUN ${chain} && echo `.length + strLit.length]],
    };
  }
  if (ext === "sh") {
    const chain = items.map((item) => `echo ${item}`).join(" && ");
    const prefix = `${chain} && echo `;
    return {
      dense: `${prefix}${strLit} ${commentLit}`,
      formatted: "echo item0 &&\n  echo item1\n",
      protectedRanges: [
        [prefix.length, prefix.length + strLit.length],
        [prefix.length + strLit.length + 1, prefix.length + strLit.length + 1 + commentLit.length],
      ],
    };
  }
  if (ext === "clj" || ext === "lisp" || ext === "scm") {
    const dense = `(process-items ${items.map((item) => `(step ${item} ${strLit})`).join(" ")}) ${commentLit}`;
    return {
      dense,
      formatted: "(process-items\n  (step item0)\n  (step item1))\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "edn") {
    const dense = `{:items [${items.map((item) => `{:name ${strLit} :id ${item}}`).join(" ")}]} ${commentLit}`;
    return {
      dense,
      formatted: "{:items\n  [{:name \"a\" :id item0}]}\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "sql") {
    const cols = items.slice(0, 12).join(", ");
    const conds = items.slice(12, 22).map((item) => `${item} = ${strLit}`).join(" AND ");
    const dense = `SELECT ${cols} FROM users JOIN orders ON users.id = orders.user_id WHERE ${conds} ${commentLit}`;
    return {
      dense,
      formatted: "SELECT item0,\n  item1\nFROM users\nWHERE item2 = 1\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "vue") {
    const spans = items.slice(0, 14).map((item) => `<span data-id="${item}">${strLit}</span>`).join("");
    const dense = `<template><div class="root">${spans}</div></template>`;
    return {
      dense,
      formatted: "<template>\n  <div>\n    <span>ok</span>\n  </div>\n</template>\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length]],
    };
  }
  if (XML_MARKUP.has(ext)) {
    const children = items.slice(0, 14).map((item) => `<span data-id="${item}">${item}</span>`).join("");
    const dense = `<div class="container" data-note=${strLit}>${children}</div>${commentLit}`;
    return {
      dense,
      formatted: "<div>\n  <span>ok</span>\n</div>\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "md") {
    const jsonRow = `{${items.map((item) => `"${item}": ${strLit}`).join(", ")}}`;
    const dense = `# Notes\n\n\`\`\`\n${jsonRow}\n\`\`\`\n`;
    return {
      dense,
      formatted: "# Notes\n\n```json\n{\n  \"a\": 1\n}\n```\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length]],
    };
  }
  if (ext === "log") {
    const jsonRow = `{${items.map((item) => `"${item}": ${strLit}`).join(", ")}}`;
    const dense = `2026-11-14T10:00:00Z INFO request ${jsonRow}`;
    return {
      dense,
      formatted: "2026-11-14T10:00:00Z INFO ok\n2026-11-14T10:00:01Z INFO done\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length]],
    };
  }
  if (ext === "json" || ext === "jsonnet") {
    const pairs = items.map((item) => `"${item}": 1`).join(", ");
    const dense = `{"data": [${items.map((item) => `"${item}"`).join(", ")}], "note": ${strLit}, ${pairs}}`;
    return {
      dense,
      formatted: "{\n  \"a\": 1,\n  \"b\": 2\n}\n",
      protectedRanges: [[dense.indexOf(strLit), dense.indexOf(strLit) + strLit.length]],
    };
  }
  if (ext === "yaml") {
    const head = `value: [${items.slice(0, 12).join(", ")}, [${items.slice(12, 22).join(", ")}], `;
    const dense = `${head}${strLit}] ${commentLit}`;
    return {
      dense,
      formatted: "value:\n  - a\n  - b\n",
      protectedRanges: [[head.length, head.length + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "go") {
    const head = `func run() { call(${items.join(", ")}, `;
    const dense = `${head}${strLit}); } ${commentLit}`;
    return {
      dense,
      formatted: "func run() {\n  call(\n    item0,\n    item1,\n  )\n}\n",
      protectedRanges: [[head.length, head.length + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }
  if (ext === "ps1") {
    const head = `function Run { Call(${items.join(", ")}, `;
    const dense = `${head}${strLit}); } ${commentLit}`;
    return {
      dense,
      formatted: "function Run {\n  Call(\n    item0,\n    item1\n  )\n}\n",
      protectedRanges: [[head.length, head.length + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
    };
  }

  const head = `value = call(${items.slice(0, 10).join(", ")}, [${items.slice(10, 20).join(", ")}], (${items.slice(20).join(", ")}), `;
  const dense = `${head}${strLit}) ${commentLit}`;
  return {
    dense,
    formatted: "value = call(\n  item0,\n  item1\n)\n",
    protectedRanges: [[head.length, head.length + strLit.length], [dense.lastIndexOf(commentLit), dense.length]],
  };
}

describe("bidirectional supportedLanguages matrix", () => {
  const supported = parseSupportedLanguages();

  it("maps all 44 supportedLanguages to a non-null grammar and active PrettyLayout", () => {
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
  });

  it("passes dense, formatted, idempotence, and malformed-edit checks across all 44 supported languages and extras", () => {
    const cases = [
      ...supported.map((lang) => ({ label: lang, filePath: REPRESENTATIVE_PATHS[lang]!, language: lang })),
      ...EXTRA_PATHS.map((filePath) => ({ label: filePath, filePath, language: undefined })),
    ];

    for (const { label, filePath, language } of cases) {
      const { dense, formatted, protectedRanges } = fixtureForPath(filePath);
      const denseState = open(dense, filePath, language);
      const denseBreaks = breaks(denseState);
      expect(denseBreaks.length, `${label}: dense breaks`).toBeGreaterThan(0);
      for (const pos of denseBreaks) {
        for (const [from, to] of protectedRanges) {
          expect(pos > from && pos < to, `${label}: break at ${pos} inside [${from}, ${to}]`).toBe(false);
        }
      }

      const formattedState = open(formatted, filePath, language);
      expect(breaks(formattedState), `${label}: formatted breaks`).toEqual([]);

      const roundTrip = open(writtenOut(denseState), filePath, language);
      expect(breaks(roundTrip), `${label}: second-pass idempotence`).toEqual([]);

      const malformed = denseState.update({ changes: { from: Math.floor(dense.length / 2), insert: ' "unclosed' } }).state;
      expect(() => breaks(malformed), `${label}: malformed edit`).not.toThrow();
    }
  });

  it("protects strings and comments past column 11,000 on 15,000-character single-line Lezer and StreamLanguage buffers", () => {
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
      const repeatCount = Math.ceil(11_200 / chunk.length);
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

      const state = open(doc, filePath);
      const posList = breaks(state);
      expect(posList.length, `${filePath}: total breaks`).toBeGreaterThan(10);
      expect(posList.some((p) => p > 11_000), `${filePath}: breaks past 11k`).toBe(true);
      for (const p of posList) {
        expect(p > strStart && p < strEnd, `${filePath}: break inside string at ${p}`).toBe(false);
        expect(p > commentStart && p < commentEnd, `${filePath}: break inside comment at ${p}`).toBe(false);
      }

      const edited = state.update({ changes: { from: strEnd + 2, insert: " " } }).state;
      const editedBreaks = breaks(edited);
      expect(editedBreaks.some((p) => p > 11_000), `${filePath}: edited breaks past 11k`).toBe(true);
      for (const p of editedBreaks) {
        expect(p > strStart + 1 && p < strEnd, `${filePath}: edited break inside string at ${p}`).toBe(false);
        expect(p > commentStart + 1 && p < commentEnd + 1, `${filePath}: edited break inside comment at ${p}`).toBe(false);
      }
    }
  });
});
