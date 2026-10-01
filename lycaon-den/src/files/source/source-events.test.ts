import { afterEach, describe, expect, it, vi } from "vitest";
import type { SourceChange, SourceDirListing } from "../../api/types.ts";
import { applySourceChangesEvent, requestSourceProjectionResync } from "./source-events.ts";
import { setBufferLiveHandlers } from "../documents/buffer-live.ts";
import {
  applyFilesBufferLoad,
  applyFilesBufferDraft,
  openFilesBuffer,
  resetProjectFilesForTests,
} from "../documents/project-files-buffers.ts";
import { setSourceRefresh, SOURCE_REFRESH_IDLE_MS } from "./source-refresh.ts";
import {
  connectSourceTreeWorkspace,
  resetSourceTreeStoreForTests,
} from "../tree/source-tree-store.ts";

const WORKSPACE = "workspace-main";

function change(path: string, overrides: Partial<SourceChange> = {}): SourceChange {
  return {
    root_id: "r1",
    path,
    op: "write",
    origin: "agent",
    changed_at: new Date().toISOString(),
    ...overrides,
  };
}

function listing(dir: string, entries: SourceDirListing["entries"]): SourceDirListing {
  return {
    workspace_id: WORKSPACE,
    root_id: "r1",
    dir,
    watch_complete: true,
    entries,
  };
}

afterEach(() => {
  resetSourceTreeStoreForTests();
  resetProjectFilesForTests();
  vi.useRealTimers();
});

describe("source-events", () => {
  it("resync refreshes previews and preserves dirty buffers despite incomplete path lists", () => {
    const projectId = "resync-project";
    const keys = ["preview.bin", "draft.txt"].map((path) => {
      const key = openFilesBuffer(projectId, { intent: "permanent", rootId: "r1", rootLabel: "repo", path });
      applyFilesBufferLoad(projectId, key, {
        file_id: "", version_id: "", workspace_id: WORKSPACE, workspace_kind: "project",
        path, content: "old", over_limit: false, writable: true, binary: path.endsWith(".bin"),
        size_bytes: 3, sha256: "old",
      });
      return key;
    });
    applyFilesBufferDraft(projectId, keys[1]!, "unsaved");
    const onClean = vi.fn();
    const onDirty = vi.fn();
    const onDelete = vi.fn();
    const onConfig = vi.fn();
    setBufferLiveHandlers(projectId, {
      onCleanDiskChange: onClean, onDirtyDiskChange: onDirty,
      onForeignDeleteDirty: onDelete, onEditorConfigChange: onConfig,
    });
    applySourceChangesEvent({
      project_id: projectId, workspace_id: WORKSPACE, workspace_kind: "project",
      resync: true, changes: [change("draft.txt", { op: "delete" })],
    });
    expect(onClean.mock.calls.map(([buf]) => buf.key)).toEqual([keys[0]]);
    expect(onDirty.mock.calls.map(([buf]) => buf.key)).toEqual([keys[1]]);
    expect(onDelete).not.toHaveBeenCalled();
    expect(onConfig).toHaveBeenCalledExactlyOnceWith("r1");
    setBufferLiveHandlers(projectId, null);
  });

  it("applies both sides of a rename", async () => {
    const projectId = "proj-1";

    const connection = connectSourceTreeWorkspace({
      projectId,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) =>
        listing(dir, dir === "old" ? [{ name: "a.go", is_dir: false }] : []),
    });
    await connection.load("r1", "old");
    await connection.load("r1", "pkg");

    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: WORKSPACE,
      workspace_kind: "project",
      resync: false,
      changes: [change("pkg/a.go", {
        op: "rename",
        from_path: "old/a.go",
        origin: "user",
        is_dir: false,
      })],
    });

    expect(connection.get("r1", "old")?.listing.entries).toEqual([]);
    expect(connection.get("r1", "pkg")?.listing.entries).toEqual([
      { name: "a.go", is_dir: false },
    ]);
    connection.disconnect();
  });

  it("publishes one projection update for a large host batch", async () => {
    const projectId = "proj-burst";
    const connection = connectSourceTreeWorkspace({
      projectId,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, []),
    });
    await connection.load("r1", ".");
    const listener = vi.fn();
    connection.subscribe(listener);
    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: WORKSPACE,
      workspace_kind: "project",
      resync: false,
      changes: Array.from({ length: 51 }, (_, index) =>
        change(`file${index}.go`, { op: "create", is_dir: false })),
    });

    expect(listener).toHaveBeenCalledOnce();
    expect(connection.get("r1", ".")?.listing.entries).toHaveLength(51);
    connection.disconnect();
  });

  it("notifies open buffers while the Files surface is unmounted", () => {
    const projectId = "proj-buf";
    const key = openFilesBuffer(projectId, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "a.ts",
    });
    applyFilesBufferLoad(projectId, key, {
      file_id: "",
      version_id: "",
      workspace_id: WORKSPACE,
      workspace_kind: "project",
      path: "a.ts",
      content: "old",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 3,
      sha256: "sha-old",
    });
    const onClean = vi.fn();
    setBufferLiveHandlers(projectId, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });

    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: WORKSPACE,
      workspace_kind: "project",
      resync: false,
      changes: [change("a.ts", {
        origin: "user",
        after_sha256: "sha-new",
      })],
    });

    expect(onClean).toHaveBeenCalledOnce();
    setBufferLiveHandlers(projectId, null);
  });

  it("routes a worker overlay batch away from root projections", async () => {
    const projectId = "proj-overlay";
    const connection = connectSourceTreeWorkspace({
      projectId,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, [{ name: "pkg", is_dir: true }]),
    });
    await connection.load("r1", ".");
    const projectionListener = vi.fn();
    connection.subscribe(projectionListener);
    const sourceRefresh = vi.fn().mockResolvedValue(undefined);
    setSourceRefresh(projectId, sourceRefresh);

    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: "workspace-overlay",
      workspace_kind: "worker",
      resync: false,
      changes: [change("pkg/a.go", { worker_id: "job-1" })],
    });

    expect(projectionListener).not.toHaveBeenCalled();
    expect(sourceRefresh).not.toHaveBeenCalled();
    setSourceRefresh(projectId, null);
    connection.disconnect();
  });

  it("refreshes root-derived views once when promoted changes land", async () => {
    vi.useFakeTimers();
    const projectId = "proj-promote";
    const sourceRefresh = vi.fn().mockResolvedValue(undefined);
    setSourceRefresh(projectId, sourceRefresh);

    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: WORKSPACE,
      workspace_kind: "project",
      resync: false,
      changes: [change("pkg/a.go", {
        worker_id: "job-1",
        after_sha256: "sha-landed",
      })],
    });

    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(sourceRefresh).toHaveBeenCalledOnce();
    setSourceRefresh(projectId, null);
  });

  it("reloads watch coverage only for a resync or missed event reconciliation", async () => {
    vi.useFakeTimers();
    const refresh = vi.fn().mockResolvedValue(undefined);
    setSourceRefresh("coverage", refresh);
    const event = { project_id: "coverage", workspace_id: WORKSPACE, workspace_kind: "project" as const, changes: [], resync: false };
    applySourceChangesEvent(event);
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenLastCalledWith({ watchCoverage: false });
    applySourceChangesEvent({ ...event, resync: true });
    applySourceChangesEvent(event);
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenLastCalledWith({ watchCoverage: true });
    requestSourceProjectionResync("coverage");
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenNthCalledWith(3, { watchCoverage: true });
    setSourceRefresh("coverage", null);
  });

  it("keeps an overlay buffer live and a root buffer untouched", () => {
    const projectId = "proj-two-views";
    const rootKey = openFilesBuffer(projectId, {
      intent: "permanent", rootId: "r1", rootLabel: "repo", path: "a.ts",
    });
    const overlayKey = openFilesBuffer(projectId, {
      intent: "permanent", rootId: "r1", rootLabel: "repo", path: "a.ts", jobId: "job-1",
    });
    for (const key of [rootKey, overlayKey]) {
      applyFilesBufferLoad(projectId, key, {
        file_id: "",
        version_id: "",
        workspace_id: key === overlayKey ? "workspace-overlay" : "workspace-project",
        workspace_kind: key === overlayKey ? "worker" : "project",
        path: "a.ts", content: "old", over_limit: false, writable: true,
        binary: false, size_bytes: 3, sha256: "sha-old",
      });
    }
    const onClean = vi.fn();
    setBufferLiveHandlers(projectId, {
      onCleanDiskChange: onClean,
      onDirtyDiskChange: vi.fn(),
      onForeignDeleteDirty: vi.fn(),
    });

    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: "workspace-overlay",
      workspace_kind: "worker",
      resync: false,
      changes: [change("a.ts", {
        worker_id: "job-1",
        after_sha256: "sha-overlay",
      })],
    });

    expect(onClean).toHaveBeenCalledOnce();
    expect(onClean.mock.calls[0]?.[0]?.key).toBe(overlayKey);
    setBufferLiveHandlers(projectId, null);
  });
});
