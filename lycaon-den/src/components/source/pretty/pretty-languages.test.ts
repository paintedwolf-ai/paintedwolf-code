import { indentUnit } from "@codemirror/language";
import { EditorState, type Extension } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath, prettyLayoutCatalog } from "../editor/codemirror-lang.ts";
import { createPrettyExtension, prettyOutcomeOf } from "./pretty-layout.ts";
import { PrettyBreakWidget, prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, path: string): EditorState {
  const language = languageExtensionForPath(path);
  const extensions: Extension[] = [indentUnit.of("  "), EditorState.tabSize.of(4), createPrettyExtension({ enabled: true, path })];
  return EditorState.create({ doc, extensions: language ? [language, ...extensions] : extensions });
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

/** How each language writes a string and a trailing comment that hold separators and brackets. */
const LITERALS: Record<string, { string: string; comment: string }> = {
  default: { string: '"s, (t); [u]"', comment: "// c, (d); [e]" },
  hash: { string: '"s, (t); [u]"', comment: "# c, (d); [e]" },
  dash: { string: '"s, (t); [u]"', comment: "-- c, (d); [e]" },
  percent: { string: '"s, (t); [u]"', comment: "% c, (d); [e]" },
  ml: { string: '"s, (t); [u]"', comment: "(* c, (d); [e] *)" },
};
const HASH = new Set(["py", "pyi", "yaml", "yml", "toml", "rb", "cr", "ex", "exs", "r", "jl", "al", "perl", "ph", "pl", "plx", "pm", "psgi", "t", "promql", "sass"]);
const DASH = new Set(["lua", "hs", "elm"]);
const PERCENT = new Set(["erl", "hrl"]);
const ML = new Set(["ml", "mli"]);
const YAML = new Set(["yaml", "yml"]);

function literalsFor(ext: string) {
  if (HASH.has(ext)) return LITERALS.hash!;
  if (DASH.has(ext)) return LITERALS.dash!;
  if (PERCENT.has(ext)) return LITERALS.percent!;
  if (ML.has(ext)) return LITERALS.ml!;
  return LITERALS.default!;
}

function denseLine(ext: string): { text: string; protectedRanges: Array<[number, number]> } {
  const { string, comment } = literalsFor(ext);
  const items = Array.from({ length: 32 }, (_, i) => `item${i}`);
  // YAML reads brackets as flow collections only after a key.
  const head = YAML.has(ext)
    ? `value: [${items.slice(0, 8).join(", ")}, [${items.slice(8, 16).join(", ")}], {${items.slice(16).map((item) => `${item}: 1`).join(", ")}}, `
    : `value = call(${items.slice(0, 8).join(", ")}, [${items.slice(8, 16).join(", ")}], (${items.slice(16).join(", ")}), `;
  const text = `${head}${string}${YAML.has(ext) ? "]" : ")"} ${comment}`;
  const stringFrom = head.length;
  const commentFrom = stringFrom + string.length + 2;
  return { text, protectedRanges: [[stringFrom, stringFrom + string.length], [commentFrom, text.length]] };
}

const bracketLanguages = prettyLayoutCatalog().filter(({ pretty }) => pretty.kind === "bracket");
const noLayoutLanguages = prettyLayoutCatalog().filter(({ pretty }) => pretty.kind === "none");

describe("bracket layout for every language declared with it", () => {
  for (const { ext, pretty } of bracketLanguages) {
    it(`lays out a wide line and leaves formatted source alone (.${ext})`, () => {
      const path = `sample.${ext}`;
      const { text, protectedRanges } = denseLine(ext);
      expect(text.length).toBeGreaterThan(240);
      const state = open(text, path);
      expect(prettyOutcomeOf(state).kind, ext).toBe("active");
      const positions = breaks(state);
      expect(positions.length, ext).toBeGreaterThan(0);
      for (const pos of positions) {
        for (const [from, to] of protectedRanges) expect(pos > from && pos < to, `${ext} break at ${pos} inside ${from}-${to}`).toBe(false);
      }
      expect(breaks(open(writtenOut(state), path)), `${ext} second pass`).toEqual([]);
      expect(breaks(open("value = call(item0, [item1], (item2))\n", path)), `${ext} formatted`).toEqual([]);

      const malformed = state.update({ changes: { from: text.indexOf("["), to: text.indexOf("[") + 1 } }).state;
      expect(prettyOutcomeOf(malformed).kind, `${ext} after deleting a bracket`).toBe("active");
      expect(malformed.doc.length).toBe(text.length - 1);

      const semicolons = `${"step();".repeat(40)}step()`;
      const statements = breaks(open(semicolons, path)).length > 0;
      expect(statements, `${ext} statement breaks`).toBe(pretty.kind === "bracket" && pretty.statements);
    });
  }

  it("keeps a Go header clause on its line", () => {
    const line = `if err := load(${Array.from({ length: 60 }, (_, i) => `a${i}`).join(", ")}); err != nil { return err }`;
    const state = open(line, "main.go");
    const semicolon = line.indexOf(";");
    expect(breaks(state)).not.toContain(line.indexOf("err", semicolon));
    expect(breaks(state).length).toBeGreaterThan(0);
  });

  it("disables Pretty for every language declared without a layout", () => {
    for (const { ext } of noLayoutLanguages) {
      expect(prettyOutcomeOf(open("echo {a,b}", `sample.${ext}`)).kind, ext).toBe("unavailable");
    }
  });
});

describe("SQL clause layout", () => {
  const statement = "SELECT u.id, u.email, u.display_name, count(s.id) AS session_count, max(s.updated_at) AS last_active FROM users u LEFT JOIN sessions s ON s.user_id = u.id AND s.archived = false WHERE u.created_at > $1 AND u.email NOT LIKE '%@example.test' GROUP BY u.id, u.email, u.display_name ORDER BY last_active DESC LIMIT 50";

  it("breaks before each clause and lists a clause that does not fit", () => {
    expect(statement.length).toBeGreaterThan(240);
    expect(writtenOut(open(statement, "query.sql")).split("\n")).toEqual([
      "SELECT",
      "  u.id,",
      "  u.email,",
      "  u.display_name,",
      "  count(s.id) AS session_count,",
      "  max(s.updated_at) AS last_active",
      "FROM users u",
      "LEFT JOIN sessions s ON s.user_id = u.id AND s.archived = false",
      "WHERE u.created_at > $1 AND u.email NOT LIKE '%@example.test'",
      "GROUP BY u.id, u.email, u.display_name",
      "ORDER BY last_active DESC",
      "LIMIT 50",
    ]);
  });

  it("adds no break for keywords in strings, comments or right after a parenthesis", () => {
    const columns = Array.from({ length: 30 }, (_, i) => `c${i}`).join(", ");
    const query = `SELECT 'select from where', ${columns} FROM t WHERE id IN (SELECT id) AND note = 'group by order by' -- from where\n`;
    const state = open(query, "query.sql");
    const positions = breaks(state);
    expect(positions).not.toContain(query.indexOf("SELECT id"));
    expect(positions).toContain(query.indexOf("FROM t"));
    for (const word of ["select from where", "group by order by", "from where\n"]) {
      const at = query.indexOf(word);
      expect(positions.some((pos) => pos > at && pos < at + word.length)).toBe(false);
    }
  });

  it("adds nothing to a formatted statement or on a second pass", () => {
    expect(breaks(open("SELECT id\nFROM users\nWHERE ok\n", "query.sql"))).toEqual([]);
    expect(breaks(open(writtenOut(open(statement, "query.sql")), "query.sql"))).toEqual([]);
    expect(breaks(open("SELECT id FROM users WHERE ok ORDER BY id", "query.sql"))).toEqual([]);
  });
});

describe("JSON-like unknown content", () => {
  const unknown = (text: string) => breaks(EditorState.create({ doc: text, extensions: [createPrettyExtension({ enabled: true })] })).length > 0;

  it("lays out a printed dict or list", () => {
    expect(unknown("{'id': 7, 'tags': ['a', 'b'], 'active': True, 'parent': None,}")).toBe(true);
    expect(unknown("[{'a': 1}, {'b': False}]")).toBe(true);
  });

  it("still leaves prose, diagnostics and code alone", () => {
    for (const text of [
      "src/a.ts(12,5): error TS2345: Argument of type '{ id: number; }' is not assignable.",
      "Ship it (soon), then tell [the team], {maybe} tomorrow.",
      "def f(x):\n    return {'a': [1, 2]}",
      "{'unclosed': [1, 2}",
    ]) {
      expect(unknown(text), text).toBe(false);
    }
  });
});
