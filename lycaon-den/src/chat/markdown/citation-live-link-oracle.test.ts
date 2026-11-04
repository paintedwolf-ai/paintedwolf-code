// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { renderMarkdownHtml } from "./markdown-render.ts";

type Corpus = { validated: string[]; cases: { source: string; present: string }[] };

const corpus = JSON.parse(
  readFileSync(
    resolve(__dirname, "../../../../lycaon/internal/guidance/testdata/live-link-oracle.json"),
    "utf8",
  ),
) as Corpus;

/** What a browser would follow out to the web from rendered accepted speech. */
function liveWebHrefs(present: string): string[] {
  const holder = document.createElement("div");
  holder.innerHTML = renderMarkdownHtml(present, { projectId: "project" });
  return Array.from(holder.querySelectorAll("a[href], area[href]"))
    .map((node) => node.getAttribute("href") ?? "")
    .filter((href) => /^\s*(https?:|\/\/)/i.test(href));
}

// The host presents each source with only the validated URLs observed. Nothing else
// may come out live, whatever the Markdown parser and sanitizer make of it.
describe("accepted speech renders no unvalidated live link", () => {
  const validated = new Set(corpus.validated);

  it("covers the corpus", () => {
    const escaped: { source: string; present: string; href: string }[] = [];
    let live = 0;
    for (const { source, present } of corpus.cases) {
      for (const href of liveWebHrefs(present)) {
        if (validated.has(href)) live += 1;
        else escaped.push({ source, present, href });
      }
    }
    expect(escaped).toEqual([]);
    expect(live).toBeGreaterThan(100);
  });
});
