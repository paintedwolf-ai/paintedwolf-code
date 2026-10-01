import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { SourceComparison } from "../../api/types.ts";
import { computeDiffHunks } from "../review/line-diff.ts";
import {
  applyFilePutBatch,
  applyFileUndo,
  restoreFileVersion,
  writeFileContent,
} from "./file-mutations.ts";
import {
  historyUnavailableCopy,
  isCreateDiff,
  isTombstoneDiff,
  planFileRevert,
  rejectHunkInFile,
  revertFileToBaseline,
} from "../history/file-revert.ts";
import {
  comparisonEndpoints,
  type ComparisonEndpoints,
} from "../source/source-comparison.ts";

function comparison(
  before: string,
  after: string,
  opts: { beforeAbsent?: boolean; afterAbsent?: boolean } = {},
): SourceComparison {
  const side = (content: string, absent: boolean) => ({
    state: absent ? "absent" as const : "content" as const,
    size_bytes: content.length,
    availability: absent ? "absent" as const : "available" as const,
    content,
    ...(absent ? {} : { sha256: `sha-${content.length}` }),
  });
  return {
    truncated: false,
    in_range: true,
    before: side(before, opts.beforeAbsent === true),
    after: side(after, opts.afterAbsent === true),
    location_changed: false,
  };
}

/** Narrows a fixture to its endpoints; the fixtures always have them. */
function endpoints(diff: SourceComparison): ComparisonEndpoints {
  const pair = comparisonEndpoints(diff);
  expect(pair).not.toBeNull();
  return pair!;
}

describe("file actions", () => {
  it("maps 409 to conflict without blind retry", async () => {
    const client = stubClient({
      replaceProjectSource: vi.fn().mockRejectedValue(
        new LycaonApiError("Conflict", 409, "source_mutation_diverged"),
      ),
    });
    const res = await writeFileContent(client, "p1", {
      rootId: "r1",
      path: "a.ts",
      content: "x\n",
      encoding: "utf-8",
      baseSha256: "old",
    });
    expect(res).toEqual({ ok: false, conflict: true });
  });

  it("maps a 404 restoring a version to conflict, same as a 409", async () => {
    const restoreProjectSourceVersion = vi.fn().mockRejectedValue(
      new LycaonApiError("Not Found", 404, "source_not_found"),
    );
    const client = stubClient({ restoreProjectSourceVersion });
    const res = await restoreFileVersion(client, "p1", {
      versionId: "version-1",
      fileId: "file-a",
      rootId: "r1",
      path: "a.ts",
      base: { state: "content", sha256: "after-sha" },
    });
    expect(res).toEqual({ ok: false, conflict: true });
  });

  it("reports batch skips for conflicts", async () => {
    const put = vi
      .fn()
      .mockResolvedValueOnce({ sha256: "ok", size_bytes: 1 })
      .mockRejectedValueOnce(new LycaonApiError("Conflict", 409, "source_mutation_diverged"));
    const client = stubClient({ replaceProjectSource: put });
    const report = await applyFilePutBatch(client, "p1", [
      {
        rootId: "r",
        path: "a.ts",
        content: "a\n",
        encoding: "utf-8",
        baseSha256: "1",
        undoContent: "A\n",
      },
      {
        rootId: "r",
        path: "b.ts",
        content: "b\n",
        encoding: "utf-8",
        baseSha256: "2",
        undoContent: "B\n",
      },
    ]);
    expect(report.ok).toBe(1);
    expect(report.skipped).toEqual([{ path: "b.ts", reason: "changed since" }]);
    expect(report.undos).toEqual([
      {
        kind: "put",
        rootId: "r",
        path: "a.ts",
        content: "A\n",
        encoding: "utf-8",
        baseSha256: "ok",
      },
    ]);
  });

  it("exposes the history-unavailable copy", () => {
    expect(historyUnavailableCopy()).toBe(
      "This comparison has metadata, but its text content is unavailable.",
    );
    expect(historyUnavailableCopy({
      ...comparison("old\n", "new\n"),
      after: {
        state: "content",
        size_bytes: 4,
        availability: "unavailable",
        content: "",
      },
    })).toContain("unavailable");
  });

  it("classifies tombstone and create diffs", () => {
    expect(
      isTombstoneDiff(
        endpoints(comparison("old\n", "", { afterAbsent: true })),
        { state: "absent" },
      ),
    ).toBe(true);
    expect(
      isCreateDiff(endpoints(comparison("", "new\n", { beforeAbsent: true }))),
    ).toBe(true);
  });

  it("plans a deleted-file revert as create", async () => {
    const client = stubClient({
      getProjectSourceComparison: vi.fn().mockResolvedValue(
        comparison("kept\n", "", { afterAbsent: true }),
      ),
    });
    const plan = await planFileRevert(client, "p1", {
      fileId: "file-gone",
      rootId: "r1",
      path: "gone.ts",
      baseline: "presentation",
      tip: { state: "absent" },
    });
    expect(plan).toMatchObject({
      ok: true,
      member: { kind: "create", path: "gone.ts", content: "kept\n" },
    });
  });

  it("plans a created-file revert as delete", async () => {
    const client = stubClient({
      getProjectSourceComparison: vi.fn().mockResolvedValue(
        comparison("", "new\n", { beforeAbsent: true }),
      ),
    });
    const plan = await planFileRevert(client, "p1", {
      fileId: "file-new",
      rootId: "r1",
      path: "new.ts",
      baseline: "presentation",
    });
    expect(plan).toMatchObject({
      ok: true,
      member: { kind: "delete", path: "new.ts" },
    });
  });

  it("reverts a deleted file with create then put", async () => {
    const client = stubClient({
      getProjectSourceComparison: vi.fn().mockResolvedValue(
        comparison("kept\n", "", { afterAbsent: true }),
      ),
      createProjectSourceEntry: vi.fn().mockResolvedValue({ path: "gone.ts" }),
      getProjectSource: vi.fn().mockResolvedValue({
        sha256: "empty",
        encoding: "utf-8",
        content: "",
      }),
      replaceProjectSource: vi.fn().mockResolvedValue({ sha256: "full", size_bytes: 5 }),
    });
    const res = await revertFileToBaseline(client, "p1", {
      fileId: "file-gone",
      rootId: "r1",
      path: "gone.ts",
      baseline: "presentation",
      tip: { state: "absent" },
    });
    expect(res).toMatchObject({
      ok: true,
      label: "Reverted gone.ts",
      undo: { kind: "delete", path: "gone.ts" },
    });
    expect(client.createProjectSourceEntry).toHaveBeenCalled();
    expect(client.replaceProjectSource).toHaveBeenCalled();
  });

  it("cleans up an empty create when the put conflicts", async () => {
    const client = stubClient({
      getProjectSourceComparison: vi.fn().mockResolvedValue(
        comparison("kept\n", "", { afterAbsent: true }),
      ),
      createProjectSourceEntry: vi.fn().mockResolvedValue({ path: "gone.ts" }),
      getProjectSource: vi.fn().mockResolvedValue({
        sha256: "empty",
        encoding: "utf-8",
        content: "",
      }),
      replaceProjectSource: vi.fn().mockRejectedValue(
        new LycaonApiError("Conflict", 409, "source_mutation_diverged"),
      ),
      deleteProjectSource: vi.fn().mockResolvedValue(undefined),
    });
    const res = await revertFileToBaseline(client, "p1", {
      fileId: "file-gone",
      rootId: "r1",
      path: "gone.ts",
      baseline: "presentation",
      tip: { state: "absent" },
    });
    expect(res).toEqual({ ok: false, conflict: true });
    expect(client.deleteProjectSource).toHaveBeenCalled();
  });

  it("rejects a hunk with a put and holds the previous after for undo", async () => {
    const before = "a\nb\n";
    const after = "a\nB\n";
    const hunk = computeDiffHunks(before, after).find((h) => h.kind === "change")!;
    const client = stubClient({
      replaceProjectSource: vi.fn().mockResolvedValue({ sha256: "next", size_bytes: 4 }),
    });
    const res = await rejectHunkInFile(client, "p1", {
      rootId: "r1",
      path: "a.ts",
      hunk,
      before,
      after,
      encoding: "utf-8",
      baseSha256: "old",
    });
    expect(res).toMatchObject({
      ok: true,
      label: "Rejected hunk in a.ts",
      undo: { kind: "put", content: after, baseSha256: "next" },
    });
  });

  it("undoes a deleted-file revert and remembers its bytes", async () => {
    const client = stubClient({
      getProjectSource: vi.fn().mockResolvedValue({
        sha256: "full",
        encoding: "utf-8",
        content: "kept\n",
      }),
      deleteProjectSource: vi.fn().mockResolvedValue(undefined),
    });
    const res = await applyFileUndo(client, "p1", {
      kind: "delete",
      rootId: "r1",
      path: "gone.ts",
    });
    expect(res).toMatchObject({
      ok: true,
      undo: { kind: "create", path: "gone.ts", content: "kept\n" },
    });
    expect(client.deleteProjectSource).toHaveBeenCalled();
  });

  it("does not delete when it cannot capture Undo bytes", async () => {
    const client = stubClient({
      getProjectSource: vi.fn().mockRejectedValue(new Error("read failed")),
      deleteProjectSource: vi.fn(),
    });
    const res = await applyFileUndo(client, "p1", {
      kind: "delete",
      rootId: "r1",
      path: "gone.ts",
    });
    expect(res).toEqual({ ok: false, conflict: false, error: "read failed" });
    expect(client.deleteProjectSource).not.toHaveBeenCalled();
  });

  it("applies exact-version Undo through the version restore endpoint", async () => {
    const restoreProjectSourceVersion = vi.fn().mockResolvedValue({
      version_id: "version-before",
      previous_version_id: "version-after",
      file_id: "file-a",
      root_id: "r1",
      path: "a.ts",
      state: "content",
      sha256: "before-sha",
      changed: true,
    });
    const client = stubClient({ restoreProjectSourceVersion });
    const res = await applyFileUndo(client, "p1", {
      kind: "version",
      versionId: "version-before",
      fileId: "file-a",
      rootId: "r1",
      path: "a.ts",
      base: { state: "content", sha256: "after-sha" },
    }, "session-1");
    expect(restoreProjectSourceVersion).toHaveBeenCalledWith(
      "p1",
      "version-before",
      expect.objectContaining({
        file_id: "file-a",
        base: { state: "content", sha256: "after-sha" },
      }),
      "session-1",
    );
    expect(res).toMatchObject({
      ok: true,
      undo: {
        kind: "version",
        versionId: "version-after",
        base: { state: "content", sha256: "before-sha" },
      },
    });
  });

});
