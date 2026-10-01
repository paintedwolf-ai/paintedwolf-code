import { describe, expect, it } from "vitest";
import type {
  SourceFileCommit,
  SourceFileVersion,
  SourceGitChange,
} from "../../api/types.ts";
import {
  buildFileHistoryRows,
  fileCommitMeta,
  gitLaneNotice,
  gitLaneSettled,
  shortCommit,
} from "./file-history-model.ts";
import type { FileVersionHistory } from "./file-version.ts";

function version(
  id: string,
  overrides: Partial<SourceFileVersion> = {},
): SourceFileVersion {
  return {
    id,
    file_id: "file-a",
    workspace_kind: "project",
    root_id: "r1",
    path: "src/a.ts",
    state: "content",
    content_sha256: `sha-${id}`,
    size_bytes: 1,
    capture_state: "stored",
    capture_quality: "exact",
    landing: "working_file",
    ordinal: 2,
    turn: 0,
    created_at: "2026-08-26T10:00:00Z",
    ...overrides,
  };
}

function commit(overrides: Partial<SourceFileCommit> = {}): SourceFileCommit {
  return {
    commit: overrides.commit ?? "aaaa1111bbbb",
    subject: overrides.subject ?? "a subject",
    author_name: overrides.author_name ?? "Sam Okafor",
    authored_at: overrides.authored_at ?? "2026-08-25T10:00:00Z",
    committed_at: overrides.committed_at ?? "2026-08-25T10:00:00Z",
    source_path: overrides.source_path ?? "src/a.ts",
    blob_oid: "blob_oid" in overrides ? overrides.blob_oid : "oid-1",
    matches_version_id: overrides.matches_version_id,
    arrival_git_change_id: overrides.arrival_git_change_id,
  };
}

const movement: SourceGitChange = {
  session_id: "", turn: 0, tool_call_id: "", tool_name: "",
  id: "t-pull",
  root_id: "r1",
  kind: "pull",
  to_ref: "main",
  ordinal: 7,
  observed_at: "2026-08-26T09:00:00Z",
};

function history(overrides: Partial<FileVersionHistory> = {}): FileVersionHistory {
  return {
    status: "ready",
    gitStatus: "ready",
    current: { state: "content", sha256: "current" },
    versions: [],
    commits: [],
    arrivals: [],
    gitHistoryState: "available",
    trackedSince: "2026-08-26T08:00:00Z",
    nextVersionsCursor: null,
    nextGitCursor: null,
    loadingMore: false,
    ...overrides,
  };
}

describe("buildFileHistoryRows", () => {
  it("folds a byte-identical commit into the version row it duplicates", () => {
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v1")],
      commits: [commit({ matches_version_id: "v1" })],
    }));
    expect(rows).toHaveLength(1);
    const row = rows[0]!;
    if (row.kind !== "version") throw new Error("invariant: expected a version row");
    expect(row.commits.map((c) => c.commit)).toEqual(["aaaa1111bbbb"]);
  });

  it("places an arrival group under the version its movement produced", () => {
    const caused = version("v-git", {
      git_change: { ...movement, id: "t-pull" },
    });
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v-newest"), caused],
      arrivals: [movement],
      commits: [
        commit({ commit: "ffff2222", arrival_git_change_id: "t-pull" }),
        commit({ commit: "eeee3333", arrival_git_change_id: "t-pull" }),
      ],
    }));
    expect(rows.map((row) => row.kind)).toEqual(["version", "version", "arrival"]);
    const group = rows[2]!;
    if (group.kind !== "arrival") throw new Error("invariant: expected an arrival row");
    expect(group.change.id).toBe("t-pull");
    expect(group.commits.map((entry) => entry.commit.commit))
      .toEqual(["ffff2222", "eeee3333"]);
  });

  it("still lists an arrival whose movement is on this page but no version is", () => {
    const { rows } = buildFileHistoryRows(history({
      arrivals: [movement],
      commits: [commit({ commit: "ffff2222", arrival_git_change_id: "t-pull" })],
    }));
    expect(rows.map((row) => row.kind)).toEqual(["arrival"]);
  });

  it("keeps commits whose arrival movement the page could not resolve", () => {
    const { rows } = buildFileHistoryRows(history({
      commits: [commit({ commit: "ffff2222", arrival_git_change_id: "t-missing" })],
    }));
    expect(rows.map((row) => row.kind)).toEqual(["boundary", "commit"]);
  });

  it("puts the boundary above history that only git holds", () => {
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v1")],
      commits: [commit({ commit: "cccc4444" })],
    }));
    expect(rows.map((row) => row.kind)).toEqual(["version", "boundary", "commit"]);
    const boundary = rows[1]!;
    if (boundary.kind !== "boundary") throw new Error("invariant: expected a boundary");
    expect(boundary.trackedSince).toBe("2026-08-26T08:00:00Z");
  });

  it("omits the boundary when nothing sits below it", () => {
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v1")],
      commits: [commit({ matches_version_id: "v1" })],
    }));
    expect(rows.some((row) => row.kind === "boundary")).toBe(false);
  });

  it("shows the boundary when the git era is only a page away", () => {
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v1")],
      commits: [commit({ matches_version_id: "v1" })],
      nextGitCursor: "git-page-2",
    }));
    expect(rows.map((row) => row.kind)).toEqual(["version", "boundary"]);
  });

  it("omits the boundary entirely when the root is not a git checkout", () => {
    const { rows } = buildFileHistoryRows(history({
      versions: [version("v1")],
      gitHistoryState: "no_repository",
      trackedSince: "2026-08-26T08:00:00Z",
    }));
    expect(rows.map((row) => row.kind)).toEqual(["version"]);
  });

  it("folds a tracking baseline that only restates the working file", () => {
    const baseline = version("v-baseline", { content_sha256: "current" });
    delete baseline.op;
    delete baseline.origin;
    const built = buildFileHistoryRows(history({
      versions: [baseline],
      commits: [commit({ matches_version_id: "v-baseline" })],
    }));
    expect(built.rows.some((row) => row.kind === "version")).toBe(false);
    expect(built.currentCommits.map((c) => c.commit)).toEqual(["aaaa1111bbbb"]);
  });

  it("keeps a baseline that no longer matches the working file", () => {
    const baseline = version("v-baseline", { content_sha256: "older" });
    delete baseline.op;
    delete baseline.origin;
    const built = buildFileHistoryRows(history({ versions: [baseline] }));
    expect(built.rows.map((row) => row.kind)).toEqual(["version"]);
    expect(built.currentCommits).toEqual([]);
  });

  it("keeps the newest recorded edit even when it holds the current bytes", () => {
    const built = buildFileHistoryRows(history({
      versions: [version("v1", { content_sha256: "current", op: "write", origin: "agent" })],
    }));
    expect(built.rows.map((row) => row.kind)).toEqual(["version"]);
  });

  it("pairs each commit with the previous commit that still held bytes", () => {
    const { rows } = buildFileHistoryRows(history({
      commits: [
        commit({ commit: "newest", blob_oid: "oid-3" }),
        commit({ commit: "removal", blob_oid: undefined }),
        commit({ commit: "oldest", blob_oid: "oid-1" }),
      ],
    }));
    const entries = rows.filter((row) => row.kind === "commit");
    expect(entries).toHaveLength(3);
    const previous = entries.map((row) =>
      row.kind === "commit" ? row.entry.previousBlobOid : null
    );
    expect(previous).toEqual(["oid-1", "oid-1", null]);
  });
});

describe("commit labels", () => {
  it("shortens a hash for display", () => {
    expect(shortCommit("a1b2c3f4d5e6f7a8")).toBe("a1b2c3f");
  });

  it("names the author and git's own subject", () => {
    expect(fileCommitMeta(commit())).toBe("Sam Okafor · a subject");
  });

  it("says plainly when a commit removed the file", () => {
    expect(fileCommitMeta(commit({ blob_oid: undefined })))
      .toContain("removed the file");
  });
});

describe("git lane states", () => {
  it("treats answered and absent lanes as settled, unfinished ones as not", () => {
    expect(gitLaneSettled("available")).toBe(true);
    expect(gitLaneSettled("not_tracked")).toBe(true);
    expect(gitLaneSettled("no_repository")).toBe(true);
    expect(gitLaneSettled("not_requested")).toBe(false);
    expect(gitLaneSettled("timed_out")).toBe(false);
    expect(gitLaneSettled("failed")).toBe(false);
  });

  it("reports only the lanes that were asked and could not answer", () => {
    expect(gitLaneNotice("timed_out")).toContain("Earlier commits may exist");
    expect(gitLaneNotice("failed")).toContain("Earlier commits may exist");
    expect(gitLaneNotice("available")).toBeNull();
    expect(gitLaneNotice("no_repository")).toBeNull();
    expect(gitLaneNotice("not_tracked")).toBeNull();
    expect(gitLaneNotice("not_requested")).toBeNull();
  });
});
