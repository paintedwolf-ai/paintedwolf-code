import { readSourceText } from "../stylesheet-source.ts";
import { join } from "node:path";
import { readFileSync } from "node:fs";
import { walkFiles } from "./inventory.ts";


export const REQUIRED_THEME_COLOR_KEYS = [
  "--color-den-bg",
  "--color-den-surface",
  "--color-den-border",
  "--color-den-text",
  "--color-den-text-muted",
  "--color-den-accent",
  "--color-den-accent-text",
  "--color-den-danger",
] as const;

/** Required spacing tokens. */
export const REQUIRED_THEME_SPACING_KEYS = [
  "--spacing-den-1",
  "--spacing-den-2",
  "--spacing-den-3",
  "--spacing-den-4",
  "--spacing-den-5",
  "--spacing-den-6",
  "--spacing-den-7",
  "--spacing-den-8",
  "--spacing-den-9",
] as const;

/** Required type tokens. */
export const REQUIRED_THEME_TEXT_KEYS = [
  "--text-den-micro",
  "--text-den-overline",
  "--text-den-caption",
  "--text-den-compact",
  "--text-den-hint",
  "--text-den-label",
  "--text-den-body",
  "--text-den-input",
  "--text-den-subhead",
  "--text-den-section",
  "--text-den-title",
  "--text-den-display",
  "--text-den-heading",
  "--text-den-hero",
  "--text-den-banner",
] as const;

export function extractThemeBlock(css: string): string {
  return css.match(/@theme\s*\{([\s\S]*?)\n\}/)?.[1] ?? "";
}

export function parseThemeDeclaration(line: string): { key: string; value: string } | null {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith("/*")) return null;
  const match = /^(--[a-zA-Z0-9_-]+)\s*:\s*(.+);?\s*$/.exec(trimmed);
  if (!match) return null;
  return { key: match[1] ?? "", value: (match[2] ?? "").trim().replace(/;$/, "") };
}

/** Map @theme keys with a given prefix to their declared values. */
export function themeKeyValues(css: string, prefix: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const line of extractThemeBlock(css).split("\n")) {
    const decl = parseThemeDeclaration(line);
    if (decl?.key.startsWith(prefix)) {
      map.set(decl.key, decl.value);
    }
  }
  return map;
}

export function findUndefinedThemeRadiusReferences(denSrc: string): string[] {
  const tailwind = readSourceText(join(denSrc, "tailwind.css"), "utf8");
  const defined = new Set(
    [...tailwind.matchAll(/^\s*(--radius-den(?:-[a-z0-9]+)?)\s*:/gm)].map(
      (match) => match[1]!,
    ),
  );
  const hits: string[] = [];
  for (const file of walkFiles(denSrc, (path) => path.endsWith(".css"))) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const [index, line] of css.split("\n").entries()) {
      for (const match of line.matchAll(/var\((--radius-den(?:-[a-z0-9]+)?)/g)) {
        const token = match[1]!;
        if (!defined.has(token)) hits.push(`${rel}:${index + 1}: ${token}`);
      }
    }
  }
  return hits.sort();
}

/** Stylesheets belong to `tailwind.css`, where their cascade order is declared. */
export function findUnexpectedComponentCssImports(denSrc: string): string[] {
  const components = denSrc;
  const hits: string[] = [];
  for (const file of walkFiles(components, (path) => /\.tsx?$/.test(path))) {
    const rel = file.slice(denSrc.length + 1);
    for (const [index, line] of readFileSync(file, "utf8").split("\n").entries()) {
      const trimmed = line.trim();
      if (rel === "index.tsx" || !/^import\s+.*\.css";$/.test(trimmed)) continue;
      hits.push(`${rel}:${index + 1}: ${trimmed}`);
    }
  }
  return hits.sort();
}

/** Finds hardcoded hex colors, including color fallbacks. */
export function findHardcodedHexLines(css: string, label: string): string[] {
  const hits: string[] = [];
  const lines = css.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    // Strip only var() calls with no colour literal inside; one nesting level.
    const withoutSafeVars = line.replace(/var\((?:[^()#]|\([^()#]*\))*\)/g, " ");
    if (/#[0-9a-fA-F]{3,8}/.test(withoutSafeVars)) {
      hits.push(`${label}:${i + 1}: ${line.trim()}`);
    }
  }
  return hits;
}
