import { describe, expect, it } from "vitest";
import {
  countWholeWordOccurrences,
  projectCountLabel,
  renameCollisionWarning,
  validRenameInput,
} from "./rename-card-model.ts";

describe("countWholeWordOccurrences", () => {
  it("counts whole words only, case-sensitively", () => {
    const text = "user User userName my_user user";
    expect(countWholeWordOccurrences(text, "user")).toBe(2);
    expect(countWholeWordOccurrences(text, "User")).toBe(1);
    expect(countWholeWordOccurrences(text, "userName")).toBe(1);
  });

  it("treats underscores and $ as word characters", () => {
    expect(countWholeWordOccurrences("a_user $user (user)", "user")).toBe(1);
  });

  it("counts adjacent punctuation-bounded matches", () => {
    expect(countWholeWordOccurrences("x(x).x,x", "x")).toBe(4);
  });

  it("returns zero for empty needle", () => {
    expect(countWholeWordOccurrences("anything", "")).toBe(0);
  });
});

describe("validRenameInput", () => {
  it("accepts identifier-like names and rejects whitespace", () => {
    expect(validRenameInput("newName")).toBe(true);
    expect(validRenameInput("kebab-case")).toBe(true);
    expect(validRenameInput("  padded  ")).toBe(true);
    expect(validRenameInput("two words")).toBe(false);
    expect(validRenameInput("")).toBe(false);
    expect(validRenameInput("   ")).toBe(false);
  });
});

describe("renameCollisionWarning", () => {
  const doc = "const alpha = 1;\nconst beta = alpha + 2;\n";

  it("warns when the new name already occurs", () => {
    expect(renameCollisionWarning(doc, "alpha", "beta")).toMatch(
      /already appears in this file/,
    );
  });

  it("stays quiet for fresh names, same name, and empty input", () => {
    expect(renameCollisionWarning(doc, "alpha", "gamma")).toBeNull();
    expect(renameCollisionWarning(doc, "alpha", "alpha")).toBeNull();
    expect(renameCollisionWarning(doc, "alpha", "")).toBeNull();
  });
});

describe("projectCountLabel", () => {
  it("formats plain and truncated counts", () => {
    expect(
      projectCountLabel({ matches: 12, files: 4, truncated: false }),
    ).toBe("12 across 4 files");
    expect(projectCountLabel({ matches: 1, files: 1, truncated: false })).toBe(
      "1 across 1 file",
    );
    expect(
      projectCountLabel({ matches: 5000, files: 500, truncated: true }),
    ).toBe("5000+ across 500+ files");
  });
});
