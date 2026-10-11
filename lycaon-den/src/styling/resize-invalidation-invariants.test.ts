// @vitest-environment jsdom
import { readSourceText } from "../test/stylesheet-source.ts";
import { describe, expect, it } from "vitest";
import {
  CHAT_COL_MIN,
  DIVIDER_PX,
  SPLIT_MIN_HOST,
  STAGE_COL_MIN,
} from "../shell/stage-placement.ts";
import { join } from "node:path";
import { extractCssRuleBlock } from "./css-contract.ts";
import { denRoot, denSourceRoot, loadSourceCorpus } from "../test/source-corpus.ts";

const corpus = loadSourceCorpus(denSourceRoot, {
  extensions: [".css", ".ts", ".tsx"],
});

function read(rel: string): string {
  // Stylesheets are import manifests; read them in cascade order.
  if (rel.endsWith(".css")) return readSourceText(join(denSourceRoot, rel));
  const file = corpus.file(rel);
  if (!file) throw new Error(`source corpus file not found: ${rel}`);
  return file.text;
}

/** Outside the src corpus, and named rather than scanned. */
function readDenFile(rel: string): string {
  return readSourceText(join(denRoot, rel), "utf8");
}

/** Custom properties registered `inherits: false` in global.css. */
function registeredNonInheriting(css: string): Set<string> {
  const found = new Set<string>();
  const re = /@property\s+(--[\w-]+)\s*\{([^}]*)\}/g;
  for (let m = re.exec(css); m; m = re.exec(css)) {
    if (/inherits:\s*false/.test(m[2]!)) found.add(m[1]!);
  }
  return found;
}

/** Finds px-valued layout properties written by components, as style entries or imperative writes. */
function pxValuedCustomProps(source: string): string[] {
  const found: string[] = [];
  const re = /"(--den-[\w-]+)":\s*(?:`[^`]*px`|[^,\n]*\?[^,\n]*"0px"[^,\n]*)|setProperty\("(--den-[\w-]+)",\s*`[^`]*px`\)/g;
  for (let m = re.exec(source); m; m = re.exec(source)) found.push((m[1] ?? m[2])!);
  return found;
}

describe("resize invalidation", () => {
  const globalCss = read("global.css");
  const registered = registeredNonInheriting(globalCss);

  it("registers every live layout-geometry property as non-inheriting", () => {
    // `inherits: false` only holds if every reader is the declaring element.
    expect(registered).toContain("--den-nav-slot-width");
    expect(registered).toContain("--den-split-chat-w");
    expect(registered).toContain("--den-detail-w");
    expect(registered).toContain("--den-detail-h");
    expect(registered).toContain("--den-list-cols");
    expect(registered).toContain("--den-editor-block-left");
    expect(registered).toContain("--den-editor-block-w");
  });

  it("keeps registrations on the universal syntax so var() fallbacks survive", () => {
    // A typed syntax needs `initial-value`, which disables `var()` fallbacks.
    const re = /@property\s+--den-[\w-]+\s*\{([^}]*)\}/g;
    for (let m = re.exec(globalCss); m; m = re.exec(globalCss)) {
      expect(m[1]).toMatch(/syntax:\s*"\*"/);
      expect(m[1]).not.toMatch(/initial-value/);
    }
  });

  it("declares no live geometry property that is not registered", () => {
    const unregistered: string[] = [];
    for (const file of corpus.files) {
      if (!/\.tsx?$/.test(file.rel) || /\.test\.tsx?$/.test(file.rel)) continue;
      for (const prop of pxValuedCustomProps(file.text)) {
        if (registered.has(prop)) continue;
        unregistered.push(`${file.rel} → ${prop}`);
      }
    }
    // Live geometry is registered and read on its declaring element.
    expect(unregistered).toEqual([]);
  });

  it("frames editor block chrome on the block that reads it", () => {
    // The frame is stamped per block, so the write never restyles the editor's lines.
    const widgets = read("components/source/editor/solid-block-widget.ts");
    expect(widgets).toMatch(/host\.style\.setProperty\("--den-editor-block-left"/);
    expect(widgets).toMatch(/host\.style\.setProperty\("--den-editor-block-w"/);
    const block = extractCssRuleBlock(read("components/source/reader/source-reader.css"), ".cm-den-block");
    expect(block).toMatch(/position:\s*sticky/);
    expect(block).toMatch(/left:\s*var\(--den-editor-block-left, 0px\)/);
    expect(block).toMatch(/width:\s*var\(--den-editor-block-w, 100%\)/);
  });

  it("declares tree geometry on the pane that reads it", () => {
    expect(registered).toContain("--den-files-tree-w");
    expect(read("files/components/ProjectFilesView.tsx")).toMatch(
      /<FilesNavigationPane[\s\S]*?style=\{treeStyle\(\)\}/,
    );
    expect(read("files/components/FilesNavigationPane.tsx")).toMatch(/<FilesTreePane[\s\S]*?style=\{props\.style\}/);
    expect(read("files/components/FilesNavigationPane.tsx")).toMatch(/<FilesTreePane[\s\S]*?style=\{props\.style\}/);
    expect(read("files/components/FilesTreePane.tsx")).toMatch(/class="den-files-tree-pane"[\s\S]*?style=\{props\.style\}/);
  });

  it("keeps the nav slot width off the shell root", () => {
    const shell = read("components/shell/Shell.tsx");
    const shellRoot = /class="den-shell"[\s\S]{0,400}?>/.exec(shell)?.[0] ?? "";
    expect(shellRoot).not.toContain("--den-nav-slot-width");
    // Both readers declare it themselves.
    expect(read("components/shell/WorkspaceSidebar.tsx")).toMatch(
      /class="den-shell-aside"\s*\n\s*style=\{\{ "--den-nav-slot-width"/,
    );
    expect(read("components/shell/WorkspaceOverlays.tsx")).toMatch(
      /class="den-find-bar-host"[\s\S]{0,160}?"--den-nav-slot-width"/,
    );
  });

  it("gives the stage whatever the conversation leaves of the live host box", () => {
    const shellCss = read("shell-domain.css");
    const split = extractCssRuleBlock(shellCss, ".den-shell-stage-host--split");

    // The live host width resolves the stage entirely in CSS.
    expect(split).toMatch(/--den-split-chat-w/);
    expect(split).not.toMatch(/--den-split-stage-width/);
    expect(split).toMatch(
      new RegExp(`--den-split-chat-min:\\s*${CHAT_COL_MIN}px`),
    );
    expect(split).toMatch(
      new RegExp(`--den-split-stage-min:\\s*${STAGE_COL_MIN}px`),
    );

    // Both floors, and the collapse below the split minimum, live in the track.
    expect(split).toMatch(/clamp\(\s*var\(--den-split-chat-min\)/);
    expect(split).toMatch(
      /calc\(100% - var\(--den-split-stage-min\) - var\(--den-split-divider-px\)\)/,
    );
    expect(split).toMatch(
      new RegExp(`max\\(0px, calc\\(\\(100% - ${SPLIT_MIN_HOST - 1}px\\) \\* 1000\\)\\)`),
    );

    // Both topologies size the conversation from the same track; the stage flexes.
    expect(split).toMatch(
      /grid-template-columns:\s*minmax\(0, 1fr\) var\(--den-split-divider-px\)\s*var\(--den-split-chat-track\)/,
    );
    const mirrored = extractCssRuleBlock(
      shellCss,
      '.den-shell-stage-host--split[data-stage-leading="false"]',
    );
    expect(mirrored).toMatch(
      /grid-template-columns:\s*var\(--den-split-chat-track\) var\(--den-split-divider-px\)\s*minmax\(0, 1fr\)/,
    );
  });

  it("stops rendering the stage column and divider below the split minimum", () => {
    // Hidden stages skip layout work.
    const shellCss = read("shell-domain.css");
    const query = new RegExp(
      `\\[data-host-band~="max-${SPLIT_MIN_HOST - 1}"\\][\\s\\S]*?display: none;`,
    );
    expect(shellCss).toMatch(query);
  });

  it("sizes the split divider track from the same constant as the column math", () => {
    // The grid host controls the divider-width variable.
    const split = extractCssRuleBlock(
      read("shell-domain.css"),
      ".den-shell-stage-host--split",
    );
    expect(split).toMatch(
      new RegExp(`--den-split-divider-px:\\s*${DIVIDER_PX}px`),
    );
    expect(split).not.toMatch(/var\(--den-split-divider-px,/);
    expect(read("components/shell/SplitDivider.tsx")).not.toContain(
      '"--den-split-divider-px"',
    );
  });

  it("keeps resize hit areas independent of pane widths", () => {
    const shellCss = read("shell-domain.css");
    const nav = extractCssRuleBlock(shellCss, ".den-shell-nav-resize");
    const splitTarget = extractCssRuleBlock(
      shellCss,
      ".den-split-divider::before",
    );
    const files = extractCssRuleBlock(
      read("files-domain.css"),
      ".den-files-split",
    );

    expect(nav).toMatch(/width:\s*4px/);
    expect(splitTarget).toMatch(/inset:\s*0 -2px/);
    expect(files).toMatch(/width:\s*4px/);
    expect(files).toMatch(/margin-inline:\s*-2px/);
  });

  it("leaves the boot splash name sized by the live viewport", () => {
    const html = readDenFile("index.html");
    const name = extractCssRuleBlock(
      html,
      "#den-boot-fallback .den-boot-fallback-name",
    );

    // The name is nowrap: a size written back in px holds the launch geometry
    // and overruns the window once it narrows.
    expect(name).toMatch(/font-size:\s*clamp\(40px,\s*10vw,\s*120px\)/);
    expect(html).not.toMatch(/style\.fontSize\s*=/);
  });

  it("truncates the peer window label rather than running it off the edge", () => {
    const html = readDenFile("index.html");
    const label = extractCssRuleBlock(
      html,
      "body.den-peer-boot #den-boot-fallback .den-boot-fallback-name",
    );

    // A file name has no length bound, and the rule it inherits is nowrap.
    expect(label).toMatch(/overflow:\s*hidden/);
    expect(label).toMatch(/text-overflow:\s*ellipsis/);
    // Truncation is visual only; the reader still gets the whole label.
    expect(html).toMatch(/setAttribute\("aria-label", peerLabel\)/);
  });
});
