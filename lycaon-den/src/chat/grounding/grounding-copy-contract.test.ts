import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  GROUNDING_HEADLINE_GROUNDED,
  GROUNDING_STATUS_GROUNDED,
} from "./grounding-copy.ts";
import { loadSourceCorpus } from "../../test/source-corpus.ts";

const GROUNDING_FORBIDDEN_DISPLAY_PATTERNS = [
  /\bVerified\b/,
  /verified against/i,
  /Citation verification/i,
  /Verification incomplete/i,
  /\bconfirmed correct\b/i,
  /\bproven\b/i,
] as const;

const GROUNDING_DIR = import.meta.dirname;
const COMPONENTS_DIR = join(GROUNDING_DIR, "../../components/citation");
const CHAT_ROOT = join(GROUNDING_DIR, "..");

const GROUNDING_COMPONENT_GLOBS = [
  "CitationGroundingChicklet.tsx",
  "CitationGroundingBadge.tsx",
  "CitationGroundingPanel.tsx",
];

const chatSources = loadSourceCorpus(CHAT_ROOT, {
  extensions: [".ts"],
  excludeTests: true,
});

describe("grounding copy contract", () => {
  it("locks provenance vocabulary in grounding-copy.ts", () => {
    expect(GROUNDING_STATUS_GROUNDED).toBe("Grounded");
    expect(GROUNDING_HEADLINE_GROUNDED).toBe(
      "All citations grounded in captured evidence",
    );
  });

  it("forbids correctness-implying display copy in grounding components", () => {
    for (const file of GROUNDING_COMPONENT_GLOBS) {
      const text = readFileSync(join(COMPONENTS_DIR, file), "utf8");
      for (const pattern of GROUNDING_FORBIDDEN_DISPLAY_PATTERNS) {
        expect(
          pattern.test(text),
          `${file} must not contain forbidden grounding display copy (${pattern})`,
        ).toBe(false);
      }
    }
  });

  it("routes citation-grounding-model headlines through grounding-copy", () => {
    const text = readFileSync(
      join(GROUNDING_DIR, "citation-grounding-model.ts"),
      "utf8",
    );
    expect(text).toContain('from "./grounding-copy.ts"');
    for (const pattern of GROUNDING_FORBIDDEN_DISPLAY_PATTERNS) {
      expect(
        pattern.test(text),
        `citation-grounding-model.ts must not embed forbidden display copy (${pattern})`,
      ).toBe(false);
    }
  });

  it("does not scatter grounding headline strings in other chat modules", () => {
    for (const file of chatSources.files) {
      if (file.path.endsWith("grounding-copy.ts")) continue;
      if (!file.text.includes("CitationGrounding") && !file.text.includes("grounding")) {
        continue;
      }
      for (const pattern of GROUNDING_FORBIDDEN_DISPLAY_PATTERNS) {
        expect(
          pattern.test(file.text),
          `${file.rel} must not contain forbidden grounding display copy (${pattern})`,
        ).toBe(false);
      }
    }
  });
});
