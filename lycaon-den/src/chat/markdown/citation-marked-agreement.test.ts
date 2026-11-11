import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { marked, type Token } from "marked";
import { describe, expect, it } from "vitest";

type Fixture = { name: string; source: string; links: string[]; code: string[] };

const fixtures = JSON.parse(
  readFileSync(
    resolve(__dirname, "../../../../lycaon/internal/citationmd/testdata/marked-agreement.json"),
    "utf8",
  ),
) as Fixture[];

function summarize(source: string): { links: string[]; code: string[] } {
  const links: string[] = [];
  const code: string[] = [];
  void marked.walkTokens(marked.lexer(source, { gfm: true }), (token: Token) => {
    if (token.type === "link" && /^https?:/i.test(token.href)) links.push(token.href);
    if (token.type === "code" || token.type === "codespan") code.push(token.text);
  });
  return { links, code };
}

// The host's citation grammar is checked against these same rows, so the two
// Markdown parsers cannot drift apart unnoticed.
describe("marked agrees with the host citation grammar's fixtures", () => {
  it.each(fixtures)("$name", ({ source, links, code }) => {
    expect(summarize(source)).toEqual({ links, code });
  });
});
