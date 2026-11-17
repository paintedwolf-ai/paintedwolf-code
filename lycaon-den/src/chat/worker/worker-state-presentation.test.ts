import { describe, expect, it } from "vitest";
import type { BoardWorker, WorkerTask } from "../../api/types.ts";
import {
  workerDispositionPresentation,
  workerFactActivityLine,
  workerStateBadgeLabel,
  workerUnreadPresentation,
} from "./worker-state-presentation.ts";

function worker(overrides: Partial<WorkerTask> = {}): WorkerTask {
  return {
    id: "job-1",
    agent_type: "implementer",
    status: "complete",
    created_at: "2026-11-15T01:00:00Z",
    ...overrides,
  };
}

function boardWorker(overrides: Partial<BoardWorker> = {}): BoardWorker {
  return {
    worker_id: "rec-1",
    name: "auth-impl",
    agent_type: "implementer",
    state: "idle",
    round: 1,
    unread_message_count: 0,
    ...overrides,
  };
}

describe("worker-state-presentation", () => {
  it("projects all worker states and badge labels without treating change ops as execution", () => {
    expect(workerStateBadgeLabel(undefined, undefined)).toBeUndefined();
    expect(workerStateBadgeLabel(worker({ status: "failed" }))).toBe("Failed");
    expect(workerStateBadgeLabel(worker({ status: "canceled" }))).toBeUndefined();
    expect(workerStateBadgeLabel(worker({ status: "running" }))).toBe("Working");
    expect(workerStateBadgeLabel(worker({ status: "waiting" }))).toBe("Parked");
    expect(
      workerStateBadgeLabel(
        worker({
          status: "pending",
          dependencies: [{ worker_id: "w0", state: "waiting" }],
        }),
      ),
    ).toBe("Waiting");
    expect(workerStateBadgeLabel(worker({ status: "pending" }))).toBe("Queued");
    expect(
      workerStateBadgeLabel(worker({ result: { status: "needs_decision", summary: "Pick one" } })),
    ).toBe("Asking");
    expect(workerStateBadgeLabel(worker({ result_disposition: "unaccounted" }))).toBe("Delivered");
    expect(workerStateBadgeLabel(worker({ result_disposition: "landed" }))).toBe("Idle");
    expect(workerStateBadgeLabel(worker({ result_disposition: "rejected" }))).toBe("Idle");
    expect(workerStateBadgeLabel(worker({ result_disposition: "shelved" }))).toBe("Idle");

    expect(
      workerStateBadgeLabel(worker({ status: "running" }), undefined, {
        jobId: "job-1",
        childSessionId: "child-1",
        checkpointId: "cp-1",
        kind: "tool_approval",
        commandSummary: "cargo test",
      }),
    ).toBe("Waiting for approval");
    expect(workerStateBadgeLabel(worker({ status: "waiting", parked_reason: "dev server" }))).toBe(
      "Parked",
    );
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "planned" }))).toBe("Planned");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "waiting" }))).toBe("Waiting");
    expect(
      workerStateBadgeLabel(undefined, boardWorker({ state: "working", reason: "queued" })),
    ).toBe("Queued");
    expect(workerStateBadgeLabel(worker({ status: "running" }))).toBe("Working");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "asking" }))).toBe("Asking");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "delivered" }))).toBe("Delivered");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "idle" }))).toBe("Idle");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "failed" }))).toBe("Failed");
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "recovering" }))).toBe(
      "Recovering",
    );
    expect(workerStateBadgeLabel(undefined, boardWorker({ state: "closed" }))).toBe("Closed");
    expect(workerStateBadgeLabel(worker({ status: "canceled" }))).toBeUndefined();
  });

  it("separates result disposition badges from unread worker message counts", () => {
    expect(workerDispositionPresentation(worker())).toBeUndefined();
    expect(workerDispositionPresentation(worker({ result_disposition: "unaccounted" }))).toEqual({
      disposition: "unaccounted",
      label: "Not accounted for",
    });
    expect(workerDispositionPresentation(worker({ result_disposition: "accounted" }))).toEqual({
      disposition: "accounted",
      label: "Accounted for",
    });
    expect(workerDispositionPresentation(worker({ result_disposition: "open" }))).toEqual({
      disposition: "open",
      label: "Open changes",
    });
    expect(workerDispositionPresentation(worker({ result_disposition: "landed" }))).toEqual({
      disposition: "landed",
      label: "Landed",
    });
    expect(workerDispositionPresentation(worker({ result_disposition: "rejected" }))).toEqual({
      disposition: "rejected",
      label: "Rejected",
    });
    expect(
      workerDispositionPresentation(
        worker({ result_disposition: "shelved", shelve_reason: "after auth migration" }),
      ),
    ).toEqual({
      disposition: "shelved",
      label: "Shelved · after auth migration",
      shelveReason: "after auth migration",
    });

    expect(workerUnreadPresentation(boardWorker({ unread_message_count: 0 }))).toBeUndefined();
    expect(workerUnreadPresentation(boardWorker({ unread_message_count: 1 }))).toEqual({
      count: 1,
      label: "1 unread message",
    });
    expect(workerUnreadPresentation(boardWorker({ unread_message_count: 3 }))).toEqual({
      count: 3,
      label: "3 unread messages",
    });
  });

  it("renders activity lines for approval, recovery, asking, parked, failure, round, shelve, and workspace reset", () => {
    expect(
      workerFactActivityLine(worker(), undefined, {
        jobId: "job-1",
        childSessionId: "child-1",
        checkpointId: "cp-1",
        kind: "tool_approval",
        commandSummary: "rm -rf build",
      }),
    ).toBe("Waiting for approval · rm -rf build");
    expect(workerFactActivityLine(undefined, boardWorker({ state: "recovering" }))).toBe(
      "Recovering interrupted workspace settlement",
    );
    expect(
      workerFactActivityLine(
        undefined,
        boardWorker({ state: "asking", question: "Should refresh rotate keys?" }),
      ),
    ).toBe("Asking: Should refresh rotate keys?");
    expect(
      workerFactActivityLine(worker({ status: "waiting", parked_reason: "waiting on port 5432" })),
    ).toBe("Parked · waiting on port 5432");
    expect(
      workerFactActivityLine(
        worker({
          status: "failed",
          failure: {
            code: "WORKSPACE_DISK_FULL",
            title: "Workspace disk full",
            message: "Workspace disk is full",
          },
        }),
      ),
    ).toBe("Workspace disk is full");
    expect(workerFactActivityLine(undefined, boardWorker({ state: "waiting" }))).toBe(
      "Waiting on prerequisites",
    );
    expect(
      workerFactActivityLine(
        worker({ status: "running", round: 2 }),
        boardWorker({ state: "working", round: 2, about: "Tightening token validation" }),
      ),
    ).toBe("Round 2 · Tightening token validation");
    expect(
      workerFactActivityLine(
        worker({
          result_disposition: "shelved",
          shelve_reason: "defer until schema lands",
          result: { summary: "Prepared migration" },
        }),
      ),
    ).toBe("Prepared migration · Shelved — defer until schema lands");
    expect(
      workerFactActivityLine(undefined, boardWorker({ state: "idle", workspace_state: "refreshed" })),
    ).toBe("Workspace refreshed");
    expect(
      workerFactActivityLine(undefined, boardWorker({ state: "idle", workspace_state: "reset" })),
    ).toBe("Workspace reset");
  });
});
