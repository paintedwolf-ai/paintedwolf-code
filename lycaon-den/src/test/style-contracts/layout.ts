import { readFileSync } from "node:fs";
import { parseDeclarations, splitStylesheetRules, splitTopLevel, subjectCompound } from "./stylesheet-parser.ts";
import { readDenStylesheetInventory, stylesheetFamilyFiles } from "./inventory.ts";


/** Pseudo-elements evaluated eagerly when matched by a universal selector. */
const EAGER_PSEUDO_ELEMENTS = new Set(["before", "after"]);

/**
 * Identifies universal pseudo-element rules (`*::before`, `::after`).
 */
export function findUniversalPseudoElementRules(css: string, label: string): string[] {
  const hits: string[] = [];
  const source = css.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, " "));
  let preludeStart = 0;
  let depth = 0;
  for (let i = 0; i < source.length; i++) {
    const char = source[i];
    if (char === "(" || char === "[") depth++;
    else if (char === ")" || char === "]") depth--;
    if (depth > 0) continue;
    if (char === "}" || char === ";") {
      preludeStart = i + 1;
      continue;
    }
    if (char !== "{") continue;
    const prelude = source.slice(preludeStart, i).trim();
    preludeStart = i + 1;
    if (!prelude || prelude.startsWith("@")) continue;
    const line = source.slice(0, i).split("\n").length;
    for (const selector of splitTopLevel(prelude, /,/)) {
      const subject = subjectCompound(selector);
      const match = /^\*?::?([a-z-]+)$/.exec(subject);
      if (match && EAGER_PSEUDO_ELEMENTS.has(match[1] ?? "")) {
        hits.push(`${label}:${line}: ${selector.replace(/\s+/g, " ")}`);
      }
    }
  }
  return hits;
}

// Retain selectors that utilities cannot reach or express.
const RETENTION_SELECTOR_RE =
  /::(?:before|after)|\[(?:data-|open\])|:has\(|:nth-(?:last-)?child|\.cm-|den-file-edit-diff|transcript-viewport|layout-busy|nav-resizing|split-resizing|den-split-col|tabs__|den-shell-header-chat|den-shell-chrome|den-shell--nav|den-shell-nav-fold|den-shell-aside-main|den-shell-stage|progress-strip|bubble--|den-browse-|den-list-|den-detail-|den-stage-|den-search-|den-find-|den-scans-|project-scans-|den-revoke-|den-artifacts-|project-artifacts-|den-blueprints-|project-blueprints-|project-extensions-|project-cost-/i;

const RETENTION_DECL_PROPS = new Set([
  "animation",
  "animation-name",
  "animation-duration",
  "animation-delay",
  "animation-timing-function",
  "content",
  "content-visibility",
  "contain",
  "contain-intrinsic-size",
  "pointer-events",
  "-webkit-app-region",
  "app-region",
  "clip-path",
  "filter",
  "opacity",
  "transition",
  "transform",
  "transform-origin",
  "z-index",
  "position",
  "top",
  "left",
  "right",
  "bottom",
  "inset",
  "overscroll-behavior",
  "overflow-anchor",
  "scroll-behavior",
  "isolation",
  "will-change",
  "box-shadow",
  "backdrop-filter",
]);

const LAYOUT_DECL_PROPS = new Set([
  "display",
  "flex",
  "flex-direction",
  "flex-shrink",
  "flex-grow",
  "flex-wrap",
  "align-items",
  "align-self",
  "align-content",
  "justify-content",
  "justify-items",
  "gap",
  "row-gap",
  "column-gap",
  "padding",
  "padding-top",
  "padding-right",
  "padding-bottom",
  "padding-left",
  "margin",
  "margin-top",
  "margin-right",
  "margin-bottom",
  "margin-left",
  "width",
  "height",
  "min-width",
  "min-height",
  "max-width",
  "max-height",
  "font",
  "font-size",
  "font-weight",
  "font-family",
  "line-height",
  "letter-spacing",
  "text-transform",
  "color",
  "background",
  "background-color",
  "border",
  "border-top",
  "border-right",
  "border-bottom",
  "border-left",
  "border-radius",
  "border-color",
  "cursor",
  "text-align",
  "white-space",
  "overflow",
  "overflow-x",
  "overflow-y",
  "list-style",
  "box-sizing",
  "vertical-align",
  "grid-template-columns",
  "grid-template-rows",
  "user-select",
  "text-overflow",
  "word-break",
  "overflow-wrap",
]);

function isRetentionDeclaration(prop: string, value: string): boolean {
  if (prop.startsWith("--")) return true;
  if (RETENTION_DECL_PROPS.has(prop)) return true;
  if (prop === "overflow" && value === "clip") return true;
  if (/gradient|color-mix|var\s*\(/i.test(value)) return true;
  return false;
}

function isBareLayoutRule(selector: string, body: string): boolean {
  const sel = selector.trim();
  if (!sel || sel.startsWith("@keyframes")) return false;
  if (sel === ":root" || sel.startsWith(":root ")) return false;
  if (RETENTION_SELECTOR_RE.test(sel)) return false;
  if (sel.startsWith("@media")) {
    for (const inner of splitStylesheetRules(body)) {
      if (isBareLayoutRule(inner.selector, inner.body)) return true;
    }
    return false;
  }
  const decls = parseDeclarations(body);
  if (decls.length === 0) return false;
  let hasLayout = false;
  for (const { prop, value } of decls) {
    if (isRetentionDeclaration(prop, value)) return false;
    if (LAYOUT_DECL_PROPS.has(prop)) hasLayout = true;
  }
  return hasLayout;
}

/** Finds layout-only rules at the domain retention floor. */
export function findBareLayoutDomainRules(denSrc: string): string[] {
  const hits: string[] = [];
  const files = stylesheetFamilyFiles(denSrc, /-domain\.css$/);
  for (const file of files) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const rule of splitStylesheetRules(css)) {
      if (!isBareLayoutRule(rule.selector, rule.body)) continue;
      const preview = rule.selector.replace(/\s+/g, " ").slice(0, 80);
      hits.push(`${rel}: ${preview}`);
    }
  }
  return hits.sort();
}

/** Matches stage roots governed by the shared backdrop. */
const STAGE_ROOT_SELECTOR =
  /^\.(?:project-[a-z0-9-]+-view|den-[a-z0-9-]+-stage|den-search-takeover|den-settings-view)$/;

export function findStageRootBackdropFills(denSrc: string): string[] {
  const hits: string[] = [];
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    for (const rule of splitStylesheetRules(readFileSync(file, "utf8"))) {
      const selector = rule.selector.trim();
      if (!STAGE_ROOT_SELECTOR.test(selector)) continue;
      if (/background:\s*var\(--den-background\)/.test(rule.body)) {
        hits.push(`${rel}: ${selector}`);
      }
    }
  }
  return hits.sort();
}
