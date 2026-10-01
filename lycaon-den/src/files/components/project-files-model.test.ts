import { describe, expect, it } from "vitest";
import {
  childTreePath,
  fileBufferKey,
  fileDisplayName,
  neighborKeyAfterClose,
  parseFileBufferKey,
} from "./project-files-model.ts";

describe("project-files-model", () => {
  it("keys buffers by root and path", () => {
    expect(fileBufferKey("r1", "src/a.ts")).toBe("path:r1\0src/a.ts");
    expect(fileBufferKey("r2", "src/a.ts")).not.toBe(
      fileBufferKey("r1", "src/a.ts"),
    );
  });

  it("parses path and stable-file keys without conflating their identities", () => {
    expect(parseFileBufferKey(fileBufferKey("r1", "src/a.ts", "job-1")))
      .toEqual({ kind: "path", rootId: "r1", path: "src/a.ts", jobId: "job-1" });
    expect(parseFileBufferKey(fileBufferKey("r1", "src/a.ts", undefined, "file-a")))
      .toEqual({ kind: "file", fileId: "file-a" });
  });

  it("joins tree paths from the root level", () => {
    expect(childTreePath(".", "src")).toBe("src");
    expect(childTreePath("", "src")).toBe("src");
    expect(childTreePath("src", "main.go")).toBe("src/main.go");
  });

  it("derives display names from paths", () => {
    expect(fileDisplayName("src/components/App.tsx")).toBe("App.tsx");
    expect(fileDisplayName("README.md")).toBe("README.md");
  });

  describe("neighborKeyAfterClose", () => {
    const keys = ["a", "b", "c"];

    it("takes the neighbor that slides into the closed slot", () => {
      expect(neighborKeyAfterClose(keys, "a")).toBe("b");
      expect(neighborKeyAfterClose(keys, "b")).toBe("c");
    });

    it("falls back to the new last tab when closing the end", () => {
      expect(neighborKeyAfterClose(keys, "c")).toBe("b");
    });

    it("returns null when the last tab closes", () => {
      expect(neighborKeyAfterClose(["a"], "a")).toBeNull();
    });

    it("returns null for a key that is not in the strip", () => {
      expect(neighborKeyAfterClose(keys, "zz")).toBeNull();
    });
  });
});
