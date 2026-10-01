#!/usr/bin/env bun
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const ROOT = join(SCRIPT_DIR, "..");
const DEN = join(ROOT, "lycaon-den");
const NM = join(DEN, "node_modules");

// These values do not identify a license.
const UNKNOWN = new Set([
  "",
  "unknown",
  "unlicensed",
  "see license in",
  "license",
  "proprietary",
  "null",
  "undefined",
]);

type Pkg = {
  name: string;
  version: string;
  license: string;
  licenseText: string | null;
  path: string;
};

function parseArgs(argv: string[]): { out: string | null } {
  let out: string | null = null;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--out" && argv[i + 1]) {
      out = argv[++i]!;
    }
  }
  return { out };
}

function readJSON(path: string): Record<string, unknown> | null {
  try {
    return JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
  } catch {
    return null;
  }
}

function normalizeLicense(raw: unknown): string {
  if (raw == null) return "";
  if (typeof raw === "string") return raw.trim();
  if (typeof raw === "object" && raw !== null && "type" in raw) {
    const t = (raw as { type?: unknown }).type;
    return typeof t === "string" ? t.trim() : "";
  }
  if (Array.isArray(raw)) {
    return raw
      .map((x) => normalizeLicense(x))
      .filter(Boolean)
      .join(" OR ");
  }
  return String(raw).trim();
}

function isUnknown(license: string): boolean {
  const lower = license.toLowerCase();
  if (UNKNOWN.has(lower)) return true;
  if (lower.startsWith("see license")) return true;
  if (lower.includes("unlicensed")) return true;
  return false;
}

function findLicenseFile(pkgDir: string): string | null {
  const names = [
    "LICENSE",
    "LICENSE.txt",
    "LICENSE.md",
    "LICENCE",
    "LICENCE.txt",
    "COPYING",
    "license",
    "license.md",
  ];
  for (const n of names) {
    const p = join(pkgDir, n);
    if (existsSync(p)) return p;
  }
  try {
    for (const ent of readdirSync(pkgDir)) {
      if (/^license/i.test(ent) || /^copying/i.test(ent)) {
        return join(pkgDir, ent);
      }
    }
  } catch {
    // An unreadable directory provides no license candidate.
  }
  return null;
}

function resolvePkgDir(name: string): string | null {
  if (name.startsWith("@")) {
    const [scope, pkg] = name.split("/");
    if (!scope || !pkg) return null;
    const p = join(NM, scope, pkg);
    return existsSync(p) ? p : null;
  }
  const p = join(NM, name);
  return existsSync(p) ? p : null;
}

function collectProductionPkgs(): Pkg[] {
  const rootPkg = readJSON(join(DEN, "package.json"));
  if (!rootPkg) {
    throw new Error(`missing ${join(DEN, "package.json")}`);
  }
  const deps = (rootPkg.dependencies ?? {}) as Record<string, string>;
  const queue = Object.keys(deps);
  const seen = new Set<string>();
  const out: Pkg[] = [];

  while (queue.length > 0) {
    const name = queue.shift()!;
    if (seen.has(name)) continue;
    seen.add(name);

    const dir = resolvePkgDir(name);
    if (!dir) {
      throw new Error(`production dependency ${name} not installed under node_modules`);
    }
    const meta = readJSON(join(dir, "package.json"));
    if (!meta) {
      throw new Error(`missing package.json for ${name} at ${dir}`);
    }
    const version = String(meta.version ?? "");
    const license = normalizeLicense(meta.license);
    if (isUnknown(license)) {
      throw new Error(
        `unclassifiable license for npm package ${name}@${version} (got ${JSON.stringify(meta.license)})`,
      );
    }
    const licPath = findLicenseFile(dir);
    let licenseText: string | null = null;
    if (licPath) {
      try {
        licenseText = readFileSync(licPath, "utf8");
      } catch {
        licenseText = null;
      }
    }
    out.push({
      name,
      version,
      license,
      licenseText,
      path: relative(DEN, dir),
    });

    const childDeps = (meta.dependencies ?? {}) as Record<string, string>;
    for (const child of Object.keys(childDeps)) {
      if (!seen.has(child)) queue.push(child);
    }
  }

  out.sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
  return out;
}

function renderMarkdown(pkgs: Pkg[]): string {
  const lines: string[] = [
    "## Den npm packages",
    "",
    "Production dependency graph for `lycaon-den/` (direct + transitive).",
    "",
  ];
  for (const p of pkgs) {
    lines.push(`### ${p.name}@${p.version}`);
    lines.push("");
    lines.push(`- License: ${p.license}`);
    lines.push("");
    if (p.licenseText && p.licenseText.trim()) {
      lines.push("```");
      lines.push(p.licenseText.trimEnd());
      lines.push("```");
      lines.push("");
    }
  }
  return lines.join("\n");
}

function main(): void {
  const { out } = parseArgs(process.argv.slice(2));
  if (!existsSync(NM)) {
    throw new Error(`missing ${NM} — run bun install in lycaon-den first`);
  }
  const pkgs = collectProductionPkgs();
  if (pkgs.length === 0) {
    throw new Error("no production npm packages found");
  }
  const md = renderMarkdown(pkgs);
  if (out) {
    writeFileSync(out, md, "utf8");
  } else {
    process.stdout.write(md);
  }
  console.error(`licenses-npm-notices: ${pkgs.length} packages`);
}

main();
