import { describe, expect, it, vi } from "vitest";
import { loadGitWorkspaceStatus } from "./git-workspace-status.ts";
import { LycaonApiError } from "../../api/http.ts";

const summary = {
  available: true, repo_id: "repo-1", root_ids: ["r1"], revision: 7, refreshing: true,
  branch: "main", ahead: 0, behind: 0, dirty: true, staged_count: 1, unstaged_count: 1, changed_count: 2,
};

describe("loadGitWorkspaceStatus", () => {
  it("collects every changed-file page from the summary's revision", async () => {
    const first = Array.from({ length: 500 }, (_, i) => ({ path: `file-${i}`, status: " M", root_id: "r1", root_relative_path: `file-${i}` }));
    const last = { path: "last", status: " M", root_id: "r1", root_relative_path: "last" };
    const getGitStatus = vi.fn(async () => ({ ...summary, changed_count: 501 }));
    const listGitChanges = vi.fn()
      .mockResolvedValueOnce({ repo_id: "repo-1", revision: 7, refreshing: false, files: first, next_cursor: "more" })
      .mockResolvedValueOnce({ repo_id: "repo-1", revision: 7, refreshing: false, files: [last] });
    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1", "s1");
    expect(status.files).toEqual([...first, last]);
    expect(listGitChanges).toHaveBeenNthCalledWith(2, "p1", "repo-1", { session_id: "s1", revision: 7, limit: 500, cursor: "more" });
  });

  it("restarts the complete read when a continuation's revision expires", async () => {
    const stale = { path: "stale", status: " M", root_id: "r1", root_relative_path: "stale" };
    const current = { ...stale, path: "current", root_relative_path: "current" };
    const getGitStatus = vi.fn()
      .mockResolvedValueOnce(summary)
      .mockResolvedValueOnce({ ...summary, revision: 8 });
    const listGitChanges = vi.fn()
      .mockResolvedValueOnce({ repo_id: "repo-1", revision: 7, refreshing: false, files: [stale], next_cursor: "more" })
      .mockRejectedValueOnce(new LycaonApiError("Expired", 409, "cursor_generation_expired"))
      .mockResolvedValueOnce({ repo_id: "repo-1", revision: 8, refreshing: false, files: [current] });
    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1");
    expect(status.files).toEqual([current]);
    expect(listGitChanges).toHaveBeenNthCalledWith(3, "p1", "repo-1", { session_id: undefined, revision: 8, limit: 500 });
  });

  it("does not publish an empty file list when no coherent snapshot becomes available", async () => {
    const getGitStatus = vi.fn(async () => ({ ...summary, revision: 0 }));
    const listGitChanges = vi.fn();
    await expect(loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1")).rejects.toThrow("Git status is still loading");
    expect(listGitChanges).not.toHaveBeenCalled();
  });
  it("answers with the changed files of a published revision that is refreshing behind", async () => {
    const files = [
      { path: "README.md", status: " M", root_id: "r1", root_relative_path: "README.md" },
      { path: "src/app.ts", status: " M", root_id: "r1", root_relative_path: "src/app.ts" },
    ];
    const getGitStatus = vi.fn(async () => summary);
    const listGitChanges = vi.fn(async () => ({ repo_id: "repo-1", revision: 7, refreshing: true, files }));

    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1", "s1");

    expect(status.files.map((file) => file.path)).toEqual(["README.md", "src/app.ts"]);
    expect(status.branch).toBe("main");
    expect(listGitChanges).toHaveBeenCalledExactlyOnceWith("p1", "repo-1", { session_id: "s1", revision: 7, limit: 500 });
  });

  it("waits for the first snapshot before answering", async () => {
    const loading = { ...summary, revision: 0, branch: undefined };
    const getGitStatus = vi.fn()
      .mockResolvedValueOnce(loading)
      .mockResolvedValueOnce({ ...summary, revision: 3, refreshing: false });
    const listGitChanges = vi.fn(async () => ({ repo_id: "repo-1", revision: 3, refreshing: false, files: [] }));

    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1");

    expect(status.branch).toBe("main");
    expect(getGitStatus).toHaveBeenCalledTimes(2);
  });

  it("answers an unavailable repository without reading changes", async () => {
    const getGitStatus = vi.fn(async () => ({ ...summary, available: false, revision: 0 }));
    const listGitChanges = vi.fn();

    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "p1", "repo-1");

    expect(status).toMatchObject({ available: false, files: [] });
    expect(listGitChanges).not.toHaveBeenCalled();
  });
});
