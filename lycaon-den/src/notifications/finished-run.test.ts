import { describe, expect, it } from "vitest";
import type { AttentionRow, SessionBootstrap } from "../api/types.ts";
import { finishedRun } from "./finished-run.ts";

const attention: AttentionRow = { session_id: "background", project_id: "other-project", class: "finished", reason: "turn_finished", since_at: "2026-10-18T00:00:02Z" };
function snapshot(): SessionBootstrap {
  return { session: { id: "background", status: "idle" }, turn_clock: { session_id: "background", active_ms: 5000, work_ms: 5000, running: false, opening_message_id: "prompt-1", settled_at: "2026-10-18T00:00:01Z" } } as SessionBootstrap;
}
describe("finished notification clock", () => {
  it("uses the background session's settled host identity", () => {
    expect(finishedRun(snapshot(), attention)).toEqual({ activeMs: 5000, identity: "prompt-1:2026-10-18T00:00:01Z" });
  });
  it("refuses busy, queued, archived, mismatched and unreadable clock state", () => {
    for (const change of ["busy", "queued", "archived", "wrong-session", "wrong-clock", "running", "no-opening", "no-settlement"] as const) {
      const value = snapshot();
      if (change === "busy") value.session.status = "busy";
      if (change === "queued") value.session.prompt_pending = true;
      if (change === "archived") value.session.archived_at = "2026-10-18T00:00:03Z";
      if (change === "wrong-session") value.session.id = "other";
      if (change === "wrong-clock") value.turn_clock.session_id = "other";
      if (change === "running") value.turn_clock.running = true;
      if (change === "no-opening") delete value.turn_clock.opening_message_id;
      if (change === "no-settlement") delete value.turn_clock.settled_at;
      expect(finishedRun(value, attention), change).toBeUndefined();
    }
  });
  it("suppresses a result already read in another window", () => {
    const value = snapshot();
    value.session.seen_at = "2026-10-18T00:00:02Z";
    expect(finishedRun(value, attention)).toBeUndefined();
    value.session.seen_at = "2026-10-18T00:00:00Z";
    expect(finishedRun(value, attention)).toBeDefined();
    expect(finishedRun(value, { ...attention, since_at: "invalid" })).toBeUndefined();
  });
});
