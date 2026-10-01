import { openingTagAttributes } from "./jsx-classes.ts";
import { readFileSync } from "node:fs";
import { walkFiles } from "./inventory.ts";

/** Finds native tooltip attributes in application source. */
export function findNativeTooltipAttributes(denSrc: string): string[] {
  const hits: string[] = [];
  const scanned = (path: string) =>
    /\.tsx?$/.test(path) &&
    !/\.test\.tsx?$/.test(path) &&
    !path.includes("/test/style-contracts/");
  for (const file of walkFiles(denSrc, scanned)) {
    const rel = file.slice(denSrc.length + 1);
    const src = readFileSync(file, "utf8");
    const lineAt = (index: number) => src.slice(0, index).split("\n").length;

    // Intrinsic title attributes create native tooltips.
    for (const match of src.matchAll(/<([a-z][a-zA-Z0-9-]*)(?=[\s>/])/g)) {
      const attrs = openingTagAttributes(src, match.index + match[0].length);
      if (/(^|\s)title\s*=/.test(attrs)) {
        hits.push(`${rel}:${lineAt(match.index)}: <${match[1]} title=…>`);
      }
    }
    for (const match of src.matchAll(/setAttribute\(\s*["']title["']/g)) {
      hits.push(`${rel}:${lineAt(match.index)}: setAttribute("title", …)`);
    }
    for (const match of src.matchAll(/(?<!\bthis)\.title\s*=(?!=)/g)) {
      hits.push(`${rel}:${lineAt(match.index)}: .title = …`);
    }
    // An attribute fragment interpolated into markup carries no tag to scan.
    for (const match of src.matchAll(/`[^`\n]*\stitle="/g)) {
      const lineStart = src.lastIndexOf("\n", match.index) + 1;
      const before = src.slice(lineStart, match.index);
      if (before.includes("//") || before.trimStart().startsWith("*")) continue;
      hits.push(`${rel}:${lineAt(match.index)}: title=" in an HTML template`);
    }
  }
  return hits.sort();
}

/** Scrollbar construction boundary. */
const SCROLLPORT_LAYER = "platform/scrolling/themed-scrollbars.ts";

/** Counts a call's top-level arguments. */
function readCallArity(src: string, open: number): number | null {
  let depth = 0;
  let quote = "";
  let arity = 0;
  for (let i = open; i < src.length; i++) {
    const c = src[i] as string;
    if (quote) {
      if (c === quote && src[i - 1] !== "\\") quote = "";
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      continue;
    }
    if (c === "(" || c === "[" || c === "{") {
      depth++;
      if (depth === 1) arity = 1;
      continue;
    }
    if (c === ")" || c === "]" || c === "}") {
      depth--;
      if (depth === 0) return arity;
      continue;
    }
    if (c === "," && depth === 1) arity++;
  }
  return null;
}

/** Finds scrollbar constructions outside the scroll layer. */
export function findScrollbarConstructionsOutsideLayer(denSrc: string): string[] {
  const hits: string[] = [];
  const scanned = (path: string) =>
    /\.tsx?$/.test(path) &&
    !/\.test\.tsx?$/.test(path) &&
    !path.endsWith(SCROLLPORT_LAYER) &&
    !path.includes("/test/style-contracts/");
  for (const file of walkFiles(denSrc, scanned)) {
    const rel = file.slice(denSrc.length + 1);
    const src = readFileSync(file, "utf8");
    for (const match of src.matchAll(/\bOverlayScrollbars\s*\(/g)) {
      const arity = readCallArity(src, match.index + match[0].length - 1);
      if (arity === null || arity < 2) continue;
      const line = src.slice(0, match.index).split("\n").length;
      hits.push(`${rel}:${line}: OverlayScrollbars(…, …)`);
    }
  }
  return hits.sort();
}

/** Finds DOM searches for a separate viewport element. */
export function findGeneratedViewportLookups(denSrc: string): string[] {
  const hits: string[] = [];
  const scanned = (path: string) =>
    (/\.tsx?$/.test(path) || /\.css$/.test(path)) &&
    !/\.test\.tsx?$/.test(path) &&
    !path.endsWith(SCROLLPORT_LAYER) &&
    !path.endsWith("themed-scrollbars.css") &&
    !path.includes("/test/style-contracts/");
  for (const file of walkFiles(denSrc, scanned)) {
    const rel = file.slice(denSrc.length + 1);
    const src = readFileSync(file, "utf8");
    for (const match of src.matchAll(/\[data-overlayscrollbars-viewport[^\]]*\]/g)) {
      const line = src.slice(0, match.index).split("\n").length;
      hits.push(`${rel}:${line}: ${match[0]}`);
    }
  }
  return hits.sort();
}
