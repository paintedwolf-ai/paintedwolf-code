import { describe, expect, it } from "vitest";
import {
  denRoot,
  denSourceRoot,
  loadSourceCorpus,
  loadTypeScriptCorpus,
} from "./source-corpus.ts";

const source = loadTypeScriptCorpus(denSourceRoot);
const e2e = loadSourceCorpus(`${denRoot}/e2e`, { extensions: [".ts"] });
const rust = loadSourceCorpus(`${denRoot}/src-tauri/src`, { extensions: [".rs"] });

function violations(
  files: readonly { rel: string; text: string }[],
  pattern: RegExp,
): string[] {
  return files.filter((file) => pattern.test(file.text)).map((file) => file.rel);
}

describe("test infrastructure consolidation", () => {
  it("routes partial API clients through the typed stub", () => {
    expect(
      violations(source.files, /as\s+unknown\s+as\s+[^;\n]*LycaonClient/),
    ).toEqual([]);
  });

  it("keeps shared Playwright synchronization out of individual specs", () => {
    const specs = e2e.files.filter((file) => file.rel.endsWith(".spec.ts"));
    expect(
      violations(
        specs,
        /(?:async\s+)?function\s+(?:waitHarnessConnected|waitActiveWorkflowRun|openSessionRow|openProjectFilesFixture)\b/,
      ),
    ).toEqual([]);
  });

  it("routes architectural source scans through the source corpus", () => {
    const architectureTests = source.files.filter((file) =>
      file.rel !== "test/test-infrastructure-contract.test.ts" &&
      /(?:contract|invariants?|audit|guard)\.test\.tsx?$/.test(file.rel),
    );
    expect(violations(architectureTests, /\b(?:readdirSync|statSync)\b/)).toEqual([]);
  });

  it("uses the shared Rust temporary directory fixture", () => {
    expect(violations(rust.files, /\bfn\s+temp_dir\s*\(/)).toEqual([]);
  });
});
