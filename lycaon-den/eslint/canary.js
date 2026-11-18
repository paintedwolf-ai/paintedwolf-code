// Proves the lint invariants can fail: every invariant and local rule is run through the
// shipped config on source seeded with its violation.
import { globSync } from "node:fs";
import path from "node:path";
import { ESLint } from "eslint";
import invariants from "./invariants/index.js";
import { cases as computationCases } from "./rules/no-computation-in-jsx-callback.js";
import { cases as statusCases } from "./rules/no-http-error-status-branching.js";
import { cases as draftCases } from "./rules/no-keyed-settings-draft.js";
import { cases as scrollCases } from "./rules/no-native-smooth-scroll.js";
import { cases as leakCases } from "./rules/no-show-accessor-prop-leak.js";

const denRoot = path.resolve(import.meta.dirname, "..");
const failures = [];

// An invariant that governs nothing, or exempts nothing, has outlived its subject.
const names = new Set();
for (const invariant of invariants) {
  if (names.has(invariant.name)) failures.push(`${invariant.name}: declared twice`);
  names.add(invariant.name);
  for (const glob of [...invariant.files, ...(invariant.ignores ?? [])]) {
    if (globSync(glob, { cwd: denRoot }).length === 0) failures.push(`${invariant.name}: ${glob} matches no file`);
  }
}

const cases = [
  ...invariants.map((invariant) => ({
    label: invariant.name,
    rule: "den/invariants",
    file: invariant.seed.file ?? invariant.files[0],
    code: invariant.seed.code,
    marker: `[${invariant.name}]`,
    some: true,
  })),
  ...[
    ["den/no-computation-in-jsx-callback", computationCases],
    ["den/no-http-error-status-branching", statusCases],
    ["den/no-keyed-settings-draft", draftCases],
    ["den/no-native-smooth-scroll", scrollCases],
    ["den/no-show-accessor-prop-leak", leakCases],
  ].flatMap(([rule, ruleCases]) => ruleCases.map((entry) => ({ ...entry, label: rule, rule }))),
  ...[
    { code: `export const Seed = () => <div class="flex flex-col flex-row" />;`, some: true },
    { code: `import { cn } from "../../shared/cn.ts"; export const Seed = () => <div class={cn("flex flex-col flex-row")} />;`, some: true },
    { code: `import { cn } from "../../shared/cn.ts"; export const Seed = () => <div class={cn("flex flex-col gap-2", "rounded-den")} />;`, errors: 0 },
  ].map((entry) => ({
    ...entry,
    label: "tailwindcss/no-contradicting-classname",
    rule: "tailwindcss/no-contradicting-classname",
    file: "src/components/TaskCard.tsx",
  })),
];

const eslint = new ESLint({ cwd: denRoot });
for (const entry of cases) {
  const [result] = await eslint.lintText(entry.code, { filePath: path.join(denRoot, entry.file) });
  const found = result.messages.filter(
    (message) => message.ruleId === entry.rule && (!entry.marker || message.message.includes(entry.marker)),
  ).length;
  // A seed may trip its rule more than once; a rule case states its exact count.
  if (entry.some ? found === 0 : found !== entry.errors) {
    failures.push(`${entry.label}: expected ${entry.some ? "a report" : `${entry.errors} report(s)`} in ${entry.file}, found ${found}\n    ${entry.code.trim()}`);
  }
}

if (failures.length > 0) {
  console.error(`Lint canary failed:\n  ${failures.join("\n  ")}`);
  process.exit(1);
}
console.log(`Lint canary: ${cases.length} seeded cases reported as expected.`);
