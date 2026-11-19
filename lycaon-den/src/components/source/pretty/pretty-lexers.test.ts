// @vitest-environment jsdom
import { indentUnit } from "@codemirror/language";
import { EditorState, Text } from "@codemirror/state";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { finishFixtureParsing } from "../../../test/codemirror-parsing-fixture.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { ScriptLexer } from "./pretty-lexers.ts";
import { TextWindow } from "./pretty-text-window.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

type Span = [number, number];

function mergeAndClipSpans(raw: Span[], doc: string): Span[] {
  const sorted = raw.filter(([a, b]) => b > a).sort((x, y) => x[0] - y[0] || x[1] - y[1]);
  const merged: Span[] = [];
  for (const [from, to] of sorted) {
    const prev = merged[merged.length - 1];
    if (prev && from <= prev[1]) prev[1] = Math.max(prev[1], to);
    else merged.push([from, to]);
  }
  const clipped: Span[] = [];
  let lineStart = 0;
  while (lineStart <= doc.length) {
    const nl = doc.indexOf("\n", lineStart);
    const lineEnd = nl < 0 ? doc.length : nl;
    for (const [from, to] of merged) {
      if (to <= lineStart) continue;
      if (from >= lineEnd) break;
      const a = Math.max(from, lineStart);
      const b = Math.min(to, lineEnd);
      if (b > a) clipped.push([a, b]);
    }
    if (nl < 0) break;
    lineStart = nl + 1;
  }
  return clipped;
}

function scriptLexerSpans(doc: string, jsx: boolean, stride = 0): Span[] {
  const text = new TextWindow(Text.of(doc.split("\n")), null);
  let lexer = new ScriptLexer(jsx);
  const raw: Span[] = [];
  let lineStart = 0;
  while (lineStart <= doc.length) {
    const nl = doc.indexOf("\n", lineStart);
    const lineEnd = nl < 0 ? doc.length : nl;
    let pos = lineStart;
    let nextStop = stride > 0 ? pos + stride : lineEnd;
    while (pos < lineEnd) {
      if (stride > 0 && pos >= nextStop) {
        const cloned = lexer.copy();
        expect(cloned.equals(lexer)).toBe(true);
        lexer = cloned;
        nextStop = pos + stride;
      }
      const end = stride > 0 ? Math.min(lineEnd, nextStop) : lineEnd;
      const skipped = lexer.skip(text, pos, end, lineEnd);
      if (skipped > pos) {
        raw.push([pos, skipped]);
        pos = skipped;
      } else {
        lexer.noteCode(text, pos);
        pos += 1;
      }
    }
    lexer.endLine();
    if (nl < 0) break;
    lineStart = nl + 1;
  }
  return mergeAndClipSpans(raw, doc);
}

/**
 * Extracts reference protected spans with TypeScript's scanner (`ts.createScanner`)
 * driven by `reScanSlashToken`, `reScanTemplateToken`, and JSX tag/children contexts
 * from `ts.createSourceFile`.
 */
function tsScannerSpans(doc: string, jsx: boolean): Span[] {
  const sf = ts.createSourceFile(
    jsx ? "sample.tsx" : "sample.ts",
    doc,
    ts.ScriptTarget.Latest,
    true,
    jsx ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
  const regexStarts = new Set<number>();
  const templateRescans = new Set<number>();
  const jsxSpans: Span[] = [];

  const addJsxTagMinusExpressions = (tag: ts.Node, attrs?: ts.JsxAttributes) => {
    let cursor = tag.getStart(sf);
    if (attrs) {
      for (const prop of attrs.properties) {
        let expr: ts.JsxExpression | undefined;
        if (ts.isJsxSpreadAttribute(prop)) {
          jsxSpans.push([cursor, prop.getStart(sf)]);
          cursor = prop.end;
          continue;
        }
        if (prop.initializer && ts.isJsxExpression(prop.initializer)) {
          expr = prop.initializer;
        }
        if (expr) {
          jsxSpans.push([cursor, expr.getStart(sf)]);
          cursor = expr.end;
        }
      }
    }
    jsxSpans.push([cursor, tag.end]);
  };

  const visit = (node: ts.Node) => {
    if (ts.isRegularExpressionLiteral(node)) {
      regexStarts.add(node.getStart(sf));
    } else if (ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
      templateRescans.add(node.getStart(sf));
    } else if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      addJsxTagMinusExpressions(node, node.attributes);
    } else if (
      ts.isJsxClosingElement(node) ||
      ts.isJsxOpeningFragment(node) ||
      ts.isJsxClosingFragment(node)
    ) {
      jsxSpans.push([node.getStart(sf), node.end]);
    } else if (ts.isJsxText(node)) {
      jsxSpans.push([node.pos, node.end]);
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);

  const inJsxProtected = (pos: number): number => {
    for (const [from, to] of jsxSpans) {
      if (pos >= from && pos < to) return to;
    }
    return -1;
  };

  const scanner = ts.createScanner(
    ts.ScriptTarget.Latest,
    false,
    jsx ? ts.LanguageVariant.JSX : ts.LanguageVariant.Standard,
    doc,
  );
  const spans: Span[] = [...jsxSpans];
  for (;;) {
    const jump = inJsxProtected(scanner.getTextPos());
    if (jump >= 0) {
      scanner.setTextPos(jump);
    }
    let kind = scanner.scan();
    if (kind === ts.SyntaxKind.EndOfFileToken) break;
    const start = scanner.getTokenPos();
    if (
      (kind === ts.SyntaxKind.SlashToken || kind === ts.SyntaxKind.SlashEqualsToken) &&
      regexStarts.has(start)
    ) {
      kind = scanner.reScanSlashToken();
    } else if (kind === ts.SyntaxKind.CloseBraceToken && templateRescans.has(start)) {
      kind = scanner.reScanTemplateToken(false);
    }
    const end = scanner.getTextPos();
    if (
      kind === ts.SyntaxKind.StringLiteral ||
      kind === ts.SyntaxKind.RegularExpressionLiteral ||
      kind === ts.SyntaxKind.NoSubstitutionTemplateLiteral ||
      kind === ts.SyntaxKind.TemplateHead ||
      kind === ts.SyntaxKind.TemplateMiddle ||
      kind === ts.SyntaxKind.TemplateTail ||
      kind === ts.SyntaxKind.SingleLineCommentTrivia ||
      kind === ts.SyntaxKind.MultiLineCommentTrivia
    ) {
      spans.push([start, end]);
    }
  }
  return mergeAndClipSpans(spans, doc);
}

function prng(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 0x100000000;
  };
}

function generateScriptDocument(seed: number, targetTokens: number, jsx: boolean): string {
  const rand = prng(seed);
  const pick = <T>(items: readonly T[]): T => items[Math.floor(rand() * items.length)]!;
  const idents = ["alpha", "beta", "count", "item", "value", "box"];
  const regexWords = ["return", "typeof", "void", "throw"];

  const atom = (depth: number): string => {
    const r = rand();
    if (r < 0.14) return `${pick(idents)}! / 2`;
    if (r < 0.26) return `${pick(idents)}++ / 2`;
    if (r < 0.36) return `${pick(idents)}-- / 2`;
    if (r < 0.46) return `obj.${pick(["return", "default", "case", "in"])} / 2`;
    if (r < 0.56) return `/[a-z/}\\]]+/gi.test(${pick(idents)})`;
    if (r < 0.66) return `!/^a\\/b$/.test(${pick(idents)})`;
    if (r < 0.76) {
      return rand() < 0.4
        ? `"str \\" { } \\\nmore"`
        : `'single \\' /re/'`;
    }
    if (r < 0.88 && depth < 2) {
      const inner = atom(depth + 1);
      const tail = rand() < 0.35 ? `\\\nnext` : "end";
      return `\`head \${${inner}} mid \${{ k: "${pick(idents)}" }} ${tail}\``;
    }
    if (jsx && depth < 2 && r < 0.96) {
      const inner = atom(depth + 1);
      return rand() < 0.5
        ? `<Card title="a > b" data={${inner}} />`
        : `<Panel note='x / y'>Don't panic {${inner}} <Icon /></Panel>`;
    }
    return `(${pick(idents)} + 42) / 3`;
  };

  const stmts: string[] = [];
  let count = 0;
  while (count < targetTokens) {
    const choice = rand();
    if (choice < 0.25) {
      const kw = pick(regexWords);
      if (kw === "return" || kw === "throw") {
        stmts.push(`(() => { ${kw} /a[}/]b/i.test(${pick(idents)}); })();`);
      } else {
        stmts.push(`const v${count} = ${kw} /a\\/b/g;`);
      }
      count += 8;
    } else if (choice < 0.4 && jsx) {
      stmts.push(
        rand() < 0.5
          ? `const fn${count} = <T,>(x: T) => x;`
          : `const fn${count} = <T extends Record<string, unknown>>(x: T) => (<>{${atom(0)}}</>);`,
      );
      count += 12;
    } else if (choice < 0.6) {
      const cmt = rand() < 0.5 ? `/* } \` / ' */` : `// line comment with " ' \` / }\n`;
      stmts.push(`let x${count} = ${atom(0)}; ${cmt}`);
      count += 10;
    } else {
      stmts.push(`const r${count} = [${atom(0)}, ${atom(0)}];`);
      count += 14;
    }
  }
  return stmts.join("\n") + "\n";
}

function open(doc: string, path: string): EditorState {
  return finishFixtureParsing(EditorState.create({
    doc,
    extensions: [languageExtensionForPath(path)!, indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  }));
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

describe("ScriptLexer expression state, templates, and JSX", () => {
  it("matches ts.createScanner across 500 seeded 200-token JS/TS documents", () => {
    for (let seed = 1; seed <= 500; seed++) {
      const doc = generateScriptDocument(seed, 200, false);
      const stride = seed % 10 === 0 ? 7 + (seed % 19) : 0;
      expect(scriptLexerSpans(doc, false, stride), `JS/TS seed ${seed}`).toEqual(
        tsScannerSpans(doc, false),
      );
    }
  });

  it("matches ts.createScanner across 500 seeded 200-token JSX/TSX documents", () => {
    for (let seed = 1_001; seed <= 1_500; seed++) {
      const doc = generateScriptDocument(seed, 200, true);
      const stride = seed % 10 === 0 ? 5 + (seed % 17) : 0;
      expect(scriptLexerSpans(doc, true, stride), `JSX/TSX seed ${seed}`).toEqual(
        tsScannerSpans(doc, true),
      );
    }
  });

  it("preserves expression and JSX context without swallowing following objects into regexes, strings, or templates", () => {
    const obj = `{${Array.from({ length: 30 }, (_, i) => `k${i}: ${i}`).join(", ")}}`;
    const controlCount = breaks(open(`const a = x / 2; const b = ${obj};\n`, "probe.ts")).length;
    expect(controlCount).toBeGreaterThanOrEqual(30);

    for (const prefix of [
      "const a = x! / 2;",
      "const a = i++ / 2;",
      "const a = `hello \\\n`;",
      'const a = `${ /}/.test(s) ? "`" : "" }`;',
      'const a = `${ /* } */ "`" }`;',
    ]) {
      const doc = `${prefix} const b = ${obj};\n`;
      expect(breaks(open(doc, "probe.ts")).length, prefix).toBeGreaterThanOrEqual(30);
    }

    for (const jsxPrefix of [
      "const el = <A b={c} />;",
      "const el = <div>x</div>;",
      "const el = <p>Don't panic</p>;",
      "const id = <T,>(x: T) => x;",
      "const id = <T extends Foo>(x: T) => x;",
    ]) {
      const doc = `${jsxPrefix} const b = ${obj};\n`;
      expect(breaks(open(doc, "probe.tsx")).length, jsxPrefix).toBeGreaterThanOrEqual(30);
    }
  });

  it("converges from every single-character checkpoint offset inside nested templates and JSX", () => {
    const sample =
      'const view = <Card label="a > b">{`outer ${/}/.test(s) ? "`" : `inner ${x! / 2}`} tail`}</Card>;\n';
    const full = scriptLexerSpans(sample, true, 0);
    expect(full).toEqual(tsScannerSpans(sample, true));
    for (let stride = 1; stride <= 16; stride++) {
      expect(scriptLexerSpans(sample, true, stride), `stride ${stride}`).toEqual(full);
    }
  });

  it("breaks between adjacent JSX tags and inside expression props while leaving formatted JSX untouched", () => {
    const pad = "x".repeat(200);
    const tsx = `export function App() { return (<Layout data={{ a: 1, b: "${pad}" }}><Sidebar /><Main /></Layout>); }\n`;
    const state = open(tsx, "App.tsx");
    const pos = breaks(state);
    expect(pos).toContain(tsx.indexOf("<Sidebar />"));
    expect(pos).toContain(tsx.indexOf("<Main />"));
    expect(pos).toContain(tsx.indexOf("</Layout>"));

    const textWithExpr = `export function Greet() { const pad = "${pad}"; return (<p>Hello {user.name} world</p>); }\n`;
    const greetBreaks = breaks(open(textWithExpr, "Greet.tsx"));
    const worldIdx = textWithExpr.indexOf("world");
    expect(greetBreaks).not.toContain(worldIdx);

    const formatted = [
      "export function App() {",
      "  return (",
      "    <Layout>",
      "      <Sidebar />",
      "      <Main />",
      "    </Layout>",
      "  );",
      "}",
    ].join("\n");
    expect(breaks(open(formatted, "App.tsx"))).toEqual([]);
  });
});
