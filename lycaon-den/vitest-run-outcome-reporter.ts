import { writeFileSync } from "node:fs";
import { inspect } from "node:util";
import type { Reporter, TestCase } from "vitest/node";

type FailureDetail = {
  file: string;
  name: string;
  errors: Array<{ actual?: string; expected?: string }>;
};

function renderValue(value: unknown): string {
  return inspect(value, {
    breakLength: 100,
    compact: false,
    depth: null,
    maxArrayLength: null,
    maxStringLength: null,
  });
}

/** Records runner outcomes missing from the JSON report. */
export default class RunOutcomeReporter implements Reporter {
  private readonly failedTests: FailureDetail[] = [];

  onTestCaseResult(testCase: TestCase): void {
    const result = testCase.result();
    if (result.state !== "failed") return;
    const errors = result.errors.map((error) => {
      const values = error as unknown as Record<string, unknown>;
      return {
        ...(Object.prototype.hasOwnProperty.call(values, "actual") ? { actual: renderValue(values.actual) } : {}),
        ...(Object.prototype.hasOwnProperty.call(values, "expected") ? { expected: renderValue(values.expected) } : {}),
      };
    });
    this.failedTests.push({ file: testCase.module.moduleId, name: testCase.fullName, errors });
  }

  onTestRunEnd: NonNullable<Reporter["onTestRunEnd"]> = (
    _testModules,
    unhandledErrors,
    reason,
  ) => {
    const path = process.env.VITEST_OUTCOME_FILE?.trim();
    if (!path) return;
    const outcome = {
      reason,
      failedTests: this.failedTests,
      unhandledErrors: unhandledErrors.map((err) => ({
        name: typeof err.name === "string" ? err.name : undefined,
        message: err.message,
        stack: typeof err.stack === "string" ? err.stack : undefined,
      })),
    };
    writeFileSync(path, JSON.stringify(outcome, null, 2), "utf8");
  };
}
