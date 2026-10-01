import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import {
  newEntryPath,
  renameEntryPath,
  renameSelectionRange,
  sourceCreateErrorMessage,
} from "./project-files-create.ts";

describe("newEntryPath", () => {
  it("joins a plain name onto the folder", () => {
    expect(newEntryPath("src", "main.ts", "file")).toEqual({ path: "src/main.ts" });
    expect(newEntryPath(".", "README.md", "file")).toEqual({ path: "README.md" });
    expect(newEntryPath(".", "docs", "folder")).toEqual({ path: "docs" });
  });

  it("keeps nested segments so a path also makes folders", () => {
    expect(newEntryPath("src", "pkg/thing.go", "file")).toEqual({
      path: "src/pkg/thing.go",
    });
    expect(newEntryPath("src", "a/b", "folder")).toEqual({ path: "src/a/b" });
  });

  it("trims the typed name", () => {
    expect(newEntryPath("src", "  main.ts  ", "file")).toEqual({ path: "src/main.ts" });
  });

  it("accepts a trailing slash only when making a folder", () => {
    expect(newEntryPath("src", "pkg/", "folder")).toEqual({ path: "src/pkg" });
    expect("error" in newEntryPath("src", "pkg/", "file")).toBe(true);
  });

  it("refuses names that would leave the folder", () => {
    for (const kind of ["file", "folder"] as const) {
      for (const typed of ["", "   ", "/etc/passwd", "../up", "a/../b", "a//b"]) {
        const result = newEntryPath("src", typed, kind);
        expect(
          "error" in result,
          `expected refusal for ${kind} ${JSON.stringify(typed)}`,
        ).toBe(true);
      }
    }
  });
});

describe("renameEntryPath", () => {
  it("keeps the parent and refuses slashes", () => {
    expect(renameEntryPath("src/main.ts", "app.ts")).toEqual({ path: "src/app.ts" });
    expect("error" in renameEntryPath("src/main.ts", "other/name.ts")).toBe(true);
  });

  it("returns unchanged when the basename is identical", () => {
    expect(renameEntryPath("src/main.ts", "main.ts")).toEqual({ unchanged: true });
  });
});

describe("renameSelectionRange", () => {
  it("excludes the extension", () => {
    expect(renameSelectionRange("main.ts")).toEqual({ start: 0, end: 4 });
    expect(renameSelectionRange("README")).toEqual({ start: 0, end: 6 });
  });
});

describe("sourceCreateErrorMessage", () => {
  it("names the collision for a taken path", () => {
    const err = new LycaonApiError("nope", 409, "source_already_exists");
    expect(sourceCreateErrorMessage(err)).toContain("already exists");
  });

  it("falls back to the error text, then to generic copy", () => {
    expect(sourceCreateErrorMessage(new Error("offline"))).toBe("offline");
    expect(sourceCreateErrorMessage(null)).toBe("Could not create it.");
  });
});
