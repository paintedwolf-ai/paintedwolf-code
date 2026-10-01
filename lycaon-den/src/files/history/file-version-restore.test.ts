// @vitest-environment jsdom
import { stubClient } from "../../test/client-fixture.ts";

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileVersionView } from "./file-version.ts";
import {
  restoreRetainedVersion,
  versionRestoreDisabledReason,
  versionRestoreOffered,
} from "./file-version-restore.ts";
import {
  loadedBuffer,
  resetProjectFilesViewTest,
} from "../components/project-files-view-test-harness.ts";

function selected(overrides: Partial<FileVersionView> = {}): FileVersionView {
  return { versionId: "version-old",
fileId: "file-main",
rootId: "r1",
path: "src/main.ts",
op: "write",
ts: "2026-08-23T10:00:00Z",
beforeAvailability: "available",
sha256: "older-sha",
sizeBytes: 6,
availability: "available",
...overrides, source: { kind: "text", before: "oldest\n", after: "older\n" } };
}

describe("file version restore", () => {
  beforeEach(resetProjectFilesViewTest);

  it("offers restore on a file's own past version, never on a comparison preview tab", () => {
    const version = selected();
    expect(versionRestoreOffered({ kind: "text" }, version)).toBe(true);
    expect(versionRestoreOffered({ kind: "text" }, null)).toBe(false);
    expect(versionRestoreOffered({ kind: "diff", diffPreview: { version, title: "Preview" } }, version)).toBe(false);
  });

  it("disables restore for drafts, unavailable bytes, and the current state", () => {
    const buffer = { ...loadedBuffer(), fileId: "file-main" };
    const history = {
      status: "ready" as const,
      gitStatus: "ready" as const,
      current: { state: "content" as const, sha256: buffer.baseSha256 ?? undefined },
      versions: [],
      commits: [],
      arrivals: [],
      gitHistoryState: "available" as const,
      trackedSince: null,
      nextVersionsCursor: null,
      nextGitCursor: null,
      loadingMore: false,
    };
    expect(versionRestoreDisabledReason({
      buffer: { ...buffer, dirty: true }, history, version: selected(),
      restoring: false,
    })).toContain("Save or discard");
    expect(versionRestoreDisabledReason({
      buffer, history, version: selected({ availability: "unavailable" }),
      restoring: false,
    })).toContain("unavailable");
    expect(versionRestoreDisabledReason({
      buffer, history, version: selected({ availability: "binary" }),
      restoring: false,
    })).toBeNull();
    expect(versionRestoreDisabledReason({
      buffer, history, version: selected({ sha256: buffer.baseSha256 }),
      restoring: false,
    })).toContain("already matches");
  });

  it("restores through the version endpoint and returns exact version Undo", async () => {
    const buffer = { ...loadedBuffer(), fileId: "file-main" };
    const restoreProjectSourceVersion = vi.fn().mockResolvedValue({
      version_id: "version-old",
      previous_version_id: "version-current",
      file_id: buffer.fileId,
      root_id: buffer.rootId,
      path: buffer.path,
      state: "content",
      sha256: "older-sha",
      changed: true,
    });
    const result = await restoreRetainedVersion({
      client: stubClient({ restoreProjectSourceVersion }),
      projectId: "project-1",
      sessionId: "session-1",
      buffer,
      history: {
        status: "ready",
        gitStatus: "ready",
        current: { state: "content", sha256: buffer.baseSha256 ?? undefined },
        versions: [],
        commits: [],
        arrivals: [],
        gitHistoryState: "available" as const,
        trackedSince: null,
        nextVersionsCursor: null,
        nextGitCursor: null,
        loadingMore: false,
      },
      versionId: "version-old",
    });

    expect(restoreProjectSourceVersion).toHaveBeenCalledWith(
      "project-1",
      "version-old",
      expect.objectContaining({
        file_id: buffer.fileId,
        root_id: buffer.rootId,
        path: buffer.path,
        base: { state: "content", sha256: buffer.baseSha256 },
      }),
      "session-1",
    );
    expect(result).toMatchObject({
      ok: true,
      undo: {
        kind: "version",
        versionId: "version-current",
        base: { state: "content", sha256: "older-sha" },
      },
    });
  });

  it("restores retained content over a deleted current file", async () => {
    const buffer = {
      ...loadedBuffer(),
      fileId: "file-main",
      baseSha256: null,
      loadError: "Source file not found",
    };
    const restoreProjectSourceVersion = vi.fn().mockResolvedValue({
      version_id: "version-old",
      file_id: buffer.fileId,
      root_id: buffer.rootId,
      path: buffer.path,
      state: "content",
      sha256: "older-sha",
      changed: true,
    });
    const result = await restoreRetainedVersion({
      client: stubClient({ restoreProjectSourceVersion }),
      projectId: "project-1",
      buffer,
      history: {
        status: "ready",
        gitStatus: "ready",
        current: { state: "absent" },
        versions: [],
        commits: [],
        arrivals: [],
        gitHistoryState: "available" as const,
        trackedSince: null,
        nextVersionsCursor: null,
        nextGitCursor: null,
        loadingMore: false,
      },
      versionId: "version-old",
    });

    expect(restoreProjectSourceVersion).toHaveBeenCalledWith(
      "project-1",
      "version-old",
      expect.objectContaining({ base: { state: "absent" } }),
      undefined,
    );
    expect(result).toMatchObject({
      ok: true,
      undo: { kind: "delete", rootId: buffer.rootId, path: buffer.path },
    });
  });

  it("restores the state a deletion removed before history loads", async () => {
    const buffer = {
      ...loadedBuffer(),
      fileId: "file-main",
      baseSha256: null,
      deleted: {
        deleted_at: "2026-09-24T10:00:00Z",
        previous: { version_id: "version-old", state: "content" as const, size_bytes: 12, availability: "available" as const },
      },
    };
    const restoreProjectSourceVersion = vi.fn().mockResolvedValue({
      version_id: "version-restored", file_id: buffer.fileId, root_id: buffer.rootId, path: buffer.path,
      state: "content", sha256: "older-sha", changed: true,
    });
    const result = await restoreRetainedVersion({
      client: stubClient({ restoreProjectSourceVersion }),
      projectId: "project-1",
      buffer,
      history: {
        status: "idle", gitStatus: "idle", current: null, versions: [], commits: [], arrivals: [],
        gitHistoryState: "not_requested" as const, trackedSince: null, nextVersionsCursor: null, nextGitCursor: null, loadingMore: false,
      },
      versionId: "version-old",
    });

    expect(restoreProjectSourceVersion).toHaveBeenCalledWith(
      "project-1",
      "version-old",
      expect.objectContaining({ base: { state: "absent" } }),
      undefined,
    );
    expect(result).toMatchObject({ ok: true, undo: { kind: "delete", rootId: buffer.rootId, path: buffer.path } });
  });
});
