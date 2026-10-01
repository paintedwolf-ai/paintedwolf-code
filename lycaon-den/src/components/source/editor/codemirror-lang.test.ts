import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { ensureSyntaxTree } from "@codemirror/language";
import {
  languageExtensionForPath,
  languageLabelForDetectedLanguage,
  languageForInfoString,
  languageLabelForPath,
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
    expect(languageLabelForPath(".gitignore")).toBe("Git ignore");
    expect(languageLabelForPath(".env")).toBe("Dotenv");
    expect(languageLabelForPath("go.mod")).toBe("Go module");
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
});
