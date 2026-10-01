import { describe, expect, it } from "vitest";
import { denSourceRoot, loadSourceCorpus } from "../source-corpus.ts";

const tests = loadSourceCorpus(denSourceRoot, { extensions: [".ts"] }).files.filter(
  (file) => file.path.endsWith(".test.ts"),
);

/** Minimum indent (spaces) for throw new Error allowed as mock/simulation plumbing. */
const NESTED_THROW_MIN_INDENT = 6;

const ALLOWED_THROW_MESSAGES = [
  "gone",
  "timeout",
  "invariant",
  "unclosed",
  "not found",
  "EventTopic union",
  "dispatchTopic switch",
];

function isAuditTest(path: string): boolean {
  return path.includes("-audit.test.ts") || path.includes("-greenfield-audit.test.ts");
}

function lineIndent(line: string): number {
  const m = line.match(/^(\s*)/);
  return m ? m[1]!.length : 0;
}

describe("fail-messages audit (Vitest)", () => {
  it("forbids bare throw new Error for assertion failures in test files", () => {
    const violations: string[] = [];

    for (const file of tests) {
      if (file.path.endsWith("fail-messages-audit.test.ts")) continue;
      if (isAuditTest(file.path)) continue;

      const rel = file.rel;
      const lines = file.text.split("\n");
      for (let i = 0; i < lines.length; i++) {
        const line = lines[i]!;
        if (!line.includes("throw new Error(")) continue;

        const indent = lineIndent(line);
        if (indent >= NESTED_THROW_MIN_INDENT) continue;

        const allowed = ALLOWED_THROW_MESSAGES.some((msg) => line.includes(msg));
        if (allowed) continue;

        violations.push(`${rel}:${i + 1}: ${line.trim()}`);
      }
    }

    expect(violations).toEqual([]);
  });
});
