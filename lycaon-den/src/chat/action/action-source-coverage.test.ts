import { describe, expect, it } from "vitest";
import type { ActionFact } from "../../api/types.ts";
import { activityCoverageNotices } from "./action-source-coverage.ts";

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
