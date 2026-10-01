import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import {
  workerBranchActivityLine,
  workerOverlayOpen,
  workerShowsAcceptedBranchEdits,
} from "./worker-branch-model.ts";

const base: WorkerTask = {
  id: "job-1",
  agent_type: "implementer",
  status: "complete",
  created_at: "2026-01-01T00:00:00Z",
};

describe("worker-branch-model", () => {
  it("shows parent-chat diffs only after merge acceptance", () => {
    // A read worker (no write scope) never contributes branch edits.
    expect(workerShowsAcceptedBranchEdits(base)).toBe(false);
    expect(workerOverlayOpen(base)).toBe(false);

    // A running write worker has no open overlay yet — nothing to reserve.
    const running: WorkerTask = {
      ...base,
      scope: { mode: "write", paths: ["a.go"] },
      status: "running",
    };
    expect(workerShowsAcceptedBranchEdits(running)).toBe(false);
    expect(workerOverlayOpen(running)).toBe(false);

    // Open overlays remain available to branch-aware review surfaces, not parent chat.
    const writeComplete: WorkerTask = {
      ...base,
      scope: { mode: "write", paths: ["a.go"] },
    };
    expect(workerShowsAcceptedBranchEdits(writeComplete)).toBe(false);
    expect(workerOverlayOpen(writeComplete)).toBe(true);

    const pending: WorkerTask = {
      ...base,
      merge_status: "pending",
    };
    expect(workerShowsAcceptedBranchEdits(pending)).toBe(false);
    expect(workerOverlayOpen(pending)).toBe(true);

    const applying: WorkerTask = {
      ...base,
      merge_status: "applying",
    };
    expect(workerShowsAcceptedBranchEdits(applying)).toBe(false);

    // Acceptance is terminal for parent-chat visibility.
    const merged: WorkerTask = {
      ...base,
      merge_status: "merged",
    };
    expect(workerShowsAcceptedBranchEdits(merged)).toBe(true);
    expect(workerOverlayOpen(merged)).toBe(false);
  });

  it("treats rejected and orphaned overlays as terminal and closed", () => {
    const rejected: WorkerTask = {
      ...base,
      scope: { mode: "write", paths: ["a.go"] },
      merge_status: "rejected",
    };
    const orphaned: WorkerTask = {
      ...base,
      scope: { mode: "write", paths: ["b.go"] },
      merge_status: "orphaned",
    };
    expect(workerOverlayOpen(rejected)).toBe(false);
    expect(workerOverlayOpen(orphaned)).toBe(false);
    expect(workerShowsAcceptedBranchEdits(rejected)).toBe(false);
    expect(workerShowsAcceptedBranchEdits(orphaned)).toBe(false);
  });

  it("never shows edits for a cancelled worker even with an open overlay", () => {
    const cancelled: WorkerTask = {
      ...base,
      scope: { mode: "write", paths: ["a.go"] },
      status: "canceled",
      merge_status: "pending",
    };
    expect(workerShowsAcceptedBranchEdits(cancelled)).toBe(false);
  });

  it("treats rebasing overlays as still open", () => {
    const rebasing: WorkerTask = {
      ...base,
      merge_status: "rebasing",
    };
    expect(workerOverlayOpen(rebasing)).toBe(true);
  });

  it("formats open branch activity with changed paths", () => {
    const pending: WorkerTask = {
      ...base,
      merge_status: "pending",
      result: {
        change_report: { changed_paths: ["a.ts", "b.ts"] },
      },
    };
    expect(workerBranchActivityLine(pending)).toBe(
      "Open on branch · a.ts (+1 more)",
    );
  });

  it("formats terminal branch activity lines", () => {
    expect(
      workerBranchActivityLine({ ...base, merge_status: "rejected" }),
    ).toBe("Branch closed — changes not applied");
    expect(
      workerBranchActivityLine({ ...base, merge_status: "orphaned" }),
    ).toBe("Branch disconnected — parent was closed");
    expect(
      workerBranchActivityLine({ ...base, merge_status: "rebasing" }),
    ).toBe("Updating branch after parent landed");
    expect(
      workerBranchActivityLine({ ...base, merge_status: "applying" }),
    ).toBe("Landing changes on primary…");
  });
});
