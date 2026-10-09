import { loadSourceCorpus } from "../../test/source-corpus.ts";
import { readSourceText } from "../../test/stylesheet-source.ts";
import { execSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { expect } from "vitest";

const invariantsDir = join(dirname(fileURLToPath(import.meta.url)));
export const denSrc = join(invariantsDir, "../..");

export type InvariantClass = "forbidden" | "required";

export type InvariantEntry = {
  id: string;
  class: InvariantClass;
  pattern?: string;
  roots?: string;
  allowGlobs?: string[];
  note?: string;
  structural?: () => void;
};

export const PROJECTS_REGISTRY_ALLOWLIST = ["platform/connection/app-connection.ts"];

/** Combines the shell with its sibling controllers and components. */
export function shellSource(): string {
  const parts = loadSourceCorpus(join(denSrc, "components/shell"), { extensions: [".ts", ".tsx"], excludeTests: true })
    .select((file) => /^(?:shell-[\w-]+\.ts|Shell\w+\.tsx)$/.test(file.rel));
  return [readSourceText(join(denSrc, "components/shell/Shell.tsx")), ...parts.map((file) => file.text)].join("\n");
}

export function relativeToDenSrc(absPath: string): string {
  const norm = absPath.replace(/\\/g, "/");
  const root = denSrc.replace(/\\/g, "/") + "/";
  return norm.startsWith(root) ? norm.slice(root.length) : norm;
}

export function isAllowlisted(line: string, allowGlobs: string[]): boolean {
  const pathPart = line.split(":")[0] ?? line;
  const rel = relativeToDenSrc(pathPart);
  return allowGlobs.some((glob) => rel.endsWith(glob) || rel.includes(glob));
}

export function rgMatches(
  pattern: string,
  roots: string,
  excludeGlobs: string[] = ["*den-client-state-invariants*", "**/shell/invariants/**"],
): string[] {
  const excludes = excludeGlobs.map((g) => `-g '!${g}'`).join(" ");
  const out = execSync(
    `rg -n '${pattern}' "${roots}" ${excludes} 2>/dev/null || true`,
    { encoding: "utf8" },
  ).trim();
  if (!out) return [];
  return out.split("\n").filter((line) => line.length > 0);
}

export function rgForbidden(
  pattern: string,
  roots: string,
  opts?: { allowGlobs?: string[]; excludeGlobs?: string[] },
): string[] {
  const matches = rgMatches(pattern, roots, opts?.excludeGlobs);
  const allow = opts?.allowGlobs ?? [];
  return matches.filter((m) => !isAllowlisted(m, allow));
}

export function rgRequired(
  pattern: string,
  roots: string,
  opts?: { excludeGlobs?: string[] },
): string[] {
  return rgMatches(pattern, roots, opts?.excludeGlobs);
}

export function assertInvariant(entry: InvariantEntry): void {
  if (entry.structural) {
    entry.structural();
    return;
  }
  if (!entry.pattern || !entry.roots) {
    throw new Error(`${entry.id}: missing pattern/roots and no structural hook`);
  }
  if (entry.class === "forbidden") {
    const violations = rgForbidden(entry.pattern, entry.roots, {
      allowGlobs: entry.allowGlobs,
    });
    expect(
      violations,
      `${entry.id}${entry.note ? `: ${entry.note}` : ""}\n${violations.join("\n")}`,
    ).toEqual([]);
    return;
  }
  const hits = rgRequired(entry.pattern, entry.roots);
  expect(
    hits.length,
    `${entry.id}${entry.note ? `: ${entry.note}` : ""} — required pattern ${entry.pattern} missing in ${entry.roots}`,
  ).toBeGreaterThan(0);
}
