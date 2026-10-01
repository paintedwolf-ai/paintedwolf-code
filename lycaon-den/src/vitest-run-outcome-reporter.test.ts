import { readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import type { TestCase } from "vitest/node";
import RunOutcomeReporter from "../vitest-run-outcome-reporter.ts";

const outcomePath = join(tmpdir(), `paintedwolf-vitest-outcome-${process.pid}.json`);

afterEach(() => {
  delete process.env.VITEST_OUTCOME_FILE;
  rmSync(outcomePath, { force: true });
});

describe("RunOutcomeReporter", () => {
  it("records complete assertion values", () => {
    process.env.VITEST_OUTCOME_FILE = outcomePath;
    const reporter = new RunOutcomeReporter();
    const testCase = {
      fullName: "audit > reports violations",
      module: { moduleId: "src/audit.test.ts" },
      result: () => ({
        state: "failed",
        errors: [{ actual: ["src/problem.ts:9"], expected: [] }],
      }),
    } as unknown as TestCase;

    reporter.onTestCaseResult(testCase);
    reporter.onTestRunEnd([], [], "failed");

    const outcome = JSON.parse(readFileSync(outcomePath, "utf8")) as {
      failedTests: Array<{ file: string; name: string; errors: Array<{ actual: string; expected: string }> }>;
    };
    expect(outcome.failedTests).toEqual([
      {
        file: "src/audit.test.ts",
        name: "audit > reports violations",
        errors: [{ actual: "[\n  'src/problem.ts:9'\n]", expected: "[]" }],
      },
    ]);
  });
});
