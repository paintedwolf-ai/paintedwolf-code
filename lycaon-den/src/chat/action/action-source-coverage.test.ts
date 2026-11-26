import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { ActionFact } from "../../api/types.ts";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { CardAction } from "./action-merge.ts";
import { activityCoverageNotices, useActionCoverage } from "./action-source-coverage.ts";

describe("command activity coverage", () => {
  it("says nothing when every folder was compared", () => {
    expect(activityCoverageNotices([{ folder: "/work/app", coverage: "complete" }])).toEqual([]);
    expect(activityCoverageNotices()).toEqual([]);
  });

  it("names a folder that was not observed and why", () => {
    expect(activityCoverageNotices([
      { folder: "/srv/shared", coverage: "not_observed", reason: "time_budget" },
      { folder: "/", coverage: "not_observed", reason: "unconfined" },
    ])).toEqual([
      "/srv/shared was not observed: the observation ran out of time.",
      "Everything outside the project was not observed: the command ran without confinement, so it could have changed files anywhere.",
    ]);
  });

  it("lists what a partial folder skipped once", () => {
    const partial: NonNullable<ActionFact["source_coverage"]>[number] = {
      folder: "/home/me", coverage: "partial",
      boundaries: [{ path: "node_modules", reason: "scope" }, { path: "photos", reason: "walk_budget" }], omitted_boundaries: 3,
    };
    expect(activityCoverageNotices([partial, partial])).toEqual([
      "Not every folder in /home/me was compared. Skipped node_modules (outside the source scope), photos (the entry limit was reached), 3 more.",
    ]);
  });
});

function actionWithWindows(windows: string[]): CardAction {
  return {
    label: "Run command",
    status: "done",
    ran: { text: "ls", noun: "command" },
    facts: [{ kind: "source_observation", source_windows: windows }],
    row: [],
    links: [{ kind: "input" }],
  };
}

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("useActionCoverage", () => {
  it("stops re-polling a pruned command window on source_history_not_found", async () => {
    const getObservation = vi.fn().mockRejectedValue(
      new LycaonApiError("pruned", 404, "source_history_not_found"),
    );
    const client = stubClient({ getProjectSourceCommandObservation: getObservation });
    const dispose = createRoot((d) => {
      useActionCoverage(() => actionWithWindows(["win-pruned"]), {
        client,
        projectId: "proj-1",
        sessionId: "sess-1",
      });
      return d;
    });
    try {
      await vi.waitFor(() => expect(getObservation).toHaveBeenCalledTimes(1));
      await flush();

      invalidateSourceQueries({ projectId: "proj-1" });
      await flush();
      invalidateSourceQueries({ projectId: "proj-1" });
      await flush();

      expect(getObservation).toHaveBeenCalledTimes(1);
    } finally {
      dispose();
    }
  });

  it("retries transient errors on source invalidation until settled", async () => {
    const getObservation = vi.fn()
      .mockRejectedValueOnce(new LycaonApiError("temporary", 500, "internal_error"))
      .mockResolvedValueOnce({
        settling: false,
        coverage: [{ folder: "/work/app", coverage: "complete" }],
      });
    const client = stubClient({ getProjectSourceCommandObservation: getObservation });
    const dispose = createRoot((d) => {
      useActionCoverage(() => actionWithWindows(["win-retry"]), {
        client,
        projectId: "proj-1",
        sessionId: "sess-1",
      });
      return d;
    });
    try {
      await vi.waitFor(() => expect(getObservation).toHaveBeenCalledTimes(1));
      await flush();

      invalidateSourceQueries({ projectId: "proj-1" });
      await vi.waitFor(() => expect(getObservation).toHaveBeenCalledTimes(2));
      await flush();

      invalidateSourceQueries({ projectId: "proj-1" });
      await flush();
      expect(getObservation).toHaveBeenCalledTimes(2);
    } finally {
      dispose();
    }
  });
});
