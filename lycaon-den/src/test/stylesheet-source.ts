import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parse, type AtRule } from "postcss";

type LocalImport = { target: string; modifiers: string; rule: AtRule };

/** Resolve plain local CSS imports in cascade order, preserving conditional imports. */
export function readSourceText(path: string | URL, _encoding: "utf8" = "utf8"): string {
  const absolute = path instanceof URL ? fileURLToPath(path) : resolve(path);
  return expand(absolute, new Set());
}

function expand(path: string, ancestors: Set<string>): string {
  if (ancestors.has(path)) throw new Error(`Cyclic stylesheet import: ${path}`);
  const source = readFileSync(path, "utf8");
  if (!path.endsWith(".css")) return source;
  const next = new Set(ancestors).add(path);
  const replacements = localImports(source, path)
    .filter((entry) => entry.modifiers === "")
    .map(({ target, rule }) => {
      const start = rule.source?.start?.offset;
      const final = rule.source?.end?.offset;
      if (start === undefined || final === undefined) {
        throw new Error(`Stylesheet import lacks a source location: ${path}`);
      }
      let end = final;
      if (source.slice(end, end + 2) === "\r\n") end += 2;
      else if (source[end] === "\n") end++;
      return { start, end, content: expand(resolve(dirname(path), target), next) };
    });
  let result = source;
  for (const replacement of replacements.reverse()) {
    result = result.slice(0, replacement.start) + replacement.content + result.slice(replacement.end);
  }
  return result;
}

/** Discover physical local dependencies once, including imports with layer/media modifiers. */
export function stylesheetDependencies(path: string, ancestors = new Set<string>()): Set<string> {
  const absolute = resolve(path);
  if (ancestors.has(absolute)) throw new Error(`Cyclic stylesheet import: ${absolute}`);
  const next = new Set(ancestors).add(absolute);
  const result = new Set([absolute]);
  for (const { target } of localImports(readFileSync(absolute, "utf8"), absolute)) {
    for (const dependency of stylesheetDependencies(resolve(dirname(absolute), target), next)) {
      result.add(dependency);
    }
  }
  return result;
}

function localImports(source: string, path: string): LocalImport[] {
  const imports: LocalImport[] = [];
  for (const rule of parse(source, { from: path }).nodes) {
    if (rule.type !== "atrule" || rule.name.toLowerCase() !== "import") continue;
    const value = importLocation(rule.params);
    if (value?.target.startsWith(".")) imports.push({ ...value, rule });
  }
  return imports;
}

function importLocation(params: string): { target: string; modifiers: string } | null {
  const text = params.trim();
  const url = /^url\(\s*/i.exec(text);
  let offset = url?.[0].length ?? 0;
  const quote = text[offset];
  let end = offset;
  let target: string;
  if (quote === '"' || quote === "'") {
    offset++;
    end = offset;
    while (end < text.length && text[end] !== quote) {
      if (text[end] === "\\") end++;
      end++;
    }
    if (text[end] !== quote) throw new Error(`Unterminated stylesheet import: ${params}`);
    target = text.slice(offset, end);
    end++;
  } else if (url) {
    end = text.indexOf(")", offset);
    if (end < 0) throw new Error(`Unterminated stylesheet URL: ${params}`);
    target = text.slice(offset, end).trim();
  } else {
    return null;
  }
  if (url) {
    while (/\s/.test(text[end] ?? "") && end < text.length) end++;
    if (text[end] !== ")") throw new Error(`Unterminated stylesheet URL: ${params}`);
    end++;
  }
  target = target.replace(/\\(?:([\da-f]{1,6})\s?|([^\r\n]))/gi, (_escape, hex: string | undefined, literal: string | undefined) => {
    if (hex !== undefined) {
      const codepoint = Number.parseInt(hex, 16);
      return codepoint === 0 || codepoint > 0x10ffff ? "\ufffd" : String.fromCodePoint(codepoint);
    }
    return literal ?? "";
  });
  return { target, modifiers: text.slice(end).trim() };
}
