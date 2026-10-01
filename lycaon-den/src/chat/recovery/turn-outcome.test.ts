import { beforeEach, describe, expect, it } from "vitest";
import {
  applyTurnOutcome,
  hasTurnOutcome,
  resetTurnOutcomesForTests,
  sessionIdleDisposition,
} from "./turn-outcome.ts";

beforeEach(resetTurnOutcomesForTests);

describe("turn outcome", () => {
  it("records each session's wire disposition separately", () => {
    applyTurnOutcome({ id: "s1", idle_disposition: "user_stopped" });
    applyTurnOutcome({ id: "s2", idle_disposition: "turn_error" });
    expect(sessionIdleDisposition("s1")).toBe("user_stopped");
    expect(sessionIdleDisposition("s2")).toBe("turn_error");
    expect(sessionIdleDisposition("s3")).toBeUndefined();
  });

  it("does not invent or clear an outcome without a wire disposition", () => {
    applyTurnOutcome({ id: "s1", idle_disposition: "completed" });
    applyTurnOutcome({ id: "s1" });
    expect(sessionIdleDisposition("s1")).toBe("completed");
  });

  it("clears a prior stop when the next turn goes busy", () => {
    applyTurnOutcome({ id: "s1", idle_disposition: "user_stopped" });
    applyTurnOutcome({ id: "s1", status: "busy" });
    expect(sessionIdleDisposition("s1")).toBeUndefined();
  });

  it("records a later completed turn after a stop", () => {
    applyTurnOutcome({ id: "s1", idle_disposition: "user_stopped" });
    applyTurnOutcome({ id: "s1", status: "busy" });
    applyTurnOutcome({ id: "s1", status: "idle", idle_disposition: "completed" });
    expect(sessionIdleDisposition("s1")).toBe("completed");
  });

  // Only visible outcomes reserve a transcript row.
  it("names only the dispositions that render a row", () => {
    expect(hasTurnOutcome("user_stopped")).toBe(true);
    expect(hasTurnOutcome("turn_error")).toBe(true);
    expect(hasTurnOutcome("interrupted")).toBe(true);
    expect(hasTurnOutcome("completed")).toBe(false);
    expect(hasTurnOutcome(undefined)).toBe(false);
  });
});
