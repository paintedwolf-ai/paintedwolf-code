// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { CitationGrounding } from "../../api/types.ts";
import { renderMarkdownHtml } from "./markdown-render.ts";
import { buildProseCitationIndex } from "./prose-path-opens.ts";

type Corpus = { validated: string[]; cases: { source: string; present: string }[] };

const corpus = JSON.parse(
  readFileSync(
    resolve(__dirname, "../../../../lycaon/internal/guidance/testdata/live-link-oracle.json"),
    "utf8",
  ),
) as Corpus;

// A cited path makes the frontend re-escape its text run, a separate path through the
// renderer that must hold the same guarantee.
const citations = buildProseCitationIndex({ cited_evidence: [{ path: "a.go" }] } as unknown as CitationGrounding);

function render(present: string, cited: boolean): HTMLElement {
  const holder = document.createElement("div");
  holder.innerHTML = renderMarkdownHtml(present, { projectId: "project", citations: cited ? citations : undefined });
  return holder;
}

/** What a browser would follow out to the web from rendered accepted speech. */
function liveWebHrefs(holder: HTMLElement): string[] {
  return Array.from(holder.querySelectorAll("a[href], area[href]"))
    .map((node) => node.getAttribute("href") ?? "")
    .filter((href) => /^\s*(https?:|\/\/)/i.test(href));
}

// The host presents each source with only the validated URLs observed. Nothing else
// may come out live, whatever the Markdown parser and sanitizer make of it.
describe("accepted speech renders no unvalidated live link", () => {
  const validated = new Set(corpus.validated);

  it.each([false, true])("covers the corpus, with a citation index: %s", (cited) => {
    const escaped: { source: string; present: string; href: string }[] = [];
    let live = 0;
    for (const { source, present } of corpus.cases) {
      for (const href of liveWebHrefs(render(present, cited))) {
        if (validated.has(href)) live += 1;
        else escaped.push({ source, present, href });
      }
    }
    expect(escaped).toEqual([]);
    expect(live).toBeGreaterThan(100);
  });

  it("reads correctly with a cited path in the same run", () => {
    const rows = corpus.cases.filter(({ source }) => !source.startsWith("\\") && /see a\.go|a\.go now/.test(source));
    expect(rows.length).toBeGreaterThan(4);
    for (const { source, present } of rows) {
      const text = render(present, true).textContent ?? "";
      expect({ source, text }).toEqual({ source, text: expect.not.stringMatching(/&(#\d+|lt|gt|amp);|\/) });
      expect({ source, text }).toEqual({ source, text: expect.stringContaining("a.go") });
    }
  });

  it.each([
    ["1\. see a.go now", "1. see a.go now"],
    ["https\://unobs.test/a see a.go", "https://unobs.test/a see a.go"],
    ["www\.unobs.test see a.go", "www.unobs.test see a.go"],
    ['\<a href="https\://unobs\.test"\>see a.go\</a\>', '<a href="https://unobs.test">see a.go</a>'],
  ])("presents %s", (present, reads) => {
    const holder = render(present, true);
    expect(holder.textContent).toBe(reads);
    expect(liveWebHrefs(holder)).toEqual([]);
  });
});
