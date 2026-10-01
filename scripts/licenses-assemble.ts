#!/usr/bin/env bun
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, posix, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const ROOT = join(SCRIPT_DIR, "..");
const LICENSING = join(ROOT, "licensing");

type GoOverride = {
  path_prefix: string;
  license: string;
  note?: string;
  license_text_file?: string;
  // Separate license texts are keyed by their license identifiers.
  license_text_files?: Record<string, string>;
};

type CatalogBinary = {
  name: string;
  version: string;
  license: string;
  source_url?: string;
  source_path?: string;
  license_text_file?: string;
  // Dependency attribution is separate from the binary's license text.
  notice_text_file?: string;
  artifact_notice_file?: string;
  artifact_license_file?: string;
};

type CatalogArtifact = { name: string; artifact: string };
type Catalog = { binaries: CatalogBinary[] };

type CatalogRulePack = {
  name: string;
  version: string;
  license: string;
  source_url: string;
  license_text_file: string;
  notice_text_file?: string;
};

type RuleCatalog = { rule_packs: CatalogRulePack[] };

const COPYLEFT = /^(LGPL|GPL|AGPL|MPL)/i;

function argValue(argv: string[], name: string): string | null {
  const i = argv.indexOf(name);
  if (i >= 0 && argv[i + 1]) return argv[i + 1]!;
  return null;
}

function readText(path: string): string {
  return readFileSync(path, "utf8");
}

function loadOverrides(): GoOverride[] {
  const path = join(LICENSING, "go-license-overrides.yaml");
  const doc = Bun.YAML.parse(readText(path)) as { overrides?: GoOverride[] };
  return doc.overrides ?? [];
}

function loadCatalog(opengrepArtifact: string): Catalog {
  const path = join(LICENSING, "bundled-binaries.yaml");
  const doc = Bun.YAML.parse(readText(path)) as { binaries: Array<CatalogBinary | CatalogArtifact> };
  if (!doc.binaries || !Array.isArray(doc.binaries)) {
    throw new Error(`${path}: missing binaries[]`);
  }
  return { binaries: doc.binaries.map((binary) => "artifact" in binary ? releasedBinary(binary, opengrepArtifact) : binary) };
}

function releasedBinary(binary: CatalogArtifact, artifactDirectory: string): CatalogBinary {
  if (binary.name !== "opengrep" || binary.artifact !== "opengrep" ||
    Object.keys(binary).some((key) => key !== "name" && key !== "artifact")) {
    throw new Error(`unsupported or overridden artifact attribution: ${JSON.stringify(binary)}`);
  }
  const selected = Bun.YAML.parse(readText(join(ROOT, "lycaon/config/runtime/scanners/bundled-manifest.yaml"))) as {
    opengrep: { version: string; license: string };
  };
  const provenance = JSON.parse(readText(join(artifactDirectory, "provenance.json"))) as { version?: string };
  if (provenance.version !== selected.opengrep.version) {
    throw new Error(`Opengrep artifact version ${provenance.version} differs from selected release ${selected.opengrep.version}`);
  }
  return {
    name: binary.name,
    version: selected.opengrep.version,
    license: selected.opengrep.license,
    source_path: `engine-root/bundled/opengrep-${selected.opengrep.version}/opengrep-source.tar.gz`,
    artifact_notice_file: "NOTICES-opengrep.md",
    artifact_license_file: "LICENSE",
  };
}

function loadRuleCatalog(): RuleCatalog {
  const path = join(LICENSING, "bundled-rules.yaml");
  const doc = Bun.YAML.parse(readText(path)) as RuleCatalog;
  if (!doc.rule_packs || !Array.isArray(doc.rule_packs)) {
    throw new Error(`${path}: missing rule_packs[]`);
  }
  return doc;
}

function applyOverride(pkg: string, license: string, overrides: GoOverride[]): string {
  if (license && license.toLowerCase() !== "unknown") return license;
  for (const o of overrides) {
    if (pkg === o.path_prefix || pkg.startsWith(o.path_prefix + "/")) {
      return o.license;
    }
  }
  return license;
}

function findSavedLicense(saveRoot: string, pkg: string): string | null {
  // Saved license directories mirror package import paths.
  const candidates = [
    join(saveRoot, pkg, "LICENSE"),
    join(saveRoot, pkg, "LICENSE.txt"),
    join(saveRoot, pkg, "LICENCE"),
  ];
  for (const c of candidates) {
    if (existsSync(c) && statSync(c).isFile()) return readText(c);
  }
  // Other license filenames are resolved within the same package directory.
  const dir = join(saveRoot, pkg);
  if (!existsSync(dir)) return null;
  try {
    for (const ent of readdirSync(dir)) {
      if (/^license/i.test(ent) && statSync(join(dir, ent)).isFile()) {
        return readText(join(dir, ent));
      }
    }
  } catch {
    // An unreadable directory provides no saved license text.
  }
  return null;
}

function overrideText(license: string, overrides: GoOverride[], pkg: string): string | null {
  for (const o of overrides) {
    if (pkg === o.path_prefix || pkg.startsWith(o.path_prefix + "/")) {
      const rel = o.license_text_files?.[license] ?? o.license_text_file ?? `texts/${license}.txt`;
      const p = join(LICENSING, rel);
      if (existsSync(p)) return readText(p);
      return null;
    }
  }
  return null;
}

function renderGoSection(
  csvPath: string,
  saveRoot: string,
  overrides: GoOverride[],
): string {
  // Exact-row deduplication preserves distinct licenses for a shared module.
  const lines = [...new Set(readText(csvPath).split(/\r?\n/).filter(Boolean))];
  const entries: { pkg: string; url: string; license: string; text: string | null }[] = [];
  const unknown: string[] = [];

  for (const line of lines) {
    // The middle URL field can contain commas.
    const firstComma = line.indexOf(",");
    const lastComma = line.lastIndexOf(",");
    if (firstComma < 0 || lastComma <= firstComma) continue;
    const pkg = line.slice(0, firstComma);
    const url = line.slice(firstComma + 1, lastComma);
    let license = line.slice(lastComma + 1).trim();
    if (pkg.startsWith("github.com/lycaon/lycaon")) continue;
    license = applyOverride(pkg, license, overrides);
    if (!license || license.toLowerCase() === "unknown") {
      unknown.push(pkg);
      continue;
    }
    entries.push({
      pkg,
      url,
      license,
      text: findSavedLicense(saveRoot, pkg) ?? overrideText(license, overrides, pkg),
    });
  }

  if (unknown.length > 0) {
    throw new Error(
      `unclassifiable Go licenses (classify via licensing/go-license-overrides.yaml or remove):\n  - ${unknown.join("\n  - ")}`,
    );
  }
  if (entries.length === 0) {
    throw new Error("Go license CSV produced no third-party entries");
  }

  entries.sort((a, b) => a.pkg.localeCompare(b.pkg));

  const out: string[] = [
    "## Go modules",
    "",
    "Production build graphs for every Go binary staged into the desktop artifact",
    "(`lycaon/cmd/lycaon`, `lycaon/cmd/pw-logs`), unioned.",
    "",
  ];
  for (const e of entries) {
    out.push(`### ${e.pkg}`);
    out.push("");
    out.push(`- License: ${e.license}`);
    if (e.url && e.url.toLowerCase() !== "unknown") {
      out.push(`- URL: ${e.url}`);
    }
    out.push("");
    if (e.text && e.text.trim()) {
      out.push("```");
      out.push(e.text.trimEnd());
      out.push("```");
      out.push("");
    }
  }
  return out.join("\n");
}

function renderCratesFromJson(jsonPath: string, title: string, path: string): string {
  const raw = JSON.parse(readText(jsonPath)) as {
    licenses?: Array<{
      name?: string;
      id?: string;
      text?: string;
      used_by?: Array<{ crate?: { name?: string; version?: string } }>;
    }>;
  };
  const licenses = raw.licenses ?? [];
  if (licenses.length === 0) {
    throw new Error(`crates JSON empty: ${jsonPath}`);
  }
  const out: string[] = [
    `## Rust crates (${title})`,
    "",
    `Release-profile crates for \`${path}\`.`,
    "",
  ];
  for (const lic of licenses) {
    const id = lic.id ?? lic.name ?? "unknown";
    if (!id || id.toLowerCase() === "unknown") {
      throw new Error(`unclassifiable crate license in ${jsonPath}`);
    }
    out.push(`### License: ${id}`);
    out.push("");
    const used = (lic.used_by ?? [])
      .map((u) => {
        const n = u.crate?.name ?? "";
        const v = u.crate?.version ?? "";
        return v ? `${n} ${v}` : n;
      })
      .filter(Boolean)
      .sort();
    if (used.length > 0) {
      out.push("Used by:");
      out.push("");
      for (const u of used) out.push(`- ${u}`);
      out.push("");
    }
    if (lic.text && lic.text.trim()) {
      out.push("```");
      out.push(lic.text.trimEnd());
      out.push("```");
      out.push("");
    }
  }
  return out.join("\n");
}

// Fenced text is excluded from heading demotion.
function demoteHeadings(text: string, levels: number): string {
  const prefix = "#".repeat(levels);
  let fenced = false;
  return text
    .split("\n")
    .map((line) => {
      if (/^\s*(```|~~~)/.test(line)) {
        fenced = !fenced;
        return line;
      }
      return !fenced && /^#{1,6} /.test(line) ? prefix + line : line;
    })
    .join("\n");
}

function artifactNotice(binary: CatalogBinary, artifactDirectory: string): string {
  if (binary.name !== "opengrep" || binary.artifact_notice_file !== "NOTICES-opengrep.md") {
    throw new Error(`unsupported artifact notice for ${binary.name}`);
  }
  const notice = readText(join(artifactDirectory, binary.artifact_notice_file)).trimEnd();
  if (!notice.trim()) throw new Error("Opengrep artifact notice is empty");
  return demoteHeadings(notice, 3);
}

function renderCatalog(catalog: Catalog, opengrepArtifact: string): string {
  const out: string[] = [
    "## Bundled binaries and assets",
    "",
    "Third-party executables and binary assets shipped in the desktop artifact, staged by build scripts or compiled in with go:embed (not visible to Go/npm/crate manifests).",
    "",
  ];
  for (const b of catalog.binaries) {
    if (!b.name || !b.version || !b.license || Boolean(b.source_url) === Boolean(b.source_path)) {
      throw new Error(`bundled-binaries.yaml entry incomplete: ${JSON.stringify(b)}`);
    }
    if (b.source_path && (b.source_path === "." || posix.isAbsolute(b.source_path) || posix.normalize(b.source_path) !== b.source_path ||
      b.source_path.split("/").includes("..") || b.source_path.includes("\\"))) {
      throw new Error(`bundled source path must stay within application resources: ${b.source_path}`);
    }
    if (COPYLEFT.test(b.license)) {
      if (!b.license_text_file && !b.artifact_license_file) {
        throw new Error(
          `copyleft binary ${b.name} requires license_text_file and a source location`,
        );
      }
    }
    out.push(`### ${b.name} ${b.version}`);
    out.push("");
    out.push(`- License: ${b.license}`);
    out.push(b.source_path
      ? `- Corresponding source in application resources: \`${b.source_path}\``
      : `- Source: ${b.source_url}`);
    out.push("");
    for (const rel of [b.license_text_file, b.notice_text_file].filter(Boolean) as string[]) {
      const textPath = join(LICENSING, rel);
      if (!existsSync(textPath)) {
        throw new Error(`missing license text for ${b.name}: ${textPath}`);
      }
      const text = readText(textPath).trimEnd();
      if (rel.endsWith(".md")) {
        // Embedded notices retain their fences and nest beneath this heading.
        out.push(demoteHeadings(text, 3));
      } else {
        out.push("```");
        out.push(text);
        out.push("```");
      }
      out.push("");
    }
    if (b.artifact_license_file) {
      const license = readText(join(opengrepArtifact, b.artifact_license_file)).trimEnd();
      if (!license.trim()) throw new Error("Opengrep artifact license is empty");
      out.push("```", license, "```", "");
    }
    if (b.artifact_notice_file) {
      out.push(artifactNotice(b, opengrepArtifact), "");
    }
  }
  return out.join("\n");
}

function renderRuleCatalog(catalog: RuleCatalog): string {
  const out: string[] = [
    "## Bundled rule packs",
    "",
    "Third-party detection definitions copied into the desktop artifact.",
    "",
  ];
  for (const pack of catalog.rule_packs) {
    if (!pack.name || !pack.version || !pack.license || !pack.source_url || !pack.license_text_file) {
      throw new Error(`bundled-rules.yaml entry incomplete: ${JSON.stringify(pack)}`);
    }
    out.push(`### ${pack.name} ${pack.version}`);
    out.push("");
    out.push(`- License: ${pack.license}`);
    out.push(`- Source: ${pack.source_url}`);
    out.push("");
    for (const rel of [pack.license_text_file, pack.notice_text_file].filter(Boolean) as string[]) {
      const textPath = join(LICENSING, rel);
      if (!existsSync(textPath)) {
        throw new Error(`missing notice text for ${pack.name}: ${textPath}`);
      }
      out.push("```");
      out.push(readText(textPath).trimEnd());
      out.push("```");
      out.push("");
    }
  }
  return out.join("\n");
}

function main(): void {
  const argv = process.argv.slice(2);
  const goCsv = argValue(argv, "--go-csv");
  const goSave = argValue(argv, "--go-save");
  const npmMd = argValue(argv, "--npm-md");
  const cratesJson = argValue(argv, "--crates-json");
  const documentCratesJson = argValue(argv, "--document-crates-json");
  const decideCratesJson = argValue(argv, "--decide-crates-json");
  const outPath = argValue(argv, "--out");
  const opengrepArtifact = argValue(argv, "--opengrep-artifact");
  if (!goCsv || !goSave || !npmMd || !cratesJson || !documentCratesJson || !decideCratesJson || !outPath || !opengrepArtifact) {
    throw new Error(
      "usage: licenses-assemble.ts --go-csv p --go-save d --npm-md p --crates-json p --document-crates-json p --decide-crates-json p --opengrep-artifact d --out p",
    );
  }

  const overrides = loadOverrides();
  const ruleCatalog = loadRuleCatalog();

  const header = [
    "# Third-party software notices",
    "",
    "This file is generated by `./task licenses:notices`. Do not edit by hand.",
    "It aggregates licenses for Go modules, Den npm packages, Tauri, document core, and decision engine Rust crates,",
    "and bundled third-party binaries and rule packs shipped with Painted Wolf Code.",
    "",
    `Generated: ${new Date().toISOString()}`,
    "",
  ].join("\n");

  const goSection = renderGoSection(goCsv, goSave, overrides);
  const catalog = loadCatalog(opengrepArtifact);
  const body = [
    goSection,
    readText(npmMd).trimEnd(),
    demoteHeadings(readText(join(ROOT, "lycaon-den/THIRD_PARTY_NOTICES.md")).trimEnd(), 1),
    renderCratesFromJson(cratesJson, "Tauri", "lycaon-den/src-tauri"),
    renderCratesFromJson(documentCratesJson, "Document core", "lycaon/internal/documentcore/native"),
    renderCratesFromJson(decideCratesJson, "Decision engine", "lycaon/internal/decide/native"),
    renderCatalog(catalog, opengrepArtifact),
    renderRuleCatalog(ruleCatalog),
  ].join("\n\n");

  const doc = `${header}\n${body}\n`;

  for (const b of catalog.binaries) {
    if (!doc.includes(b.name)) {
      throw new Error(`assembled notices missing catalog entry ${b.name}`);
    }
  }

  for (const pack of ruleCatalog.rule_packs) {
    if (!doc.includes(pack.name)) {
      throw new Error(`assembled notices missing rule pack ${pack.name}`);
    }
  }

  // Section and size checks catch incomplete generation.
  if (doc.length < 8_000 || !doc.includes("## Go modules") || !doc.includes("## Den npm")) {
    throw new Error("assembled notices look trivial — generation incomplete");
  }

  mkdirSync(dirname(outPath), { recursive: true });
  writeFileSync(outPath, doc, "utf8");
  console.error(
    `licenses-assemble: wrote ${relative(ROOT, outPath)} (${doc.length} bytes, ${catalog.binaries.length} bundled binaries)`,
  );
}

main();
