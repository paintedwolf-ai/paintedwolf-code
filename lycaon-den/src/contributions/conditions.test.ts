import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { evaluateCondition } from "./conditions.ts";
import type { ContributionCondition } from "../api/types.ts";

type ConformanceCase = {
  name: string;
  condition: ContributionCondition | null;
  facts: Record<string, boolean>;
  expect: boolean;
  expect_host: string;
};

const fixturePath = join(
  __dirname,
  "../../../lycaon/internal/contribution/testdata/condition-conformance.json",
);

describe("condition conformance (shared fixtures with the Go evaluator)", () => {
  const file = JSON.parse(readFileSync(fixturePath, "utf8")) as {
    cases: ConformanceCase[];
  };
  expect(file.cases.length).toBeGreaterThan(0);

  for (const tc of file.cases) {
    it(tc.name, () => {
      const lookup = (fact: string, operand: string): boolean => {
        const key = operand ? `${fact}=${operand}` : fact;
        return tc.facts[key] ?? false;
      };
      expect(evaluateCondition(tc.condition, lookup)).toBe(tc.expect);
    });
  }
});

describe("evaluateCondition guardrails", () => {
  it("treats an unknown fact as false, never a passthrough", () => {
    expect(evaluateCondition({ fact: "made_up_fact" }, () => true)).toBe(false);
  });
  it("treats a missing condition as true", () => {
    expect(evaluateCondition(null, () => false)).toBe(true);
    expect(evaluateCondition(undefined, () => false)).toBe(true);
  });
});
