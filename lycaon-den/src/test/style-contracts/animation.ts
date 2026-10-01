import { readFileSync } from "node:fs";
import { readDenStylesheetInventory, stylesheetFamilyFiles } from "./inventory.ts";
import { keyframeDeclaredProps, splitStylesheetRules } from "./stylesheet-parser.ts";

const ANIMATION_CSS_KEYWORDS = new Set([
  "none",
  "inherit",
  "initial",
  "unset",
  "revert",
  "ease",
  "ease-in",
  "ease-out",
  "ease-in-out",
  "linear",
  "step-start",
  "step-end",
  "forwards",
  "backwards",
  "both",
  "infinite",
  "normal",
  "alternate",
  "reverse",
  "paused",
  "running",
]);

export function extractAnimationNames(css: string): string[] {
  const names: string[] = [];
  for (const match of css.matchAll(/\banimation(?:-name)?\s*:\s*([^;}{]+)/g)) {
    const value = (match[1] ?? "").trim();
    if (!value || value === "none") continue;
    const first = /^([a-zA-Z][a-zA-Z0-9_-]*)/.exec(value)?.[1];
    if (!first || ANIMATION_CSS_KEYWORDS.has(first)) continue;
    names.push(first);
  }
  return names;
}

export function extractKeyframeNames(css: string): Set<string> {
  const names = new Set<string>();
  for (const match of css.matchAll(/@keyframes\s+([a-zA-Z0-9_-]+)/g)) {
    names.add(match[1] ?? "");
  }
  return names;
}

/** Properties the compositor animates without per-frame main-thread repaints. */
const COMPOSITED_ANIMATION_PROPS = new Set([
  "opacity",
  "transform",
  "translate",
  "scale",
  "rotate",
]);

/** Finds infinite animations that repaint non-composited properties. */
export function findRepaintingInfiniteAnimations(denSrc: string): string[] {
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const framesByName = new Map<string, { rel: string; props: Set<string> }>();
  const infiniteUseByName = new Map<string, string>();
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const rule of splitStylesheetRules(css)) {
      const keyframes = /^@keyframes\s+([\w-]+)$/.exec(rule.selector);
      if (keyframes) {
        framesByName.set(keyframes[1] ?? "", {
          rel,
          props: keyframeDeclaredProps(rule.body),
        });
        continue;
      }
      if (!/\binfinite\b/.test(rule.body)) continue;
      // Correlate animation names and `infinite` within one component block;
      // shorthand and split animation-name / iteration-count both land here.
      for (const decl of rule.body.matchAll(
        /\banimation(?:-name)?\s*:\s*([^;{}]+)/g,
      )) {
        for (const ident of (decl[1] ?? "").matchAll(/[a-zA-Z][\w-]*/g)) {
          infiniteUseByName.set(ident[0], rel);
        }
      }
    }
  }
  const hits: string[] = [];
  for (const [name, useRel] of infiniteUseByName) {
    const frames = framesByName.get(name);
    if (!frames) continue;
    const offending = [...frames.props]
      .filter((prop) => !COMPOSITED_ANIMATION_PROPS.has(prop))
      .sort();
    if (offending.length === 0) continue;
    hits.push(
      `${useRel}: infinite animation ${name} repaints ${offending.join(", ")} (@keyframes in ${frames.rel})`,
    );
  }
  return hits.sort();
}

/** Finds keyframes outside domain and art stylesheets. */
export function findKeyframesInUtilitiesCss(denSrc: string): string[] {
  const hits: string[] = [];
  const files = stylesheetFamilyFiles(denSrc, /-utilities\.css$/);
  for (const file of files) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const match of css.matchAll(/@keyframes\s+([\w-]+)/g)) {
      hits.push(`${rel}: @keyframes ${match[1] ?? ""}`);
    }
  }
  return hits.sort();
}

/** Finds infinite spin/rotate keyframes missing explicit start boundaries (from or 0%). */
export function findIncompleteInfiniteSpinKeyframes(denSrc: string): string[] {
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const hits: string[] = [];
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const rule of splitStylesheetRules(css)) {
      const keyframes = /^@keyframes\s+([\w-]+spin[\w-]*)$/.exec(rule.selector);
      if (!keyframes) continue;
      const name = keyframes[1] ?? "";
      const hasStart = /\b(from|0%)\b/.test(rule.body);
      const hasEnd = /\b(to|100%)\b/.test(rule.body);
      if (!hasStart || !hasEnd) {
        hits.push(
          `${rel}: @keyframes ${name} missing ${!hasStart ? "start" : "end"} frame`,
        );
      }
    }
  }
  return hits.sort();
}
