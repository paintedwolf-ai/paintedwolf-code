import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { CitationGrounding } from "../../api/types.ts";
import { citationGroundingHadTraceableWork } from "./citation-grounding-model.ts";

type CitationGroundingParityGolden = {
  had_traceable_work: boolean;
  grounding: CitationGrounding;
};

const fixtureDir = join(
  dirname(fileURLToPath(import.meta.url)),
  "../../../../lycaon/test/fixtures/citation-grounding-parity",
);

function loadGolden(name: string): CitationGroundingParityGolden {
  const body = readFileSync(join(fixtureDir, name), "utf8");
  return JSON.parse(body) as CitationGroundingParityGolden;
}

describe("citation-grounding-parity", () => {
  for (const name of ["none.json", "grounded.json", "prose-advisory-only.json"]) {
    it(`matches Go vacuous flag for ${name}`, () => {
      const golden = loadGolden(name);
      expect(citationGroundingHadTraceableWork(golden.grounding)).toBe(
        golden.had_traceable_work,
      );
    });
  }
});
