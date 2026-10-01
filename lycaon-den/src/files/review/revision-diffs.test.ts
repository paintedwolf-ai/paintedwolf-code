import { afterEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceGitReviewFile, SourceRevisionComparison, SourceRevisionReview } from "../../api/types.ts";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";
import { looksLikeRevisionSpec, openRevisionDiffs, resolveRevisionComparisons } from "./revision-diffs.ts";
import { loadGitDiffs, resetGitDiffsForTests } from "./git-diffs.ts";
import { diffsFileRowSource, gitDiffsNetSelector } from "./diffs-row-source.ts";
import type { GitDiffsAddress } from "./diffs-address.ts";

const BEFORE = "b".repeat(40);
const AFTER = "a1b2c3d".padEnd(40, "0");
const COMPARISON: SourceRevisionComparison = {
  root_id: "r1", spec: "a1b2c3d", kind: "commit", label: "a1b2c3d Fix the loader",
  before_commit: BEFORE, after_commit: AFTER, subject: "Fix the loader",
};
const ADDRESS: GitDiffsAddress = {
  kind: "git", rootId: "r1", spec: "a1b2c3d", label: "a1b2c3d Fix the loader", beforeCommit: BEFORE, afterCommit: AFTER,
};

function reviewFile(path: string, op: SourceGitReviewFile["op"] = "write"): SourceGitReviewFile {
  return {
    path, before_path: path, op, before_mode: "100644", after_mode: "100644",
    before_oid: op === "create" ? "" : "1".repeat(40), after_oid: op === "delete" ? "" : "2".repeat(40),
    insertions: 1, deletions: 1, binary: false,
  };
}

function review(files: SourceGitReviewFile[], next?: string): SourceRevisionReview {
  return { root_id: "r1", before_commit: BEFORE, after_commit: AFTER, files, files_total: files.length, insertions: 0, deletions: 0, next_cursor: next };
}

afterEach(() => {
  resetOpenFilesSurfaceForTests();
  resetGitDiffsForTests();
});

describe("what the palette treats as a revision", () => {
  it("passes one token that reads as a commit id, ref name, or range", () => {
    for (const text of ["a1b2c3d", "A1B2C3D4E5", "main", "feature/login-form", "v1.2.0", "HEAD~2", "main^", "main..HEAD",
      "a...b", "topic..", "..topic", " main "]) {
      expect(looksLikeRevisionSpec(text), text).toBe(true);
    }
  });

  it("refuses text with spaces, option-like tokens, and names git cannot hold", () => {
    for (const text of ["", "  ", "fix the loader", "main HEAD", "-p", "--output=x", "main..-p", "..", "...", "a..b..c",
      "main:src/app.ts", "feature/", "/main", "a//b", ".hidden", "topic.lock", "what?", "a*", "x@{1}", "tab\tname", "x".repeat(257)]) {
      expect(looksLikeRevisionSpec(text), JSON.stringify(text)).toBe(false);
    }
  });
});

describe("resolving and opening a revision", () => {
  it("asks the host only for a plausible revision, trimmed, with the chat's scope", async () => {
    const resolve = vi.fn(async () => ({ comparisons: [COMPARISON] }));
    const client = stubClient({ resolveProjectSourceRevisions: resolve });
    const signal = new AbortController().signal;
    await expect(resolveRevisionComparisons(client, "p1", " a1b2c3d ", { sessionId: "s1", signal })).resolves.toEqual([COMPARISON]);
    expect(resolve).toHaveBeenCalledWith("p1", "a1b2c3d", { sessionId: "s1", signal });
    await expect(resolveRevisionComparisons(client, "p1", "fix the loader")).resolves.toEqual([]);
    expect(resolve).toHaveBeenCalledTimes(1);
  });

  it("opens the diffs page on the resolved commits", () => {
    const opened = vi.fn();
    registerOpenFilesSurfaceSink(opened);
    openRevisionDiffs("p1", COMPARISON);
    expect(opened).toHaveBeenCalledWith({ kind: "diffs", projectId: "p1", address: ADDRESS });
  });
});

describe("a Git diffs page's files", () => {
  it("reads every review page once, since two commits never change", async () => {
    const get = vi.fn()
      .mockResolvedValueOnce(review([reviewFile("a.go")], "cursor-1"))
      .mockResolvedValueOnce(review([reviewFile("b.go", "create")]));
    const client = stubClient({ getProjectSourceRevisionReview: get });
    const diffs = await loadGitDiffs(client, "p1", ADDRESS);
    expect(diffs.truncated).toBe(false);
    expect(diffs.files.map((row) => row.file.path)).toEqual(["a.go", "b.go"]);
    expect(get).toHaveBeenNthCalledWith(2, "p1", { rootId: "r1", beforeCommit: BEFORE, afterCommit: AFTER }, { cursor: "cursor-1", limit: 499 });
    await loadGitDiffs(client, "p1", { ...ADDRESS, label: "renamed" });
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("refuses a page answered for other commits", async () => {
    const client = stubClient({ getProjectSourceRevisionReview: vi.fn(async () => ({ ...review([]), after_commit: BEFORE })) });
    await expect(loadGitDiffs(client, "p1", ADDRESS)).rejects.toThrow("different commits");
  });

  it("presents each file as its net range between the commits, with no writes to step through", () => {
    const context = { client: () => null, projectId: () => "p1", address: () => ADDRESS, markUserEdits: () => true, rootRefs: () => undefined };
    const deleted = diffsFileRowSource(context, { kind: "git", address: ADDRESS, file: reviewFile("gone.go", "delete") });
    expect(deleted.revisions).toEqual([]);
    expect(deleted.change(null)).toBe("deleted");
    expect(deleted.openable(null)).toBe(false);
    expect(diffsFileRowSource(context, { kind: "git", address: ADDRESS, file: reviewFile("new.go", "create") }).change(null)).toBe("added");
    expect(gitDiffsNetSelector(ADDRESS, reviewFile("a.go"))).toEqual({
      kind: "git_range", root_id: "r1", before_commit: BEFORE, after_commit: AFTER, path: "a.go",
    });
  });
});
