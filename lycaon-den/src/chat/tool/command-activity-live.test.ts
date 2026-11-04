import { beforeEach, expect, it } from "vitest";
import type { ActivityEvent, CommandProgramReceipt } from "../../api/types.ts";
import { clearCommandActivities, liveCommandProgram, observeCommandActivity } from "./command-activity-live.ts";

function event(revision: number, status: "active" | "done" = "active"): ActivityEvent {
 return { session_id: "activity-live-test", tool_call_id: "call", tool_name: "command", activity_id: "lease", kind: "running_tool", status, started_at: "2026-10-30T10:00:00Z", progress: { phase: "executing", files_searched: 0, files_skipped: 0, matches: 0, command_program: { id: "program", activity_revision: revision, state: "running" } as CommandProgramReceipt } };
}
beforeEach(() => clearCommandActivities("activity-live-test"));
it("keeps terminal progress until the canonical result takes over", () => {
 observeCommandActivity(event(2,"done"));
 expect(liveCommandProgram("activity-live-test","call")?.activity_revision).toBe(2);
});
it("ignores stale live snapshots", () => {
 observeCommandActivity(event(9)); observeCommandActivity(event(3));
 expect(liveCommandProgram("activity-live-test","call")?.activity_revision).toBe(9);
 expect(liveCommandProgram("other","call")).toBeUndefined();
 clearCommandActivities("activity-live-test");
 expect(liveCommandProgram("activity-live-test","call")).toBeUndefined();
});
it("ignores unrelated activity leases", () => {
 observeCommandActivity({ ...event(1), progress: undefined });
 expect(liveCommandProgram("activity-live-test","call")).toBeUndefined();
});
