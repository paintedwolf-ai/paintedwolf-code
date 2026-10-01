import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { loaded, unloaded } from "../../store/load-state.ts";
import type { BoardView, CheckpointEvent, Project, WorkerTask } from "../../api/types.ts";
import type { GitWorkspaceStatus } from "./git-workspace-status.ts";
import { gitChangesPage, gitStatusSummary } from "../../test/git-status-fixture.ts";
import {
  BOARD_REFRESH_COALESCE_MS,
  createBoardRefreshScheduler,
  maybeRefreshGitAfterBoard,
  refreshBoard,
  refreshBoardSnapshot,
  refreshPendingCheckpointsForSession,
  refreshSessionWorkers,
} from "./board-actions.ts";
import { createAppStore } from "../../store/app-state.ts";
import {
  gitChangeBadgeCount,
  gitStatusFromBoardPulse,
  shouldSkipGitStatusFetch,
} from "./board-git-coalesce.ts";

describe("createBoardRefreshScheduler", () => {
  it.each(["queued", "in flight"])("drops obsolete %s board-to-Git work", async (phase) => {
    vi.useFakeTimers();
    let resolveBoard!: (board: BoardView) => void;
    const getBoard = vi.fn(() => new Promise<BoardView>((resolve) => { resolveBoard = resolve; }));
    const listGitRepos = vi.fn();
    const getGitStatus = vi.fn();
    const setGitStatus = vi.fn();
    const appStore = {
      state: {
        currentSession: { id: "sess-1", project_id: "proj-1" },
        sessionViewEpoch: 1,
        boardEventEpoch: 0,
        workerEventEpochs: {},
      },
      actions: { setBoardSnapshot: vi.fn(), setBoardLoad: vi.fn(), setGitStatus },
    };
    const projects = [{ id: "proj-1", roots: [{ path: "/repo", is_primary: true }] }] as Project[];
    const scheduler = createBoardRefreshScheduler(appStore as never, () => ({ getBoard, listGitRepos, getGitStatus }) as never, () => projects);
    try {
      scheduler.scheduleBoard("/repo", "sess-1", true);
      if (phase === "in flight") {
        await vi.advanceTimersByTimeAsync(BOARD_REFRESH_COALESCE_MS);
        expect(getBoard).toHaveBeenCalledOnce();
      }
      appStore.state.currentSession = { id: "sess-2", project_id: "proj-2" };
      appStore.state.sessionViewEpoch++;
      if (phase === "queued") {
        appStore.state.currentSession = { id: "sess-1", project_id: "proj-1" };
        appStore.state.sessionViewEpoch++;
      } else {
        resolveBoard({} as BoardView);
      }
      await vi.advanceTimersByTimeAsync(BOARD_REFRESH_COALESCE_MS);
      expect(getBoard).toHaveBeenCalledTimes(phase === "queued" ? 0 : 1);
      expect(listGitRepos).not.toHaveBeenCalled();
      expect(getGitStatus).not.toHaveBeenCalled();
      expect(setGitStatus).not.toHaveBeenCalled();
    } finally {
      scheduler.cancel();
      vi.useRealTimers();
    }
  });

  it.each(["sess-1", "sess-2"])("rejects an obsolete project target with session %s", async (sessionId) => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "sess-2", project_id: "proj-2" } as never);
    const getBoard = vi.fn();
    const listGitRepos = vi.fn();
    const projects = [{ id: "proj-1", roots: [{ path: "/repo", is_primary: true }] }] as Project[];
    await refreshBoard(appStore, { getBoard, listGitRepos } as never, "/repo", projects, sessionId, { includeGit: true });
    expect(getBoard).not.toHaveBeenCalled();
    expect(listGitRepos).not.toHaveBeenCalled();
  });

  it("joins identical board reads", async () => {
    const board = {} as BoardView;
    const getBoard = vi.fn().mockResolvedValue(board);
    const setBoardSnapshot = vi.fn();
    const appStore = {
      state: {
        sessionViewEpoch: 1,
        boardEventEpoch: 0,
        workerEventEpochs: { "sess-1": 0 },
        currentSession: { id: "sess-1", project_id: "proj-1" },
      },
      actions: { setBoardSnapshot, setBoardLoad: vi.fn() },
    } as never;
    const projects = [{ id: "proj-1", roots: [] }] as unknown as Project[];
    const client = { getBoard } as never;

    await Promise.all([
      refreshBoardSnapshot(appStore, client, "/repo", projects, "sess-1"),
      refreshBoardSnapshot(appStore, client, "/repo", projects, "sess-1"),
    ]);

    expect(getBoard).toHaveBeenCalledOnce();
    expect(setBoardSnapshot).toHaveBeenCalledOnce();
  });

  it("lets a new client supersede an old board read", async () => {
    let resolveOld!: (board: BoardView) => void;
    const oldClient = {
      getBoard: vi.fn(
        () =>
          new Promise<BoardView>((resolve) => {
            resolveOld = resolve;
          }),
      ),
    } as never;
    const freshBoard = { revision: 2 } as unknown as BoardView;
    const newClient = {
      getBoard: vi.fn().mockResolvedValue(freshBoard),
    } as never;
    const setBoardSnapshot = vi.fn();
    const appStore = {
      state: {
        sessionViewEpoch: 1,
        boardEventEpoch: 0,
        workerEventEpochs: { "sess-1": 0 },
        currentSession: { id: "sess-1", project_id: "proj-1" },
      },
      actions: { setBoardSnapshot, setBoardLoad: vi.fn() },
    } as never;
    const projects = [{ id: "proj-1", roots: [] }] as unknown as Project[];

    const oldRead = refreshBoardSnapshot(
      appStore,
      oldClient,
      "/repo",
      projects,
      "sess-1",
    );
    await refreshBoardSnapshot(
      appStore,
      newClient,
      "/repo",
      projects,
      "sess-1",
    );
    resolveOld({ revision: 1 } as unknown as BoardView);
    await oldRead;

    expect(setBoardSnapshot).toHaveBeenCalledTimes(1);
    expect(setBoardSnapshot).toHaveBeenCalledWith("sess-1", 1, 0, 0, freshBoard);
  });

  it("does not let an HTTP board snapshot overwrite a newer event", async () => {
    let resolveBoard!: (board: BoardView) => void;
    const client = {
      getBoard: vi.fn(() => new Promise<BoardView>((resolve) => {
        resolveBoard = resolve;
      })),
    } as never;
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const projects = [{ id: "proj-1", roots: [] }] as unknown as Project[];
    const read = refreshBoardSnapshot(appStore, client, "/repo", projects, "sess-1");
    const live = { session_id: "sess-1", now: "live" } as unknown as BoardView;
    appStore.actions.setBoard(live);
    resolveBoard({ session_id: "sess-1", now: "stale" } as unknown as BoardView);
    await read;
    expect(appStore.state.board).toStrictEqual(live);
  });

  it("does not let a worker list overwrite a newer worker event", async () => {
    let resolveWorkers!: (workers: WorkerTask[]) => void;
    const client = {
      listWorkers: vi.fn(() => new Promise<WorkerTask[]>((resolve) => {
        resolveWorkers = resolve;
      })),
      listCheckpoints: vi.fn().mockResolvedValue([]),
    } as never;
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/repo",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    appStore.actions.setWorkers([{
      id: "worker-1",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "running",
      created_at: "t",
    }]);
    const projects = [{ id: "proj-1", roots: [] }] as unknown as Project[];
    const read = refreshSessionWorkers(appStore, client, "/repo", projects, "sess-1");
    appStore.actions.updateWorker({ worker_id: "worker-1", status: "complete" });
    resolveWorkers([{
      id: "worker-1",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "running",
      created_at: "t",
    }]);
    await read;
    expect(appStore.state.workers[0]?.status).toBe("complete");
  });

  it("orders board and worker HTTP snapshots on the same worker clock", async () => {
    let resolveBoard!: (board: BoardView) => void;
    const boardClient = {
      getBoard: vi.fn(() => new Promise<BoardView>((resolve) => {
        resolveBoard = resolve;
      })),
    } as never;
    const workerClient = {
      listWorkers: vi.fn().mockResolvedValue([{
        id: "worker-1",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "complete",
        created_at: "t2",
      }]),
      listCheckpoints: vi.fn().mockResolvedValue([]),
    } as never;
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/repo",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    appStore.actions.setWorkers([{
      id: "worker-1",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "running",
      created_at: "t",
    }]);
    const projects = [{ id: "proj-1", roots: [] }] as unknown as Project[];
    const boardRead = refreshBoardSnapshot(appStore, boardClient, "/repo", projects, "sess-1");
    await refreshSessionWorkers(appStore, workerClient, "/repo", projects, "sess-1");
    resolveBoard({
      session_id: "sess-1",
      roster: [{ job_id: "worker-1", agent_type: "implementer", status: "running" }],
    } as unknown as BoardView);
    await boardRead;
    expect(appStore.state.workers[0]?.status).toBe("complete");
    expect(appStore.state.boardLoad.state).toBe("error");
  });

  it("does not let a checkpoint list overwrite a newer checkpoint event", async () => {
    let resolveCheckpoints!: (checkpoints: CheckpointEvent[]) => void;
    const client = {
      listCheckpoints: vi.fn(() => new Promise<CheckpointEvent[]>((resolve) => {
        resolveCheckpoints = resolve;
      })),
    } as never;
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const read = refreshPendingCheckpointsForSession(appStore, client, "sess-1");
    const live: CheckpointEvent = {
      id: "live-checkpoint",
      session_id: "sess-1",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t2",
    };
    appStore.actions.mergeCheckpoint(live);
    resolveCheckpoints([{
      id: "stale-checkpoint",
      session_id: "sess-1",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t1",
    }]);
    await read;
    expect(appStore.state.pendingCheckpoints.map((checkpoint) => checkpoint.checkpointId)).toEqual([
      "live-checkpoint",
    ]);
  });

  it("debounces board refresh and skips git by default", async () => {
    vi.useFakeTimers();
    const getBoard = vi.fn(async () => ({}));
    const setBoardSnapshot = vi.fn();
    const appStore = {
      state: {
        sessionViewEpoch: 1,
        boardEventEpoch: 0,
        workerEventEpochs: { "sess-1": 0 },
        currentSession: { id: "sess-1", project_id: "proj-1" },
      },
      actions: { setBoardSnapshot, setBoardLoad: vi.fn() },
    } as never;
    const client = { getBoard } as never;
    const projects = [
      {
        id: "proj-1",
        roots: [{ path: "/repo", is_primary: true }],
      },
    ] as Project[];
    const scheduler = createBoardRefreshScheduler(
      appStore,
      () => client,
      () => projects,
    );

    scheduler.scheduleBoard("/repo", "sess-1");
    scheduler.scheduleBoard("/repo", "sess-1");
    await vi.advanceTimersByTimeAsync(BOARD_REFRESH_COALESCE_MS);

    expect(getBoard).toHaveBeenCalledTimes(1);
    expect(setBoardSnapshot).toHaveBeenCalledTimes(1);
    scheduler.cancel();
    vi.useRealTimers();
  });

  it("includeGit skips getGitStatus when board pulse already matches store", async () => {
    vi.useFakeTimers();
    const pulseBoard = {
      git: {
        available: true,
        repo_id: "rtest",
        label: "app",
        branch: "main",
        dirty: true,
        staged_count: 1,
        unstaged_count: 1,
        others: [],
        others_truncated: 0,
      },
    } as unknown as BoardView;
    const prevGit: GitWorkspaceStatus = {
      available: true,
      repo_id: "rtest",
      root_ids: ["root-1"],
      branch: "main",
      ahead: 0,
      behind: 0,
      dirty: true,
      staged_count: 1,
      unstaged_count: 1,
      changed_count: 1,
      files: [{ path: "a.go", status: " M", root_id: "root-1", root_relative_path: "a.go" }],
    };
    const getBoard = vi
      .fn()
      .mockResolvedValueOnce(pulseBoard)
      .mockResolvedValueOnce({});
    const listGitRepos = vi.fn(async () => ({ repos: [], active_repo_id: "rtest" }));
    const getGitStatus = vi.fn(async () => gitStatusSummary(prevGit));
    const setBoardSnapshot = vi.fn((_sid: string, _epoch: number, _boardEpoch: number, _workerEpoch: number, board: BoardView) => {
      appStore.state.board = board;
    });
    const setGitStatus = vi.fn();
    const appStore = {
      state: {
        currentSession: { id: "sess-1", project_id: "proj-1" },
        sessionViewEpoch: 1,
        boardEventEpoch: 0,
        workerEventEpochs: { "sess-1": 0 },
        board: undefined as BoardView | undefined,
        gitStatus: loaded(prevGit),
      },
      actions: { setBoardSnapshot, setGitStatus, setBoardLoad: vi.fn() },
    };
    const client = { getBoard, listGitRepos, getGitStatus } as never;
    const projects = [
      {
        id: "proj-1",
        roots: [{ path: "/repo", is_primary: true }],
      },
    ] as Project[];

    const scheduler = createBoardRefreshScheduler(
      appStore as never,
      () => client,
      () => projects,
    );
    scheduler.scheduleBoard("/repo", "sess-1", true);
    await vi.advanceTimersByTimeAsync(BOARD_REFRESH_COALESCE_MS);

    expect(getBoard).toHaveBeenCalledTimes(1);
    expect(getGitStatus).not.toHaveBeenCalled();

    scheduler.scheduleBoard("/repo", "sess-1");
    await vi.advanceTimersByTimeAsync(BOARD_REFRESH_COALESCE_MS);

    expect(getBoard).toHaveBeenCalledTimes(2);
    expect(getGitStatus).not.toHaveBeenCalled();
    scheduler.cancel();
    vi.useRealTimers();
  });
});

describe("board+git coalesce helpers", () => {
  it("skips duplicate status fetch when pulse matches", () => {
    const pulse = {
      available: true,
      repo_id: "rtest",
      label: "app",
      branch: "main",
      dirty: false,
      staged_count: 0,
      unstaged_count: 0,
      others: [],
      others_truncated: 0,
    };
    const prev: GitWorkspaceStatus = {
      available: true,
      repo_id: "rtest",
      root_ids: ["root-1"],
      branch: "main",
      ahead: 0,
      behind: 0,
      dirty: false,
      staged_count: 0,
      unstaged_count: 0,
      changed_count: 0,
      files: [],
    };
    expect(shouldSkipGitStatusFetch(pulse, prev)).toBe(true);
  });

  it("keeps the previous file list when pulse counts change", () => {
    const files = [
      {
        path: "a.go",
        status: "M ",
        root_id: "root-1",
        root_relative_path: "a.go",
      },
    ];
    const prev: GitWorkspaceStatus = {
      available: true,
      repo_id: "rtest",
      root_ids: ["root-1"],
      branch: "main",
      ahead: 0,
      behind: 0,
      dirty: true,
      staged_count: 1,
      unstaged_count: 0,
      changed_count: 1,
      files,
    };
    const mapped = gitStatusFromBoardPulse(
      {
        available: true,
        repo_id: "rtest",
        label: "app",
        branch: "main",
        dirty: true,
        staged_count: 2,
        unstaged_count: 0,
        others: [],
        others_truncated: 0,
      },
      prev,
    );
    expect(mapped.files).toEqual(files);
    expect(mapped.staged_count).toBe(2);
  });

  it("badge falls back to staged+unstaged when file list is empty", () => {
    expect(
      gitChangeBadgeCount({
        available: true,
        repo_id: "rtest",
        root_ids: ["root-1"],
        ahead: 0,
        behind: 0,
        dirty: true,
        staged_count: 2,
        unstaged_count: 3,
        changed_count: 5,
        files: [],
      }),
    ).toBe(5);
  });

  it("badge sums every available repository when the set is loaded", () => {
    expect(
      gitChangeBadgeCount(
        {
          available: true,
          repo_id: "ra",
          root_ids: ["root-1"],
          ahead: 0,
          behind: 0,
          dirty: true,
          staged_count: 1,
          unstaged_count: 0,
          changed_count: 1,
          files: [],
        },
        [
          {
            repo_id: "ra",
            label: "a",
            root_ids: ["root-1"],
            available: true,
            ahead: 0,
            behind: 0,
            dirty: true,
            staged_count: 1,
            unstaged_count: 2,
            changed_count: 3,
          },
          {
            repo_id: "rb",
            label: "b",
            root_ids: ["root-2"],
            available: true,
            ahead: 0,
            behind: 0,
            dirty: true,
            staged_count: 4,
            unstaged_count: 0,
            changed_count: 4,
          },
        ],
      ),
    ).toBe(7);
  });

  it.each([false, true])("board Git refresh preserves chat scope with a cached repository: %s", async (cachedRepo) => {
    const pulseBoard = {
      git: {
        available: true,
        repo_id: "rtest",
        label: "app",
        branch: "main",
        dirty: true,
        staged_count: 1,
        unstaged_count: 0,
        others: [],
        others_truncated: 0,
      },
    } as unknown as BoardView;
    const fetched: GitWorkspaceStatus = {
      available: true,
      repo_id: "rtest",
      root_ids: ["root-1"],
      branch: "main",
      ahead: 0,
      behind: 0,
      dirty: true,
      staged_count: 1,
      unstaged_count: 0,
      changed_count: 1,
      files: [{ path: "a.go", status: "M ", root_id: "root-1", root_relative_path: "a.go" }],
    };
    const listGitRepos = vi.fn(async () => ({ repos: [], active_repo_id: "rtest" }));
    const getGitStatus = vi.fn(async () => gitStatusSummary(fetched));
    const listGitChanges = vi.fn(async () => gitChangesPage(fetched));
    const setGitStatus = vi.fn();
    const appStore = {
      state: {
        currentSession: { id: "sess-1", project_id: "proj-1" },
        gitRepos: cachedRepo ? [{ repo_id: "rtest", root_ids: ["root-1"], available: true }] : [],
        gitActiveRepoId: "rtest",
        board: pulseBoard,
        gitStatus: unloaded(),
      },
      actions: { setGitStatus },
    } as never;
    const outcome = await maybeRefreshGitAfterBoard(
      appStore,
      { listGitRepos, getGitStatus, listGitChanges } as never,
      "/repo",
      [{ id: "proj-1", roots: [{ path: "/repo", is_primary: true }] }] as Project[],
    );
    expect(outcome).toBe("fetched");
    expect(getGitStatus).toHaveBeenCalledOnce();
    expect(getGitStatus).toHaveBeenCalledWith("proj-1", "rtest", "sess-1");
  });
});


describe("foreground list request ordering", () => {
  it("does not let a background worker follow-up supersede the foreground refresh", async () => {
    const appStore = store();
    const response = pending<WorkerTask[]>();
    const foreground = refreshSessionWorkers(appStore, stubClient({ listWorkers: () => response.promise, listCheckpoints: async () => [] }), "/project", [], "s1");
    await refreshSessionWorkers(appStore, stubClient({ listWorkers: async () => [], listCheckpoints: async () => [] }), "/project", [], "background");
    response.resolve([{ id: "foreground", parent_session_id: "s1", agent_type: "implementer", status: "running", created_at: "t" }]);
    await foreground;
    expect(appStore.state.workers.map((worker) => worker.id)).toEqual(["foreground"]);
  });

  it("does not let a background checkpoint follow-up supersede the foreground refresh", async () => {
    const appStore = store();
    const response = pending<CheckpointEvent[]>();
    const foreground = refreshPendingCheckpointsForSession(appStore, stubClient({ listCheckpoints: () => response.promise }), "s1");
    await refreshPendingCheckpointsForSession(appStore, stubClient({ listCheckpoints: async () => [] }), "background");
    response.resolve([{ id: "foreground", session_id: "s1", kind: "tool_approval", status: "pending", issued_at: "t" }]);
    await foreground;
    expect(appStore.state.pendingCheckpoints.map((checkpoint) => checkpoint.checkpointId)).toEqual(["foreground"]);
  });

  function store() {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    return appStore;
  }

  function pending<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((finish) => { resolve = finish; });
    return { promise, resolve };
  }

  it.each([true, false])("keeps the latest checkpoint request when older finishes first: %s", async (olderFirst) => {
    const appStore = store();
    const older = pending<CheckpointEvent[]>();
    const newer = pending<CheckpointEvent[]>();
    const oldRead = refreshPendingCheckpointsForSession(appStore, stubClient({ listCheckpoints: () => older.promise }), "s1");
    const newRead = refreshPendingCheckpointsForSession(appStore, stubClient({ listCheckpoints: () => newer.promise }), "s1");
    const oldRows: CheckpointEvent[] = [{ id: "old", session_id: "s1", kind: "tool_approval", status: "pending", issued_at: "t" }];
    const newRows: CheckpointEvent[] = [{ ...oldRows[0]!, id: "new" }];
    if (olderFirst) {
      older.resolve(oldRows);
      await oldRead;
      expect(appStore.state.pendingCheckpoints).toEqual([]);
      newer.resolve(newRows);
    } else {
      newer.resolve(newRows);
      await newRead;
      older.resolve(oldRows);
    }
    await Promise.all([oldRead, newRead]);
    expect(appStore.state.pendingCheckpoints.map((row) => row.checkpointId)).toEqual(["new"]);
  });

  it.each([true, false])("keeps the latest worker request when older finishes first: %s", async (olderFirst) => {
    const appStore = store();
    const older = pending<WorkerTask[]>();
    const newer = pending<WorkerTask[]>();
    const oldRead = refreshSessionWorkers(appStore, stubClient({ listWorkers: () => older.promise, listCheckpoints: async () => [] }), "/project", [], "s1");
    const newRead = refreshSessionWorkers(appStore, stubClient({ listWorkers: () => newer.promise, listCheckpoints: async () => [] }), "/project", [], "s1");
    const oldRows: WorkerTask[] = [{ id: "old", parent_session_id: "s1", agent_type: "implementer", status: "running", created_at: "t" }];
    const newRows: WorkerTask[] = [{ ...oldRows[0]!, id: "new" }];
    if (olderFirst) {
      older.resolve(oldRows);
      await oldRead;
      expect(appStore.state.workers).toEqual([]);
      newer.resolve(newRows);
    } else {
      newer.resolve(newRows);
      await newRead;
      older.resolve(oldRows);
    }
    await Promise.all([oldRead, newRead]);
    expect(appStore.state.workers.map((row) => row.id)).toEqual(["new"]);
  });
});
