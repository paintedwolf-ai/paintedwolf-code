import { describe, expect, it } from "vitest";
import type { CitationGrounding, NavigationReference } from "../../api/types.ts";
import {
  buildProseCitationIndex,
  buildProseNavigationIndex,
  linkifyProsePathsInText,
  matchProseCitation,
  matchProseNavigation,
  renderProsePathBlock,
  renderProsePathCodespan,
} from "./prose-path-opens.ts";
import { parseProsePathCandidate } from "./prose-path-parse.ts";

function grounding(overrides: Partial<CitationGrounding> = {}): CitationGrounding {
  return { traced: true, ...overrides };
}

function index(overrides: Partial<CitationGrounding> = {}) {
  const idx = buildProseCitationIndex(grounding(overrides));
  expect(idx).toBeTruthy();
  return idx!;
}

function nav(
  mention: string,
  path = mention,
  entryKind: "file" | "folder" = "file",
 syntax = "code",
): NavigationReference {
  return {
    id: mention, syntax, status: "resolved", explicit: true, mention,
    project_id: "p-nav",
    root_id: "r-nav",
    path,
    entry_kind: entryKind,
  };
}

describe("buildProseCitationIndex", () => {
  it("returns undefined without grounding or cited paths", () => {
    expect(buildProseCitationIndex(undefined)).toBeUndefined();
    expect(buildProseCitationIndex(grounding())).toBeUndefined();
    expect(
      buildProseCitationIndex(grounding({ cited_evidence: [{ handle: "read#1" }] })),
    ).toBeUndefined();
  });

  it("indexes cited evidence and findings, excluding openable: false", () => {
    const idx = index({
      cited_evidence: [
        { path: "src/a.ts", line: 10 },
        { path: "src/dir", openable: false },
      ],
      findings: [{ path: "src/b.ts", line: 20 }],
    });
    expect(idx.paths.get("src/a.ts")).toBe(10);
    expect(idx.paths.get("src/b.ts")).toBe(20);
    expect(idx.paths.has("src/dir")).toBe(false);
    expect(idx.blocked.has("src/dir")).toBe(true);
  });

  it("includes file_region evidence records and no other shapes", () => {
    const idx = index({
      cited_evidence: [{ path: "src/a.ts" }],
      evidence_records: [
        { shape: "file_region", path: "src/read.ts", line: 7 },
        { shape: "artifact", path: "scans/pack.json" },
      ],
    });
    expect(idx.paths.get("src/read.ts")).toBe(7);
    expect(idx.paths.has("scans/pack.json")).toBe(false);
  });

  it("keeps the cited line over a record's line for the same path", () => {
    const idx = index({
      cited_evidence: [{ path: "src/a.ts", line: 42 }],
      evidence_records: [{ shape: "file_region", path: "src/a.ts", line: 7 }],
    });
    expect(idx.paths.get("src/a.ts")).toBe(42);
  });

  it("normalizes ./ prefixes and backslashes", () => {
    const idx = index({ cited_evidence: [{ path: "./src\\a.ts", line: 3 }] });
    expect(idx.paths.get("src/a.ts")).toBe(3);
  });

  it("uses the same key for equivalent citation sets regardless of order", () => {
    const first = index({
      cited_evidence: [
        { path: "src/b.ts", line: 2 },
        { path: "src/a.ts", line: 1 },
      ],
    });
    const reordered = index({
      cited_evidence: [
        { path: "./src/a.ts", line: 1 },
        { path: "src/b.ts", line: 2 },
      ],
    });
    expect(reordered.key).toBe(first.key);
  });

  it("changes the key when a citation path, line, or openability changes", () => {
    const first = index({ cited_evidence: [{ path: "src/a.ts", line: 1 }] });
    for (const citation of [
      { path: "src/b.ts", line: 1 },
      { path: "src/a.ts", line: 2 },
      { path: "src/a.ts", line: 1, openable: false },
    ]) {
      expect(index({ cited_evidence: [citation] }).key).not.toBe(first.key);
    }
  });
});

describe("matchProseCitation", () => {
  const idx = index({
    cited_evidence: [
      { path: "lycaon-den/src/chat/actions/queue-actions.ts", line: 42 },
      { path: "src/util/types.ts" },
      { path: "docs/types.ts" },
    ],
  });

  it("matches an exact path and keeps the prose line", () => {
    expect(
      matchProseCitation(idx, "lycaon-den/src/chat/actions/queue-actions.ts:57"),
    ).toEqual({ path: "lycaon-den/src/chat/actions/queue-actions.ts", line: 57 });
  });

  it("falls back to the cited line when the prose has none", () => {
    expect(
      matchProseCitation(idx, "lycaon-den/src/chat/actions/queue-actions.ts"),
    ).toEqual({ path: "lycaon-den/src/chat/actions/queue-actions.ts", line: 42 });
  });

  it("does not infer basename uniqueness from a cited subset", () => {
    expect(matchProseCitation(idx, "queue-actions.ts")).toBeUndefined();
    expect(matchProseCitation(idx, "actions/queue-actions.ts:9")).toBeUndefined();
  });

  it("preserves the full prose line range", () => {
    expect(matchProseCitation(idx, "lycaon-den/src/chat/actions/queue-actions.ts:10-20")).toEqual({
      path: "lycaon-den/src/chat/actions/queue-actions.ts",
      line: 10,
      endLine: 20,
    });
  });

  it("rejects ambiguous suffixes", () => {
    expect(matchProseCitation(idx, "types.ts")).toBeUndefined();
  });

  it("rejects non-aligned fragments and unknown paths", () => {
    expect(matchProseCitation(idx, "ueue-actions.ts")).toBeUndefined();
    expect(matchProseCitation(idx, "src/other.ts")).toBeUndefined();
    expect(matchProseCitation(idx, "queue actions")).toBeUndefined();
  });
});

describe("durable prose navigation", () => {
  const navigation = buildProseNavigationIndex([
    nav("install-all.sh:12", "scripts/install-all.sh"),
    nav("meta-packs/", "meta-packs", "folder"),
  ])!;

  it("binds an exact mention to its host-validated target and prose line", () => {
    expect(matchProseNavigation(navigation, "install-all.sh:12")).toEqual({
      reference: "install-all.sh:12",
      jobId: undefined,
      projectId: "p-nav",
      rootId: "r-nav",
      path: "scripts/install-all.sh",
      entryKind: "file",
      line: 12,
      endLine: undefined,
    });
  });

  it("keeps a trailing slash as a folder mention", () => {
    expect(matchProseNavigation(navigation, "meta-packs/")?.entryKind).toBe("folder");
  });

  it("rejects unknown and conflicting mentions", () => {
    expect(matchProseNavigation(navigation, "validate-all.sh")).toBeUndefined();
    expect(buildProseNavigationIndex([
      nav("same.ts", "a/same.ts"),
      { ...nav("same.ts", "b/same.ts"), root_id: "r-other" },
    ])).toBeDefined();
  });
});

describe("parseProsePathCandidate", () => {
  it("parses hash fragments without a colon line", () => {
    expect(parseProsePathCandidate("a/b.ts#42")).toEqual({ path: "a/b.ts", line: 42 });
  });

  it("parses inclusive start-end line ranges", () => {
    expect(parseProsePathCandidate("src/main.ts:12-34")).toEqual({
      path: "src/main.ts",
      line: 12,
      endLine: 34,
    });
  });
});

describe("renderProsePathCodespan", () => {
  const idx = index({ cited_evidence: [{ path: "src/a.ts", line: 5 }] });

  it("opens an exact cited path", () => {
    const html = renderProsePathCodespan("src/a.ts:5", "p1", { index: idx });
    expect(html).toContain('class="den-source-path-link"');
    expect(html).toContain('data-den-source-path="src/a.ts"');
    expect(html).toContain('data-den-source-line="5"');
    expect(html).toContain("<code>src/a.ts:5</code>");
  });

  it("renders an inclusive line range onto the source button", () => {
    const html = renderProsePathCodespan("src/a.ts:2-5", "p1", { index: idx });
    expect(html).toContain('data-den-source-line="2"');
    expect(html).toContain('data-den-source-end-line="5"');
  });

  it("opens an uncited durable navigation target", () => {
    const html = renderProsePathCodespan("docs/openapi.yaml", "p1", {
      navigation: buildProseNavigationIndex([
        nav("docs/openapi.yaml", "docs/openapi.yaml"),
      ]),
    });
    expect(html).toContain('data-den-source-path="docs/openapi.yaml"');
    expect(html).toContain("data-tip-when-clipped");
  });

  it("leaves unconfirmed uncited paths plain", () => {
    expect(renderProsePathCodespan("docs/openapi.yaml", "p1")).toBeUndefined();
  });

  it("returns undefined for non-path spans", () => {
    expect(renderProsePathCodespan("npm install", "p1", { index: idx })).toBeUndefined();
  });
});

describe("renderProsePathBlock", () => {
  const navigation = buildProseNavigationIndex([
    nav(
      "lycaon/config/packs/painted-wolf/security/host/detection-packs/",
      "lycaon/config/packs/painted-wolf/security/host/detection-packs",
      "folder",
      "fence",
    ),
  ]);

  it("opens one exact host-validated path while preserving preformatted presentation", () => {
    const html = renderProsePathBlock(
      "lycaon/config/packs/painted-wolf/security/host/detection-packs/\n",
      "p1",
      { navigation },
    );
    expect(html).toContain('<pre class="den-source-path-block"><code>');
    expect(html).toContain(
      'data-den-source-path="lycaon/config/packs/painted-wolf/security/host/detection-packs"',
    );
    expect(html).toContain('data-den-project-path-kind="folder"');
    expect(html).toContain('data-den-project-root-id="r-nav"');
  });

  it("leaves multiline and unvalidated blocks untouched", () => {
    expect(renderProsePathBlock("src/a.ts\nsrc/b.ts", "p1", { navigation })).toBeUndefined();
    expect(renderProsePathBlock("src/missing.ts", "p1", { navigation })).toBeUndefined();
  });
});

describe("linkifyProsePathsInText", () => {
  const idx = index({
    cited_evidence: [
      { path: "src/a.ts", line: 5 },
      { path: "src/b.ts" },
    ],
  });

  it("links bare mentions and escapes the surrounding text", () => {
    const html = linkifyProsePathsInText(
      "The <fix> lands in src/a.ts:12 & src/b.ts.",
      "p1",
      { index: idx },
    );
    expect(html).toContain('data-den-source-path="src/a.ts"');
    expect(html).toContain('data-den-source-line="12"');
    expect(html).toContain('data-den-source-path="src/b.ts"');
    expect(html).toContain("The &lt;fix&gt; lands in ");
    expect(html).toContain(" &amp; ");
    expect(html).toContain(">src/b.ts</button>.");
  });

  it("links durable uncited targets in the same run", () => {
    const navigation = buildProseNavigationIndex([
      nav("docs/openapi.yaml", undefined, "file", "text"),
      nav("lycaon-den/src/api/types.ts", undefined, "file", "text"),
    ]);
    const html = linkifyProsePathsInText(
      "Never hand-edit docs/openapi.yaml or lycaon-den/src/api/types.ts.",
      "p1",
      { navigation },
    );
    expect(html).toContain('data-den-source-path="docs/openapi.yaml"');
    expect(html).toContain('data-den-source-path="lycaon-den/src/api/types.ts"');
  });

  it("preserves an exact backslash mention", () => {
    const navigation = buildProseNavigationIndex([
      nav("scripts\\validate-all.sh", "scripts/validate-all.sh", "file", "text"),
    ]);
    const html = linkifyProsePathsInText(
      "Run scripts\\validate-all.sh.",
      "p1",
      { navigation },
    );
    expect(html).toContain('data-den-source-path="scripts/validate-all.sh"');
  });

  it("does not treat a longer token as a cited-path suffix", () => {
    const navigation = buildProseNavigationIndex([nav("barsrc/a.ts", undefined, "file", "text")]);
    const html = linkifyProsePathsInText("see barsrc/a.ts here", "p1", {
      index: idx,
      navigation,
    });
    expect(html).toContain('data-den-source-path="barsrc/a.ts"');
    expect(html).not.toContain('data-den-source-path="src/a.ts"');
    expect(
      linkifyProsePathsInText("see notb.ts", "p1", { index: idx }),
    ).toBeUndefined();
  });

  it("returns undefined when nothing matches", () => {
    expect(linkifyProsePathsInText("plain prose, no paths", "p1")).toBeUndefined();
  });

  it("does not linkify extensionless single words in plain text without path separators or explicit status", () => {
    const navigation = buildProseNavigationIndex([
      nav("task", undefined, "file", "text"),
    ]);
    const html = linkifyProsePathsInText("The next task is to verify.", "p1", { navigation });
    expect(html).toBeUndefined();
  });
});

