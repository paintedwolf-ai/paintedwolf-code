import { describe, expect, it } from "vitest";
import { wireProject } from "./mocks/project-fixture.ts";
import {
  longestMatchingRoot,
  normalizeRepoRelativePath,
  projectFileResolvable,
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

  it("matches @label prefixes case-insensitively in either direction", () => {
    const lowerLabel = {
      roots: [
        { id: "root-main", path: "/Users/me/repo", is_primary: true, label: "repo" },
        { id: "root-app", path: "/Users/me/app", is_primary: false, label: "app" },
      ],
    };
    // Uppercase input against a lowercase stored label.
    expect(resolveProjectFile(lowerLabel, "@APP/src/a.ts", "macos")).toEqual({
      absolutePath: "/Users/me/app/src/a.ts",
      rootId: "root-app",
    });
    expect(resolveProjectFile(lowerLabel, "@APP", "macos")).toEqual({
      absolutePath: "/Users/me/app",
      rootId: "root-app",
    });
    // Mixed case against the existing mixed-case label.
    expect(resolveProjectFile(twoRoots, "@App/src/a.ts", "macos")).toEqual({
      absolutePath: "/Users/me/app/src/a.ts",
      rootId: "root-app",
    });
  });

  it("resolves a bare path under the session's active root when active ≠ primary", () => {
    expect(
      resolveProjectFile({ ...twoRoots, activeRootId: "root-app" }, "src/a.ts", "macos"),
    ).toEqual({
      absolutePath: "/Users/me/app/src/a.ts",
      rootId: "root-app",
    });
    // The active root itself, as a folder reference.
    expect(
      resolveProjectFile({ ...twoRoots, activeRootId: "root-app" }, ".", "macos"),
    ).toEqual({
      absolutePath: "/Users/me/app",
      rootId: "root-app",
    });
  });

  it("falls back to the primary root when activeRootId is empty or unknown", () => {
    for (const activeRootId of [undefined, "", "   ", "root-gone"]) {
      expect(
        resolveProjectFile({ ...twoRoots, activeRootId }, "src/a.ts", "macos"),
      ).toEqual({
        absolutePath: "/Users/me/repo/src/a.ts",
        rootId: "root-main",
      });
    }
  });

  it("fails closed when a bare path escapes the active root", () => {
    const nested = {
      roots: [
        { id: "outer", path: "/work/mono", is_primary: true },
        { id: "inner", path: "/work/mono/packages/app", is_primary: false },
      ],
      activeRootId: "inner",
    };
    expect(resolveProjectFile(nested, "../../elsewhere.ts", "macos")).toEqual({
      error: "outside_roots",
    });
  });

  it("prefers a stored part root_id over the session's active root", () => {
    // Legacy message part pinned to root-app: the pin scopes resolution even
    // when the session's active root is a different one.
    expect(
      projectFileResolvable(twoRoots.roots, "/Users/me/app/src/a.ts", "root-app", "root-main"),
    ).toBe(true);
    // The same path is not resolvable when pinned to the primary root.
    expect(
      projectFileResolvable(twoRoots.roots, "/Users/me/app/src/a.ts", "root-main", "root-app"),
    ).toBe(false);
    // Unpinned legacy part: the bare path resolves under the active root.
    expect(
      projectFileResolvable(twoRoots.roots, "src/a.ts", undefined, "root-app"),
    ).toBe(true);
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
