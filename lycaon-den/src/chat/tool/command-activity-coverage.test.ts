import { describe, expect, it } from "vitest";
import type { ToolPartView } from "./tool-part-model.ts";
import { activityCoverageNotices } from "./command-activity-coverage.ts";

const call = (output: unknown): ToolPartView => ({
  id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call",
  tool: "command", kind: "command", status: "completed", args: {},
  output: typeof output === "string" ? output : JSON.stringify(output),
});

describe("command activity coverage", () => {
  it("says nothing when every folder was compared", () => {
    expect(activityCoverageNotices([call({ source_window: { id: "w", settled: true, coverage: [{ folder: "/work/app", coverage: "complete" }] } })])).toEqual([]);
    expect(activityCoverageNotices([call("plain text output")])).toEqual([]);
  });

  it("names a folder that was not observed and why", () => {
    const notices = activityCoverageNotices([call({ source_window: { coverage: [
      { folder: "/srv/shared", coverage: "not_observed", reason: "time_budget" },
      { folder: "/", coverage: "not_observed", reason: "unconfined" },
    ] } })]);
    expect(notices).toEqual([
      "/srv/shared was not observed: the observation ran out of time.",
      "Everything outside the project was not observed: the command ran without confinement, so it could have changed files anywhere.",
    ]);
  });

  it("lists what a partial folder skipped, once across calls", () => {
    const partial = { source_window: { coverage: [{
      folder: "/home/me", coverage: "partial",
      boundaries: [{ path: "node_modules", reason: "scope" }, { path: "photos", reason: "walk_budget" }], omitted_boundaries: 3,
    }] } };
    expect(activityCoverageNotices([call(partial), call(partial)])).toEqual([
      "Not every folder in /home/me was compared; skipped node_modules (outside the source scope), photos (the entry limit was reached), 3 more.",
    ]);
  });

  it("never reads a gap as no changes", () => {
    const [notice] = activityCoverageNotices([call({ source_window: { coverage: [{ folder: "/srv", coverage: "not_observed", reason: "io_error" }] } })]);
    expect(notice).not.toMatch(/no changes/i);
  });
});
