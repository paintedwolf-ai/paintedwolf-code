/**
 * Nothing under inline-edit/ or verbs/ calls the source write API or
 * mutates a buffer with model output.
 */

import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { loadTypeScriptCorpus } from "../../../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../../../test/vitest-timeouts.ts";

const ROOTS = [
  join(import.meta.dirname, "."),
  join(import.meta.dirname, "..", "verbs"),
];

const FORBIDDEN = [
  /replaceProjectSource\s*\(/,
  /\.replaceProjectSource\b/,
  /content:\s*model/i,
  /dispatch\(\s*\{\s*changes:.*model/s,
];

describe("no client write from inline-edit / verbs", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("does not call replaceProjectSource or apply model output to a buffer", () => {
    const files = ROOTS.flatMap((root) =>
      loadTypeScriptCorpus(root, { excludeTests: true }).files,
    );
    expect(files.length).toBeGreaterThan(0);
    const offenders: string[] = [];
    for (const file of files) {
      // Hunk rejection uses the mediated user-write path.
      if (file.path.endsWith("HunkReviewPanel.tsx")) continue;
      for (const re of FORBIDDEN) {
        if (re.test(file.text)) offenders.push(`${file.path} ~ ${re}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
