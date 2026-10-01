import { execSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import YAML from "yaml";
import { loadSourceCorpus } from "../test/source-corpus.ts";
import { readSourceText } from "../test/stylesheet-source.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");
const componentsDir = join(denSrc, "components");
// Files feature components moved out of components/ and stay in scope.
const filesDir = join(denSrc, "files");
const platformDir = join(denSrc, "platform");
const tauriSrc = join(denSrc, "..", "src-tauri", "src");
const goProjectDir = join(denSrc, "..", "..", "lycaon", "internal", "project");

function rg(
  pattern: string,
  cwd: string,
  globs: { include?: string[]; exclude?: string[] } = {},
): string[] {
  const include = (globs.include ?? ["*.ts", "*.tsx"]).map(
    (g) => `-g '${g}'`,
  );
  const exclude = (globs.exclude ?? ["*node_modules*", "*.test.ts", "*.test.tsx"]).map(
    (g) => `-g '!${g}'`,
  );
  const out = execSync(
    `rg -n '${pattern}' "${cwd}" ${include.join(" ")} ${exclude.join(" ")} 2>/dev/null || true`,
    { encoding: "utf8" },
  ).trim();
  if (!out) return [];
  return out.split("\n").filter((line) => line.length > 0);
}

describe("source navigation invariants", () => {
  it("INV-SRC-01 — no stray shell open/spawn for file paths in components", () => {
    const hits = [
      ...[componentsDir, filesDir].flatMap((dir) => [
        ...rg("@tauri-apps/plugin-shell", dir),
        ...rg("shell\\.open\\b", dir),
        ...rg("Command\\.create", dir),
        ...rg("open -R", dir),
        ...rg("/select,", dir),
        ...rg("xdg-open", dir),
      ]),
    ];
    expect(hits).toEqual([]);
  });

  it("INV-SRC-02 — root resolution stays in audited source and attachment adapters", () => {
    // Root resolution stays in the listed navigation modules.
    const resolverCalls = rg("resolveProjectFile", denSrc, {
      include: ["*.ts", "*.tsx"],
    }).filter((line) => !line.includes("node_modules"));

    const allowed = resolverCalls.filter(
      (line) =>
        line.includes("open-source.ts") ||
        line.includes("reveal-in-file-manager.ts") ||
        line.includes("project-path.ts") ||
        line.includes("SourcePathLink.tsx") ||
        line.includes("search-add-to-chat.ts") ||
        line.includes("add-to-chat.ts") ||
        line.includes("markdown-project-path-link.ts") ||
        line.includes("SearchHitDetail.tsx") ||
        line.includes("TaskCard.tsx") ||
        line.includes("restore-attachments.ts") ||
        line.includes("platform/navigation/open-files-surface.ts") ||
        line.includes("components/scan/fix-findings-with-agent.ts") ||
        line.includes("source-navigation-invariants"),
    );
    expect(allowed.length).toBeGreaterThan(0);

    const bad = resolverCalls.filter(
      (line) =>
        !line.includes("open-source.ts") &&
        !line.includes("reveal-in-file-manager.ts") &&
        !line.includes("project-path.ts") &&
        !line.includes("SourcePathLink.tsx") &&
        !line.includes("search-add-to-chat.ts") &&
        !line.includes("add-to-chat.ts") &&
        !line.includes("markdown-project-path-link.ts") &&
        !line.includes("SearchHitDetail.tsx") &&
        !line.includes("TaskCard.tsx") &&
        !line.includes("restore-attachments.ts") &&
        !line.includes("platform/navigation/open-files-surface.ts") &&
        !line.includes("components/scan/fix-findings-with-agent.ts") &&
        !line.includes("source-navigation-invariants"),
    );
    expect(bad).toEqual([]);
  });

  it("keeps normal source navigation separate from explicit external destinations", () => {
    const openSource = read(join(platformDir, "navigation", "open-source.ts"));
    expect(openSource).not.toContain("openInExternalEditor");
    expect(openSource).not.toContain("externalOpenPrefs");
    expect(read(join(platformDir, "navigation", "open-local-path.ts"))).toContain("openInExternalEditor");
  });

  it("INV-SRC-05 — Go /source handler reuses evidence sandbox normalize", () => {
    const sourceRead = read(join(goProjectDir, "source_read.go"));
    expect(sourceRead).toMatch(/evidence\.ResolveCitationAbs/);
    const sourceWrite = read(join(goProjectDir, "source_write.go"));
    expect(sourceWrite).toMatch(/evidence\.ResolveCitationAbs/);
    const handlers = read(join(goProjectDir, "..", "api", "sourceapi", "project_source_handlers.go"));
    expect(handlers).toMatch(/project\.ErrSourcePathDenied\):\s*\n\s*s\.responses\.Fail\(w, wire\.ApiErrorCodeSourcePathDenied,/);
    const vocabulary = YAML.parse(read(join(denSrc, "..", "..", "docs", "openapi", "vocab", "ApiErrorCode.yaml"))) as { values: { id: string; status: number }[] };
    expect(vocabulary.values.find(value => value.id === "source_path_denied")?.status).toBe(403);
  });

  it("INV-SRC-06 — CitationGroundingPanel uses SourcePathLink for paths", () => {
    const panel = read(join(componentsDir, "citation/CitationGroundingPanel.tsx"));
    expect(panel).toMatch(/SourcePathLink/);
    // Paths render through the shared navigation component.
    expect(panel).not.toMatch(/den-citation-grounding-citation-path/);
  });

  it("structured tool body routes path facts through SourcePathLink", () => {
    // Path facts use the shared navigation component.
    const body = read(join(componentsDir, "tool/StructuredToolBody.tsx"));
    expect(body).toMatch(/import.*SourcePathLink/);
    expect(body).toMatch(/fact\.path/);
    expect(body).toMatch(/line=\{fp\(\)\.line\}/);
    // The contract carries a typed path variant.
    const contract = read(join(denSrc, "chat/tool/tool-presentation-contract.ts"));
    expect(contract).toMatch(/path\?:\s*\{\s*path:\s*string/);
    // Host-data-relative spill paths stay plain text.
    const structured = read(join(denSrc, "chat/tool/tool-part-structured.ts"));
    expect(structured).toMatch(/host-data-relative/);
    // Log digest anchors use the shared navigation component.
    const digest = read(join(componentsDir, "LogDigestOutline.tsx"));
    expect(digest).toMatch(/import.*SourcePathLink/);
    expect(digest).not.toMatch(/openSourceLocation/);
  });

  it("INV-SRC-07 — no stray shell.open for http(s) outside external-link / open_in_browser", () => {
    const hits = [
      ...rg("shell\\.open\\b", denSrc, { include: ["*.ts", "*.tsx"] }),
      ...rg("@tauri-apps/plugin-shell", denSrc, { include: ["*.ts", "*.tsx"] }),
    ].filter(
      (line) =>
        !line.includes("external-link.ts") &&
        !line.includes("external-source.ts") &&
        !line.includes("open-browser.ts") &&
        !line.includes("reveal-in-file-manager.ts") &&
        !line.includes("open-source.ts") &&
        !line.includes("open-browser.test.ts") &&
        !line.includes("external-source.test.ts"),
    );
    expect(hits).toEqual([]);
  });

  it("INV-SRC-08 — confirmAndOpenExternalLink has no skip/bypass branch", () => {
    const ext = read(join(platformDir, "desktop", "external-link.ts"));
    // Confirmation precedes the external open.
    expect(ext).toMatch(/confirm\(message, okLabel\)/);
    expect(ext).toMatch(/if \(!confirmed\) return false;/);
    expect(ext).not.toMatch(/skipConfirm/);
    expect(ext).not.toMatch(/noConfirm/);
  });

  it("INV-SRC-09 — grounding URLs use SourceUrlLink → confirmAndOpenExternalLink", () => {
    const panel = read(join(componentsDir, "citation/CitationGroundingPanel.tsx"));
    expect(panel).toMatch(/SourceUrlLink/);
    // URL links use the external-open entrypoint.
    const urlLink = read(join(componentsDir, "source/SourceUrlLink.tsx"));
    expect(urlLink).toMatch(/confirmAndOpenExternalLink/);
  });

  it("INV-SRC-11 — prose navigation never probes the client filesystem", () => {
    const prose = read(join(denSrc, "chat/markdown/prose-path-opens.ts"));
    const assistant = read(
      join(componentsDir, "transcript/AssistantProseBody.tsx"),
    );
    expect(prose).not.toMatch(/pathKind|path_kind|InventoryProjectSource/);
    expect(assistant).not.toMatch(/pathKind|path_kind|confirmJail/);
    expect(prose).toMatch(/buildProseNavigationIndex/);
  });

  it("INV-SRC-12 — only the base rule colours a path link", () => {
    // Density is a closed variant set; colour is the link's own, so a surface
    // cannot style a path out of reading as a link.
    const offenders: string[] = [];
    const styles = loadSourceCorpus(denSrc, { extensions: [".css"] }).files.filter(
      (file) => !file.rel.includes("/"),
    );
    for (const file of styles) {
      // Root sheets are import manifests; read them in cascade order.
      const css = readSourceText(file.path).replace(/\/\*[\s\S]*?\*\//g, "");
      for (const match of css.matchAll(
        /([^{}]*den-source-path[^{}]*)\{([^{}]*)\}/g,
      )) {
        const selector = match[1] ?? "";
        const body = match[2] ?? "";
        const lines = selector.trim().split("\n");
        const sel = (lines[lines.length - 1] ?? "").trim();
        if (sel === ".den-source-path-link") continue;
        if (/(^|[^-\w])color\s*:/.test(body)) offenders.push(`${file.rel}: ${sel}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("INV-SRC-13 — a path link reads where it opens from its surface", () => {
    const link = read(join(componentsDir, "source/SourcePathLink.tsx"));
    expect(link).toMatch(/useSourceContext/);
    expect(link).toMatch(/source\?\.projectId/);
    expect(link).toMatch(/source\?\.rootRefs/);
    expect(link).toMatch(/source\?\.jobId/);
    // The one reason a path in a source context renders as text.
    expect(link).toMatch(/props\.openable !== false/);
    // `class` is absent from the props, so density stays a closed set.
    expect(link).not.toMatch(/^\s*class\?:/m);
    expect(existsSync(join(componentsDir, "source/annotations/source-context.ts"))).toBe(true);
  });

  it("greenfield — platform branching lives in platform modules or Rust, not chrome", () => {
    // Platform spawn commands stay outside UI components.
    const chrome = [
      ...rg("open -R|/select,|xdg-open|explorer\.exe|cmd /c", componentsDir, {
        include: ["*.ts", "*.tsx"],
      }),
      ...rg("open -R|/select,|xdg-open|explorer\.exe|cmd /c", join(denSrc, "settings"), {
        include: ["*.ts", "*.tsx"],
      }),
      ...rg("open -R|/select,|xdg-open|explorer\.exe|cmd /c", filesDir, {
        include: ["*.ts", "*.tsx"],
      }),
    ];
    expect(chrome).toEqual([]);
  });

  it("keeps source platform modules present", () => {
    expect(existsSync(join(platformDir, "navigation", "open-source.ts"))).toBe(true);
    expect(existsSync(join(platformDir, "desktop", "external-link.ts"))).toBe(true);
    expect(existsSync(join(platformDir, "desktop", "open-browser.ts"))).toBe(true);
    expect(existsSync(join(platformDir, "navigation", "external-source.ts"))).toBe(true);
    expect(existsSync(join(tauriSrc, "open_external.rs"))).toBe(true);
  });
});

function read(path: string): string {
  return readFileSync(path, "utf8");
}
