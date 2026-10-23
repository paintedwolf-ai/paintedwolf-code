import { afterEach, describe, expect, it } from "vitest";
import {
  clearEngineStartupProgress,
  engineStartupPhaseLabel,
  engineStartupState,
  noteEngineStartupProgress,
} from "./engine-startup.ts";

const progress = {
  protocol: 1,
  status: "starting" as const,
  phase: "pricing" as const,
  pid: 42,
  sequence: 3,
  elapsed_ms: 9_500,
  silence_ms: 0,
};

describe("engine startup state", () => {
  afterEach(() => clearEngineStartupProgress());

  it("projects semantic host phases onto sentence-case labels", () => {
    expect(engineStartupPhaseLabel("pricing")).toBe("Loading model pricing");
    expect(engineStartupPhaseLabel("user_path")).toBe(
      "Finding command-line tools",
    );
  });

  it.each([
    ["upgrade_snapshot", "Saving recovery data before updating"],
    ["schema_upgrade", "Updating local data"],
    ["upgrade_validation", "Verifying updated local data"],
  ] as const)("renders the durable upgrade phase %s", (phase, label) => {
    noteEngineStartupProgress({ ...progress, phase });
    expect(engineStartupState()).toMatchObject({ phase });
    expect(engineStartupPhaseLabel(phase)).toBe(label);
  });

  it("tracks progress and the host's non-terminal stalled state", () => {
    noteEngineStartupProgress(progress);
    expect(engineStartupState()).toEqual(progress);

    noteEngineStartupProgress({
      ...progress,
      status: "stalled",
      elapsed_ms: 15_000,
      silence_ms: 5_000,
    });
    expect(engineStartupState().status).toBe("stalled");
  });

  it("ignores malformed and stale records", () => {
    noteEngineStartupProgress(progress);
    noteEngineStartupProgress({ ...progress, sequence: 2, phase: "store" });
    noteEngineStartupProgress({ ...progress, phase: "store" });
    noteEngineStartupProgress({ ...progress, protocol: 99, sequence: 4 });
    noteEngineStartupProgress({ ...progress, sequence: 4, phase: "ready" });
    noteEngineStartupProgress({ ...progress, sequence: 4, elapsed_ms: 4.5 });
    expect(engineStartupState()).toEqual(progress);
  });

  it("keeps a live observation bound to one exact child", () => {
    noteEngineStartupProgress(progress);
    noteEngineStartupProgress({ ...progress, pid: 84, sequence: 1, phase: "launch" });
    expect(engineStartupState()).toEqual(progress);

    clearEngineStartupProgress();
    noteEngineStartupProgress({ ...progress, pid: 84, sequence: 1, phase: "launch" });
    expect(engineStartupState()).toMatchObject({ pid: 84, phase: "launch" });
  });
});
