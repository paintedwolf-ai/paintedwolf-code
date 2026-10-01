import { afterEach, expect, it } from "vitest";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findBareLayoutDomainRules } from "../test/style-contracts/layout.ts";
import { findDuplicateUtilityDefinitions, findDynamicModifierUtilityViolations, findMissingDynamicModifierDomainRules } from "../test/style-contracts/utilities.ts";
import { findKeyframesInUtilitiesCss } from "../test/style-contracts/animation.ts";

const roots: string[] = [];
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });
function fixture(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "stylesheet-fragments-"));
  roots.push(root);
  for (const [path, source] of Object.entries(files)) writeFileSync(join(root, path), source);
  return root;
}

it("keeps utility checks on physical definitions across import manifests", () => {
  const root = fixture({
    "tailwind.css": '@import "./recipes.css";\n',
    "recipes.css": "@utility den-row--active { color: red; }\n",
    "Row.tsx": 'const row = <div class={`den-row--${state}`} />;\n',
  });
  expect(findDynamicModifierUtilityViolations(root)).toEqual(["recipes.css: @utility den-row--active"]);
  expect(findDuplicateUtilityDefinitions(root)).toEqual([]);
});

it("carries domain rules to plain-named fragments without double counting", () => {
  const root = fixture({
    "tailwind.css": '@import "./rows-domain.css";\n',
    "rows-domain.css": '@import "./rows.css";\n',
    "rows.css": ".den-row--active { color: var(--den-accent); }\n.den-row-layout { display: flex; }\n",
    "Row.tsx": 'const row = <div class={`den-row--${state}`} />;\n',
  });
  expect(findMissingDynamicModifierDomainRules(root)).toEqual([]);
  expect(findBareLayoutDomainRules(root)).toEqual(["rows.css: .den-row-layout"]);
});

it("retains the utility keyframe restriction through imported fragments", () => {
  const root = fixture({
    "tailwind.css": '@import "./rows-utilities.css";\n',
    "rows-utilities.css": '@import "./rows.css";\n',
    "rows.css": "@keyframes flash { to { opacity: 0; } }\n",
  });
  expect(findKeyframesInUtilitiesCss(root)).toEqual(["rows.css: @keyframes flash"]);
});
