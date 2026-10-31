import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../api/client.ts";
import type {
  BackgroundProcessStopResult,
  BackgroundProcessTerminationReason,
  ProjectBackgroundProcess,
  ProjectBackgroundProcessListResponse,
} from "../api/types.ts";
import { formatBackgroundTerminationReason } from "./background-termination-reason.ts";
import {
  applyProjectBackgroundEvent,
  applyProjectBackgroundSessionEvent,
  applyProjectBackgroundSnapshot,
  patchProjectBackgroundSession,
  projectBackgroundFor,
  resetProjectBackgroundForTests,
  resyncProjectBackground,
  retainProjectBackgroundProject,
  runningBackgroundCountFor,
  stopProjectBackgroundCommand,
} from "./project-background-store.ts";

function makeProcess(
  overrides: Partial<ProjectBackgroundProcess> = {},
): ProjectBackgroundProcess {
  return {
    process_id: "bg_1",
    project_id: "proj_1",
    command: "bun run dev",
    working_dir: "app",
    ports: ["127.0.0.1:5173"],
    running: true,
    started_at: "2026-04-20T12:00:00Z",
    session_id: "sess_1",
    session_title: "Frontend server",
    session_archived: false,
    ...overrides,
  };
}

describe("formatBackgroundTerminationReason", () => {
  it("maps every host termination reason to plain words and falls back to Ended", () => {
    const cases: Array<[BackgroundProcessTerminationReason, string]> = [
      ["stopped_by_user", "Stopped by you"],
      ["stopped_by_agent", "Stopped by the agent"],
      ["root_detached", "Stopped when the folder was removed"],
      ["chat_deleted", "Stopped when the chat was deleted"],
      ["project_deleted", "Stopped when the project was deleted"],
      ["app_quit", "Ended when Painted Wolf Code quit"],
      ["worker_completed", "Stopped when the worker finished"],
      ["worker_cancelled", "Stopped when the worker was cancelled"],
      ["timed_out", "Timed out"],
      ["exited", "Exited"],
    ];
    for (const [reason, expected] of cases) {
      expect(formatBackgroundTerminationReason(reason)).toBe(expected);
    }
    expect(formatBackgroundTerminationReason(undefined)).toBe("Ended");
  });
});

describe("project-background-store", () => {
  beforeEach(() => {
    resetProjectBackgroundForTests();
  });

  it("reconciles snapshots and live events by revision watermark", () => {
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 2,
      processes: [makeProcess({ process_id: "bg_1", ports: [] })],
    });

    expect(runningBackgroundCountFor("proj_1")).toBe(1);
    expect(projectBackgroundFor("proj_1").hydrated).toBe(true);

    // Stale event at or below floor is ignored.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ended",
      revision: 2,
      running: false,
      process: makeProcess({
        process_id: "bg_1",
        running: false,
        ended_at: "2026-04-20T12:01:00Z",
        termination_reason: "exited",
      }),
    });
    expect(runningBackgroundCountFor("proj_1")).toBe(1);

    // Newer ports_changed event updates the row.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ports_changed",
      revision: 3,
      running: true,
      ports: ["127.0.0.1:3000", "127.0.0.1:3001"],
      process: makeProcess({
        process_id: "bg_1",
        ports: ["127.0.0.1:3000", "127.0.0.1:3001"],
      }),
    });
    expect(
      projectBackgroundFor("proj_1").runningGroups[0]?.processes[0]?.ports,
    ).toEqual(["127.0.0.1:3000", "127.0.0.1:3001"]);

    // Newer ended event moves the row to recentlyEnded.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ended",
      revision: 4,
      running: false,
      termination_reason: "stopped_by_agent",
      process: makeProcess({
        process_id: "bg_1",
        running: false,
        ports: [],
        ended_at: "2026-04-20T12:02:00Z",
        termination_reason: "stopped_by_agent",
      }),
    });
    expect(runningBackgroundCountFor("proj_1")).toBe(0);
    expect(projectBackgroundFor("proj_1").recentlyEnded).toHaveLength(1);
    expect(
      projectBackgroundFor("proj_1").recentlyEnded[0]?.termination_reason,
    ).toBe("stopped_by_agent");
  });

  it("keeps pending-stop state until the end event arrives, even after the stop request resolves", async () => {
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [makeProcess({ process_id: "bg_1" })],
    });

    let resolveStop!: (info: BackgroundProcessStopResult) => void;
    const client = {
      stopProjectBackgroundProcess: vi.fn(
        () =>
          new Promise<BackgroundProcessStopResult>((resolve) => {
            resolveStop = resolve;
          }),
      ),
    } as unknown as LycaonClient;

    const stopPromise = stopProjectBackgroundCommand(client, "proj_1", "bg_1");
    expect(projectBackgroundFor("proj_1").stoppingIds.has("bg_1")).toBe(true);
    expect(projectBackgroundFor("proj_1").runningCount).toBe(1);
    expect(projectBackgroundFor("proj_1").recentlyEnded).toHaveLength(0);

    // Stop HTTP request resolves while process is still exiting (running: true).
    resolveStop({
      process_id: "bg_1",
      stop_requested: true,
      running: true,
    });
    await stopPromise;

    // Must NOT move to Recently ended merely because the stop request was accepted.
    expect(projectBackgroundFor("proj_1").stoppingIds.has("bg_1")).toBe(true);
    expect(projectBackgroundFor("proj_1").runningCount).toBe(1);
    expect(projectBackgroundFor("proj_1").recentlyEnded).toHaveLength(0);

    // Host emits ended event with stopped_by_user.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ended",
      revision: 2,
      running: false,
      termination_reason: "stopped_by_user",
      process: makeProcess({
        process_id: "bg_1",
        running: false,
        ports: [],
        ended_at: "2026-04-20T12:03:00Z",
        termination_reason: "stopped_by_user",
      }),
    });

    expect(projectBackgroundFor("proj_1").stoppingIds.has("bg_1")).toBe(false);
    expect(projectBackgroundFor("proj_1").runningCount).toBe(0);
    expect(projectBackgroundFor("proj_1").recentlyEnded).toHaveLength(1);
    expect(
      projectBackgroundFor("proj_1").recentlyEnded[0]?.termination_reason,
    ).toBe("stopped_by_user");
  });

  it("clears pending state, records the error, and allows retry when stop rejects", async () => {
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [makeProcess({ process_id: "bg_1" })],
    });

    const stopFn = vi
      .fn<() => Promise<BackgroundProcessStopResult>>()
      .mockRejectedValueOnce(new Error("Process signal denied"))
      .mockResolvedValueOnce({
        process_id: "bg_1",
        stop_requested: true,
        running: true,
      });
    const client = {
      stopProjectBackgroundProcess: stopFn,
    } as unknown as LycaonClient;

    const ok1 = await stopProjectBackgroundCommand(client, "proj_1", "bg_1");
    expect(ok1).toBe(false);
    expect(projectBackgroundFor("proj_1").stoppingIds.has("bg_1")).toBe(false);
    expect(projectBackgroundFor("proj_1").stopErrors.get("bg_1")).toBe(
      "Process signal denied",
    );

    // Retry succeeds and clears the error.
    const ok2 = await stopProjectBackgroundCommand(client, "proj_1", "bg_1");
    expect(ok2).toBe(true);
    expect(projectBackgroundFor("proj_1").stopErrors.has("bg_1")).toBe(false);
    expect(projectBackgroundFor("proj_1").stoppingIds.has("bg_1")).toBe(true);
  });

  it("discards late responses for a previous project when switching projects", async () => {
    let resolveProj1!: (res: ProjectBackgroundProcessListResponse) => void;
    const client = {
      listProjectBackgroundProcesses: vi.fn((projectId: string) => {
        if (projectId === "proj_1") {
          return new Promise<ProjectBackgroundProcessListResponse>(
            (resolve) => {
              resolveProj1 = resolve;
            },
          );
        }
        return Promise.resolve({
          revision: 1,
          processes: [
            makeProcess({
              process_id: "bg_proj_2",
              project_id: "proj_2",
              command: "cargo watch",
              session_id: "sess_2",
              session_title: "Rust chat",
            }),
          ],
        });
      }),
    } as unknown as LycaonClient;

    const p1 = resyncProjectBackground(client, "proj_1");
    retainProjectBackgroundProject("proj_2");
    await resyncProjectBackground(client, "proj_2");

    resolveProj1({
      revision: 5,
      processes: [makeProcess({ process_id: "bg_proj_1", command: "npm start" })],
    });
    await p1;

    expect(projectBackgroundFor("proj_1").hydrated).toBe(false);
    expect(projectBackgroundFor("proj_1").runningGroups).toHaveLength(0);
    expect(
      projectBackgroundFor("proj_2").runningGroups[0]?.processes[0]?.command,
    ).toBe("cargo watch");
  });

  it("updates owning chat title and archived flag live on session events and patches", () => {
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [
        makeProcess({
          process_id: "bg_1",
          session_id: "sess_1",
          session_title: "Initial title",
          session_archived: false,
        }),
      ],
    });

    patchProjectBackgroundSession("proj_1", "sess_1", {
      session_archived: true,
    });
    expect(
      projectBackgroundFor("proj_1").runningGroups[0]?.sessionArchived,
    ).toBe(true);

    applyProjectBackgroundSessionEvent({
      id: "sess_1",
      project_id: "proj_1",
      action: "updated",
      status: "idle",
      title: "Renamed chat",
      archived: false,
    });
    expect(projectBackgroundFor("proj_1").runningGroups[0]?.sessionTitle).toBe(
      "Renamed chat",
    );
    expect(
      projectBackgroundFor("proj_1").runningGroups[0]?.sessionArchived,
    ).toBe(false);
  });
});
