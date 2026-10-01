// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { assessFilesTreeDrop } from "./files-tree-drag.ts";

describe("Files tree move targets", () => {
  it("names a valid destination", () => {
    expect(
      assessFilesTreeDrop(
        { rootId: "r1", path: "src/a.ts" },
        { rootId: "r1", dir: "archive" },
        "archive",
      ),
    ).toEqual({ valid: true, destination: "archive" });
  });

  it("explains same-parent, descendant, and cross-root targets", () => {
    expect(
      assessFilesTreeDrop(
        { rootId: "r1", path: "src/a.ts" },
        { rootId: "r1", dir: "src" },
        "src",
      )?.reason,
    ).toBe("Already in this folder.");
    expect(
      assessFilesTreeDrop(
        { rootId: "r1", path: "src" },
        { rootId: "r1", dir: "src/nested" },
        "src/nested",
      )?.reason,
    ).toBe("A folder cannot move into itself.");
    expect(
      assessFilesTreeDrop(
        { rootId: "r1", path: "src/a.ts" },
        { rootId: "r2", dir: "." },
        "@other",
      )?.reason,
    ).toBe("Choose a folder in the same root.");
  });
});
