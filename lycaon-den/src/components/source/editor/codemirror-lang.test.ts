import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { ensureSyntaxTree } from "@codemirror/language";
import {
  formattersForBuffer,
  formattersForPath,
  languageExtensionForBuffer,
  languageExtensionForPath,
  languageLabelForDetectedLanguage,
  languageForInfoString,
  languageLabelForPath,
  prettyLayoutCatalog,
  prettyLayoutForBuffer,
  prettyLayoutForLanguage,
  prettyLayoutForPath,
} from "./codemirror-lang.ts";

function parsedTree(path: string, doc: string) {
  const state = EditorState.create({
    doc,
    extensions: [languageExtensionForPath(path)!] as never,
  });
  return ensureSyntaxTree(state, state.doc.length, 5_000);
}

/** Node names in the parsed tree for `doc` under the grammar `path` resolves. */
function parsedNodeNames(path: string, doc: string): string[] {
  const names: string[] = [];
  parsedTree(path, doc)?.iterate({ enter: (node) => void names.push(node.name) });
  return names;
}

// Position resolution reaches nested grammar overlays that iteration skips.
function nodeNameAt(path: string, doc: string, needle: string): string {
  const at = doc.indexOf(needle);
  expect(at, `"${needle}" is in the fixture`).toBeGreaterThanOrEqual(0);
  return parsedTree(path, doc)?.resolveInner(at, 1).name ?? "";
}

describe("codemirror-lang", () => {
  it("maps extensions to labels", () => {
    expect(languageLabelForPath("src/main.ts")).toBe("TypeScript");
    expect(languageLabelForPath("query.sql")).toBe("SQL");
    expect(languageLabelForPath("styles/app.scss")).toBe("SCSS");
    expect(languageLabelForPath("Cargo.toml")).toBe("TOML");
    expect(languageLabelForPath("scripts/deploy.ps1")).toBe("PowerShell");
    expect(languageLabelForPath("app.psgi")).toBe("Perl");
    expect(languageLabelForPath("t/service.t")).toBe("Perl");
    expect(languageLabelForPath("a/b/change.patch")).toBe("Diff");
  });

  it("labels well-known basenames without extensions", () => {
    expect(languageLabelForPath("Dockerfile")).toBe("Dockerfile");
    expect(languageLabelForPath("deploy/Makefile")).toBe("Makefile");
    expect(languageLabelForPath("CMakeLists.txt")).toBe("CMake");
    expect(languageLabelForPath(".env")).toBe("Dotenv");
    expect(languageLabelForPath("cpanfile")).toBe("Perl");
    expect(languageLabelForPath("Rexfile")).toBe("Perl");
  });

  it("resolves highlight grammars for widened coverage", () => {
    for (const path of [
      "q.sql",
      "a.css",
      "a.scss",
      "a.toml",
      "a.ps1",
      "a.psgi",
      "a.plx",
      "a.perl",
      "a.ph",
      "a.al",
      "a.t",
      "cpanfile",
      "Rexfile",
      "a.hs",
      "a.erl",
      "a.gradle",
      "a.diff",
      "a.ini",
      "icon.svg",
      "App.svelte",
      "Dockerfile",
      "CMakeLists.txt",
      ".gitignore",
    ]) {
      expect(languageExtensionForPath(path), path).not.toBeNull();
    }
  });

  it("highlights every language in docs/supported-languages.md", () => {
    // One representative path per supported language (44 total), plus Sass.
    for (const path of [
      "Foo.cls",
      "run.sh",
      "a.c",
      "A.cs",
      "a.cpp",
      "a.cairo",
      "a.circom",
      "core.clj",
      "lib.ql",
      "lib.qll",
      "a.lisp",
      "app.dart",
      "Dockerfile",
      "app.ex",
      "app.exs",
      "main.go",
      "main.tf",
      "vars.tfvars",
      "infra.hcl",
      "index.html",
      "a.hack",
      "data.json",
      "A.java",
      "a.js",
      "cfg.jsonnet",
      "cfg.libsonnet",
      "a.jl",
      "A.kt",
      "a.lua",
      "coin.move",
      "a.ml",
      "index.php",
      "alerts.promql",
      "api.proto",
      "main.py",
      "stats.r",
      "app.rb",
      "main.rs",
      "A.scala",
      "a.scm",
      "token.sol",
      "App.swift",
      "main.ts",
      "App.vue",
      "a.xml",
      "cfg.yaml",
      "a.pl",
      "a.ps1",
      "a.groovy",
      "a.sass",
    ]) {
      expect(languageExtensionForPath(path), path).not.toBeNull();
    }
  });

  it("parses HTML with the real grammar, including embedded script and style", () => {
    const names = parsedNodeNames(
      "index.html",
      "<div><style>a{color:red}</style><script>let x = 1</script></div>",
    );
    // Stream-mode XML produced tokens only; these nodes come from lang-html
    // delegating to the CSS and JavaScript grammars.
    expect(names).toContain("StyleSheet");
    expect(names).toContain("Script");
    expect(names).toContain("VariableDefinition");
  });

  it("parses CSS with the real grammar", () => {
    const names = parsedNodeNames("app.css", ".a { color: red; }");
    expect(names).toContain("RuleSet");
    expect(names).toContain("Declaration");
  });

  it("resolves fenced-code info strings by extension and by label", () => {
    expect(languageForInfoString("ts")).not.toBeNull();
    expect(languageForInfoString("typescript")).not.toBeNull();
    expect(languageForInfoString("Shell")).not.toBeNull();
    expect(languageForInfoString("go")).not.toBeNull();
    // Attributes after the language are not part of the name.
    expect(languageForInfoString("js {1,3} title=x")).not.toBeNull();
    expect(languageForInfoString("")).toBeNull();
    expect(languageForInfoString("   ")).toBeNull();
    expect(languageForInfoString("notalanguage")).toBeNull();
  });

  it("highlights fenced code inside Markdown", () => {
    const doc = ["```ts", "const answer = 42", "```", ""].join("\n");
    expect(parsedNodeNames("notes.md", doc)).toContain("FencedCode");
    // The fence body parses as TypeScript rather than staying opaque text.
    expect(nodeNameAt("notes.md", doc, "answer")).toBe("VariableDefinition");
  });

  it("resolves a fence by language label as well as by extension", () => {
    const doc = ["```python", "answer = 42", "```", ""].join("\n");
    expect(nodeNameAt("notes.md", doc, "answer")).toBe("VariableName");
  });

  it("leaves an unlabelled fence as plain code text", () => {
    const doc = ["```", "const answer = 42", "```", ""].join("\n");
    expect(nodeNameAt("notes.md", doc, "answer")).toBe("CodeText");
  });

  it("keeps unknown files as plain text", () => {
    expect(languageLabelForPath("notes.unknownext")).toBe("unknownext");
    expect(languageExtensionForPath("notes.unknownext")).toBeNull();
    expect(languageLabelForPath("LICENSE")).toBe("text");
  });

  it("labels generic text without exposing the ambiguous Vim help grammar", () => {
    expect(languageLabelForPath("notes.txt")).toBe("Plain text");
    expect(languageLabelForDetectedLanguage("", "notes.txt")).toBe("Plain text");
    expect(languageLabelForDetectedLanguage("vimdoc", "notes.txt")).toBe(
      "Plain text",
    );
    expect(
      languageLabelForDetectedLanguage("requirements", "requirements.txt"),
    ).toBe("Requirements");
  });

  it("declares the Pretty layout of every catalog language", () => {
    const declared: Record<string, string> = {
      "data": "json jsonc json5 jsonl ndjson webmanifest har avsc jsonnet libsonnet geojson sarif tfstate topojson",
      "markup": "xml svg html htm vue svelte astro csproj fsproj nuspec plist props targets vbproj vcxproj wsdl xaml xsd",
      "code": "ts mts cts tsx js mjs cjs jsx css scss less",
      "bracket statements": "c h cc cpp cxx hpp hh hxx java cs csx cake scala kt kts dart rs swift groovy gradle gvy d php hack sol move cairo circom cls trigger ql qll proto hcl tf tfvars tofu nomad ps1 psm1 psd1",
      "bracket": "go py pyi yaml yml toml rb cr ex exs lua r jl hs elm ml mli erl hrl al perl ph pl plx pm psgi t promql sass bzl",
      "clause": "sql",
      "sexp": "clj cljs cljc edn scm ss lisp cl lsp",
      "chain": "sh bash zsh dockerfile",
      "fenced": "md mdx",
      "containers": "log out txt",
      "none": "diff patch properties ini env cfg cmake tcl f f90 f95 f03 pas",
    };
    const outcome = (ext: string) => {
      const layout = prettyLayoutForPath(`file.${ext}`);
      return layout.kind === "bracket" && layout.statements ? "bracket statements" : layout.kind;
    };
    const expected = Object.entries(declared).flatMap(([kind, exts]) => exts.split(" ").map((ext) => [ext, kind] as const));
    expect(Object.fromEntries(expected.map(([ext]) => [ext, outcome(ext)]))).toEqual(Object.fromEntries(expected));
    expect(prettyLayoutCatalog().map(({ ext }) => ext).sort()).toEqual(expected.map(([ext]) => ext).sort());
  });

  it("gives files outside the catalog and unknown names no Pretty layout", () => {
    expect(prettyLayoutForPath("data.csv").kind).toBe("none");
    expect(prettyLayoutForPath("README").kind).toBe("none");
    expect(prettyLayoutForPath("Gemfile")).toEqual(prettyLayoutForPath("x.rb"));
    expect(prettyLayoutForLanguage("json")).toEqual(prettyLayoutForPath("x.json"));
    expect(prettyLayoutForLanguage("Markdown").kind).toBe("fenced");
    expect(prettyLayoutForLanguage("TypeScript title=x").kind).toBe("code");
    expect(prettyLayoutForLanguage("no-such-language").kind).toBe("none");
  });

  it("maps all 44 host supportedLanguages, commented JSON configs, extensions, and fence aliases", () => {
    const supportedSrc = readFileSync(
      resolve(__dirname, "../../../../../lycaon/internal/filekind/supported.go"),
      "utf8",
    );
    const match = supportedSrc.match(/supportedLanguages\s*=\s*\[\]string\{([\s\S]*?)\}/);
    expect(match).not.toBeNull();
    const langs = [...match![1]!.matchAll(/"([^"]+)"/g)].map((m) => m[1]!);
    expect(langs).toHaveLength(44);
    for (const lang of langs) {
      expect(languageForInfoString(lang), `grammar for ${lang}`).not.toBeNull();
      expect(languageExtensionForBuffer("bin/unknown", lang), `buffer ext for ${lang}`).not.toBeNull();
    }

    for (const cfg of ["tsconfig.json", "tsconfig.node.json", "jsconfig.json", "devcontainer.json", ".devcontainer.json"]) {
      expect(prettyLayoutForPath(cfg)).toEqual({ kind: "data", comments: true, records: false });
    }
    expect(languageExtensionForPath("src/index.mts")).not.toBeNull();
    expect(languageExtensionForPath("src/index.cts")).not.toBeNull();
    expect(languageLabelForPath("Jenkinsfile")).toBe("Groovy");
    expect(languageLabelForPath("App.csproj")).toBe("XML");
    expect(languageLabelForPath("Cargo.lock")).toBe("TOML");
    expect(formattersForPath("main.go")).toEqual(["golangci"]);
    expect(formattersForPath("lib/app.ex")).toEqual(["mix_format"]);
    expect(formattersForPath("app/User.php")).toEqual(["pint", "phpcs", "prettier"]);
    expect(formattersForPath("src/core.clj")).toEqual(["cljfmt"]);
    expect(formattersForPath("src/Main.java")).toEqual(["checkstyle", "clang_format"]);
    expect(languageForInfoString("csharp")).not.toBeNull();
    expect(languageForInfoString("Common Lisp")).not.toBeNull();
    expect(languageForInfoString("golang")).not.toBeNull();
    expect(languageForInfoString("postgresql")).not.toBeNull();
    expect(prettyLayoutForBuffer("bin/deploy", "python")).toEqual(prettyLayoutForPath("deploy.py"));
    expect(formattersForBuffer("bin/deploy", "python")).toEqual(["ruff", "black"]);
  });
});
