import { describe, expect, it } from "vitest";
import { wireProject } from "./mocks/project-fixture.ts";
import {
  longestMatchingRoot,
  normalizeRepoRelativePath,
  resolveProjectFile,
} from "./project-path.ts";

describe("normalizeRepoRelativePath", () => {
  it("normalizes backslashes and strips leading slashes", () => {
    expect(normalizeRepoRelativePath("src\\foo.ts")).toBe("src/foo.ts");
    expect(normalizeRepoRelativePath("/src/foo.ts")).toBe("src/foo.ts");
    expect(normalizeRepoRelativePath("  a/b  ")).toBe("a/b");
  });
});

describe("resolveProjectFile", () => {
  const twoRoots = {
    roots: [
      { id: "root-main", path: "/Users/me/repo", is_primary: true, label: "repo" },
      { id: "root-app", path: "/Users/me/app", is_primary: false, label: "App" },
    ],
  };

  it("resolves a host-qualified @label path under the labeled root", () => {
    expect(resolveProjectFile(twoRoots, "@app/src/a.ts", "macos")).toEqual({
      absolutePath: "/Users/me/app/src/a.ts",
      rootId: "root-app",
    });
    // The root itself, as a folder reference.
    expect(resolveProjectFile(twoRoots, "@app", "macos")).toEqual({
      absolutePath: "/Users/me/app",
      rootId: "root-app",
    });
  });

  it("rejects a label no root carries instead of guessing the primary root", () => {
    expect(resolveProjectFile(twoRoots, "@elsewhere/src/a.ts", "macos")).toEqual({
      error: "outside_roots",
    });
  });

  it("joins under the sole / primary root", () => {
    const project = wireProject("/Users/me/repo");
    const got = resolveProjectFile(project, "src/main.ts", "macos");
    expect(got).toEqual({
      absolutePath: "/Users/me/repo/src/main.ts",
      rootId: "proj-1-root",
    });
  });

  it("accepts backslash-relative paths", () => {
    const project = wireProject("/Users/me/repo");
    const got = resolveProjectFile(project, "src\\lib\\a.ts", "macos");
    expect(got).toEqual({
      absolutePath: "/Users/me/repo/src/lib/a.ts",
      rootId: "proj-1-root",
    });
  });

  it("picks longest matching root for absolute paths (nested roots)", () => {
    const project = {
      roots: [
        { id: "outer", path: "/work/mono", is_primary: true },
        { id: "inner", path: "/work/mono/packages/app", is_primary: false },
      ],
    };
    const got = resolveProjectFile(
      project,
      "/work/mono/packages/app/src/x.ts",
      "macos",
    );
    expect(got).toEqual({
      absolutePath: "/work/mono/packages/app/src/x.ts",
      rootId: "inner",
    });
  });

  it("joins relative under primary then re-controls via longest prefix", () => {
    const project = {
      roots: [
        { id: "outer", path: "/work/mono", is_primary: true },
        { id: "inner", path: "/work/mono/packages/app", is_primary: false },
      ],
    };
    // Relative join under primary yields a path contained by the longer inner root.
    const got = resolveProjectFile(
      project,
      "packages/app/src/x.ts",
      "macos",
    );
    expect(got).toEqual({
      absolutePath: "/work/mono/packages/app/src/x.ts",
      rootId: "inner",
    });
  });

  it("fails closed when absolute path is outside all roots", () => {
    const project = wireProject("/Users/me/repo");
    const got = resolveProjectFile(project, "/tmp/elsewhere.ts", "macos");
    expect(got).toEqual({ error: "outside_roots" });
  });

  it("fails closed on empty path and empty roots", () => {
    expect(resolveProjectFile(wireProject("/r"), "  ", "macos")).toEqual({
      error: "empty_path",
    });
    expect(resolveProjectFile({ roots: [] }, "a.ts", "macos")).toEqual({
      error: "no_roots",
    });
  });

  it("rejects .. escape that leaves the root", () => {
    const project = wireProject("/Users/me/repo");
    const got = resolveProjectFile(project, "../../etc/passwd", "macos");
    expect(got).toEqual({ error: "outside_roots" });
  });

  it("joins under Windows drive roots", () => {
    const project = {
      roots: [
        { id: "win", path: "C:\\Users\\me\\repo", is_primary: true },
      ],
    };
    const got = resolveProjectFile(project, "src/a.ts", "windows");
    expect(got).toEqual({
      absolutePath: "C:\\Users\\me\\repo\\src\\a.ts",
      rootId: "win",
    });
  });

});

describe("longestMatchingRoot", () => {
  it("returns undefined when nothing matches", () => {
    expect(
      longestMatchingRoot("/a/b", [{ id: "x", path: "/z" }], "macos"),
    ).toBeUndefined();
  });
});
