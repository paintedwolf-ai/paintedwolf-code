import { afterEach, describe, expect, it, vi } from "vitest";
import type { SourceChange, SourceDirEntry, SourceDirListing } from "../../api/types.ts";
import {
  applySourceTreeChanges,
  connectSourceTreeWorkspace,
  requestSourceTreeResync,
  resetSourceTreeStoreForTests,
} from "./source-tree-store.ts";

const PROJECT = "project-1";
const WORKSPACE = "workspace-1";

function listing(
  dir: string,
  entries: SourceDirEntry[],
  watchComplete = true,
): SourceDirListing {
  return {
    workspace_id: WORKSPACE,
    root_id: "root-1",
    dir,
    watch_complete: watchComplete,
    entries,
  };
}

function event(
  changes: Parameters<typeof applySourceTreeChanges>[0]["changes"],
  overrides: Partial<Parameters<typeof applySourceTreeChanges>[0]> = {},
) {
  return {
    project_id: PROJECT,
    workspace_id: WORKSPACE,
    workspace_kind: "project" as const,
    changes,
    resync: false,
    ...overrides,
  };
}

function change(path: string, overrides: Partial<SourceChange> = {}): SourceChange {
  return {
    root_id: "root-1",
    path,
    op: "create" as const,
    origin: "agent" as const,
    is_dir: false,
    changed_at: "2026-08-22T00:00:00Z",
    ...overrides,
  };
}

afterEach(() => {
  resetSourceTreeStoreForTests();
  vi.useRealTimers();
});

describe("source-tree-store", () => {
  it("shares a cached listing and one in-flight read across surfaces", async () => {
    let resolve!: (value: SourceDirListing) => void;
    const browse = vi.fn(() => new Promise<SourceDirListing>((done) => {
      resolve = done;
    }));
    const first = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const second = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });

    const one = first.load("root-1", ".");
    const two = second.load("root-1", ".");
    expect(browse).toHaveBeenCalledOnce();
    resolve(listing(".", [{ name: "a.ts", is_dir: false }]));

    await expect(one).resolves.toEqual(await two);
    await second.load("root-1", ".");
    expect(browse).toHaveBeenCalledOnce();
    first.disconnect();
    second.disconnect();
  });

  it("patches create, delete, and rename membership without a relist", async () => {
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, dir === "." ? [{ name: "old.ts", is_dir: false }] : []));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");

    applySourceTreeChanges(event([
      change("new.ts"),
      change("renamed.ts", {
        op: "rename",
        from_path: "old.ts",
      }),
    ]));
    expect(connection.get("root-1", ".")?.listing.entries).toEqual([
      { name: "new.ts", is_dir: false },
      { name: "renamed.ts", is_dir: false },
    ]);

    applySourceTreeChanges(event([change("new.ts", { op: "delete", is_dir: undefined })]));
    expect(connection.get("root-1", ".")?.listing.entries).toEqual([
      { name: "renamed.ts", is_dir: false },
    ]);
    expect(browse).toHaveBeenCalledOnce();
    connection.disconnect();
  });

  it("applies parent creation before checking nested batch paths", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, []),
    });
    await connection.load("root-1", ".");
    applySourceTreeChanges(event([
      change("dir", { is_dir: true }),
      change("dir/file.ts"),
    ]));
    expect(connection.get("root-1", ".")).toMatchObject({
      stale: false,
      listing: { entries: [{ name: "dir", is_dir: true }] },
    });
    connection.disconnect();
  });

  it("reports a workspace transition before validating its directory", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async () => ({
        ...listing("elsewhere", []),
        workspace_id: "workspace-2",
        root_id: "root-2",
      }),
    });

    await expect(connection.load("root-1", ".")).rejects.toMatchObject({
      expectedWorkspaceId: WORKSPACE,
      actualWorkspaceId: "workspace-2",
    });
    connection.disconnect();
  });

  it("never applies a change from another physical workspace", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, []),
    });
    await connection.load("root-1", ".");
    applySourceTreeChanges(event([change("foreign.ts")], {
      workspace_id: "workspace-2",
    }));
    expect(connection.get("root-1", ".")?.listing.entries).toEqual([]);
    connection.disconnect();
  });

  it("never applies a worker change to a project tree", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, []),
    });
    await connection.load("root-1", ".");
    applySourceTreeChanges(event([change("worker.ts")], {
      workspace_kind: "worker",
    }));
    expect(connection.get("root-1", ".")?.listing.entries).toEqual([]);
    connection.disconnect();
  });

  it("reconciles a listing with a change that raced it", async () => {
    vi.useFakeTimers();
    let firstResolve!: (value: SourceDirListing) => void;
    const browse = vi.fn()
      .mockImplementationOnce(() => new Promise<SourceDirListing>((done) => {
        firstResolve = done;
      }));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const pending = connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);
    applySourceTreeChanges(event([change("new.ts")]));
    firstResolve(listing(".", []));

    await expect(pending).resolves.toEqual(listing(".", [{ name: "new.ts", is_dir: false }]));
    expect(connection.get("root-1", ".")).toMatchObject({
      stale: false,
      listing: { entries: [{ name: "new.ts", is_dir: false }] },
    });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(browse).toHaveBeenCalledOnce();
    connection.disconnect();
  });

  it("keeps a listing that raced an unresolved change, marked for revalidation", async () => {
    vi.useFakeTimers();
    let firstResolve!: (value: SourceDirListing) => void;
    const browse = vi.fn()
      .mockImplementationOnce(() => new Promise<SourceDirListing>((done) => {
        firstResolve = done;
      }))
      .mockResolvedValue(listing(".", [{ name: "new.ts", is_dir: false }]));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const pending = connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);
    applySourceTreeChanges(event([change("new.ts", { is_dir: undefined })]));
    firstResolve(listing(".", []));

    // Raced responses keep rows visible until revalidation.
    await expect(pending).resolves.toEqual(listing(".", []));
    expect(browse).toHaveBeenCalledOnce();
    expect(connection.get("root-1", ".")?.stale).toBe(true);

    // A quiet window later the projection re-reads once and settles.
    await vi.advanceTimersByTimeAsync(400);
    expect(browse).toHaveBeenCalledTimes(2);
    expect(connection.get("root-1", ".")).toMatchObject({
      stale: false,
      listing: { entries: [{ name: "new.ts", is_dir: false }] },
    });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(browse).toHaveBeenCalledTimes(2);
    connection.disconnect();
  });

  it("keeps confirmed changes over an older in-flight response", async () => {
    vi.useFakeTimers();
    let olderResolve!: (value: SourceDirListing) => void;
    const browse = vi.fn()
      .mockResolvedValueOnce(listing(".", [
        { name: "a.md", is_dir: false },
        { name: "gone.md", is_dir: false },
      ]))
      .mockImplementationOnce(() => new Promise<SourceDirListing>((done) => {
        olderResolve = done;
      }));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);
    const pending = connection.load("root-1", ".", true);
    connection.confirm(change("a copy.md", { origin: "user" }));
    connection.confirm(change("gone.md", { op: "delete", origin: "user" }));
    olderResolve(listing(".", [
      { name: "a.md", is_dir: false },
      { name: "gone.md", is_dir: false },
    ]));
    await pending;

    // The duplicate picker reads trusted entries; they must include the copy.
    expect(connection.get("root-1", ".")).toEqual({
      stale: false,
      listing: listing(".", [
        { name: "a copy.md", is_dir: false },
        { name: "a.md", is_dir: false },
      ]),
    });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(browse).toHaveBeenCalledTimes(2);
    connection.disconnect();
  });


  it("absorbs membership churn without re-reading the directory", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) => listing(dir, []));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    for (let tick = 0; tick < 200; tick += 1) {
      applySourceTreeChanges(event([change(`gen${tick}.ts`)]));
      applySourceTreeChanges(event([
        change(`gen${tick}.ts`, { op: "delete" }),
      ]));
      await vi.advanceTimersByTimeAsync(10);
    }
    await vi.advanceTimersByTimeAsync(5_000);
    // Every change carried its kind, so the projection stayed authoritative.
    expect(browse).toHaveBeenCalledOnce();
    expect(connection.get("root-1", ".")?.stale).toBe(false);
    connection.disconnect();
  });

  it("re-reads a continuously diverging directory on a bounded cadence", async () => {
    vi.useFakeTimers();
    // Unlisted descendants force revalidation during continuous writes.
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [{ name: "Cargo.toml", is_dir: false }]));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    browse.mockClear();
    for (let tick = 0; tick < 1_000; tick += 1) {
      applySourceTreeChanges(event([change(`target/debug/o${tick}.o`)]));
      await vi.advanceTimersByTimeAsync(10);
    }

    // Ten seconds of churn: paced by the maximum wait, not by event rate.
    const duringChurn = browse.mock.calls.length;
    expect(duringChurn).toBeGreaterThan(0);
    expect(duringChurn).toBeLessThanOrEqual(6);

    // Revalidation settles after writes stop.
    await vi.advanceTimersByTimeAsync(5_000);
    expect(connection.get("root-1", ".")?.stale).toBe(false);
    expect(browse.mock.calls.length).toBeLessThanOrEqual(duringChurn + 1);
    connection.disconnect();
  });

  it("does not re-read for a write the listing already reflects", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [{ name: "build.log", is_dir: false }]));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    for (let tick = 0; tick < 50; tick += 1) {
      applySourceTreeChanges(event([change("build.log", { op: "write" })]));
      await vi.advanceTimersByTimeAsync(10);
    }
    await vi.advanceTimersByTimeAsync(5_000);
    expect(browse).toHaveBeenCalledOnce();
    expect(connection.get("root-1", ".")?.stale).toBe(false);
    connection.disconnect();
  });

  it("discards an in-flight subtree listing after its directory is deleted", async () => {
    let firstResolve!: (value: SourceDirListing) => void;
    const browse = vi.fn()
      .mockImplementationOnce(() => new Promise<SourceDirListing>((done) => {
        firstResolve = done;
      }));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const pending = connection.load("root-1", "dir");
    applySourceTreeChanges(event([
      change("dir", { op: "delete", is_dir: true }),
    ]));
    firstResolve(listing("dir", [{ name: "stale.ts", is_dir: false }]));

    await pending;
    // A deleted directory is not re-read, and its response is not cached.
    expect(connection.get("root-1", "dir")).toBeUndefined();
    expect(browse).toHaveBeenCalledOnce();
    connection.disconnect();
  });

  it("re-reads an in-flight listing after an SSE continuity gap", async () => {
    vi.useFakeTimers();
    let firstResolve!: (value: SourceDirListing) => void;
    const browse = vi.fn()
      .mockImplementationOnce(() => new Promise<SourceDirListing>((done) => {
        firstResolve = done;
      }))
      .mockResolvedValue(listing(".", [{ name: "fresh.ts", is_dir: false }]));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const pending = connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);
    requestSourceTreeResync(PROJECT);
    firstResolve(listing(".", [{ name: "stale.ts", is_dir: false }]));
    await pending;
    expect(connection.get("root-1", ".")?.stale).toBe(true);

    await vi.advanceTimersByTimeAsync(400);
    expect(browse).toHaveBeenCalledTimes(2);
    expect(connection.get("root-1", ".")).toMatchObject({
      stale: false,
      listing: { entries: [{ name: "fresh.ts", is_dir: false }] },
    });
    connection.disconnect();
  });

  it("marks loaded listings stale after an SSE continuity gap", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) => listing(dir, []),
    });
    await connection.load("root-1", ".");
    requestSourceTreeResync(PROJECT);
    expect(connection.get("root-1", ".")?.stale).toBe(true);
    connection.disconnect();
  });

  it("does not patch a listing from an incomplete resync batch", async () => {
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT,
      workspaceId: WORKSPACE,
      browse: async (_rootId, dir) =>
        listing(dir, [{ name: "old.ts", is_dir: false }]),
    });
    await connection.load("root-1", ".");
    applySourceTreeChanges(event([change("partial.ts")], { resync: true }));
    expect(connection.get("root-1", ".")).toMatchObject({
      stale: true,
      listing: { entries: [{ name: "old.ts", is_dir: false }] },
    });
    connection.disconnect();
  });

  it("polls only observed listings whose watcher coverage is incomplete", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], false));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    await vi.advanceTimersByTimeAsync(2_000);
    expect(browse).toHaveBeenCalledTimes(2);
    connection.observe([], false);
    await vi.advanceTimersByTimeAsync(4_000);
    expect(browse).toHaveBeenCalledTimes(2);
    connection.disconnect();
  });

  // Watch coverage is tracked per directory.
  it("polls the uncovered directory and leaves a covered sibling alone", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], dir !== "vendor"));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    await connection.load("root-1", "vendor");
    connection.observe(["root-1\0.", "root-1\0vendor"], true);

    browse.mockClear();
    await vi.advanceTimersByTimeAsync(6_000);
    const polled = browse.mock.calls.map(([, dir]) => dir);
    expect(polled).not.toContain(".");
    expect(polled.length).toBeGreaterThan(0);
    expect(new Set(polled)).toEqual(new Set(["vendor"]));
    connection.disconnect();
  });

  // Polling stops when watcher coverage becomes complete.
  it("stops polling a directory once it reports coverage", async () => {
    vi.useFakeTimers();
    let covered = false;
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], covered));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    await vi.advanceTimersByTimeAsync(2_000);
    expect(browse.mock.calls.length).toBeGreaterThan(1);

    covered = true;
    await vi.advanceTimersByTimeAsync(2_000);
    const settled = browse.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(browse).toHaveBeenCalledTimes(settled);
    connection.disconnect();
  });

  it("arms incomplete-coverage polling when an observed listing arrives", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], false));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    connection.observe(["root-1\0."], true);
    await connection.load("root-1", ".");

    await vi.advanceTimersByTimeAsync(2_000);
    expect(browse).toHaveBeenCalledTimes(2);
    connection.disconnect();
  });

  it("deduplicates incomplete polling across workspace connections", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], false));
    const first = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    const second = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await first.load("root-1", ".");
    first.observe(["root-1\0."], true);
    second.observe(["root-1\0."], true);

    await vi.advanceTimersByTimeAsync(2_000);
    expect(browse).toHaveBeenCalledTimes(2);
    first.disconnect();
    second.disconnect();
  });

  it("schedules polling from the latest settled listing", async () => {
    vi.useFakeTimers();
    const browse = vi.fn(async (_rootId: string, dir: string) =>
      listing(dir, [], false));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);
    await vi.advanceTimersByTimeAsync(1_500);
    await connection.load("root-1", ".", true);

    await vi.advanceTimersByTimeAsync(1_999);
    expect(browse).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(browse).toHaveBeenCalledTimes(3);
    connection.disconnect();
  });

  it("backs off after an incomplete polling failure", async () => {
    vi.useFakeTimers();
    const browse = vi.fn()
      .mockResolvedValueOnce(listing(".", [], false))
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(listing(".", [], false));
    const connection = connectSourceTreeWorkspace({
      projectId: PROJECT, workspaceId: WORKSPACE, browse,
    });
    await connection.load("root-1", ".");
    connection.observe(["root-1\0."], true);

    await vi.advanceTimersByTimeAsync(2_000);
    expect(browse).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1_999);
    expect(browse).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(browse).toHaveBeenCalledTimes(3);
    connection.disconnect();
  });
});
