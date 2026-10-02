import { afterEach, describe, expect, it } from "vitest";
import { engineDown, engineState, parseEngineState, setEngineStateForTest } from "./engine-supervision.ts";

const exit = { signal: 9, description: "was killed by signal 9 (SIGKILL)" };

describe("engine supervision state", () => {
  afterEach(() => setEngineStateForTest({ state: "idle" }));

  it("reads every state the shell publishes", () => {
    expect(parseEngineState({ state: "idle" })).toEqual({ state: "idle" });
    expect(parseEngineState({ state: "running", generation: 2 })).toEqual({ state: "running", generation: 2 });
    expect(parseEngineState({ state: "restarting", exit, attempt: 1 })).toEqual({ state: "restarting", exit, attempt: 1 });
    expect(parseEngineState({ state: "stopped", exit: { code: 3, description: "exited with status 3" } }))
      .toEqual({ state: "stopped", exit: { code: 3, description: "exited with status 3" } });
    expect(parseEngineState({ state: "stopped", exit, failure: "another engine is already serving this store" }))
      .toEqual({ state: "stopped", exit, failure: "another engine is already serving this store" });
  });

  it("refuses shapes the shell does not publish", () => {
    for (const value of [
      null,
      "running",
      { state: "running" },
      { state: "running", generation: 0 },
      { state: "restarting", attempt: 1 },
      { state: "restarting", exit: { signal: 9 }, attempt: 1 },
      { state: "stopped", exit, failure: 7 },
      { state: "crashed", exit },
    ]) {
      expect(parseEngineState(value)).toBeNull();
    }
  });

  it("is down only while the shell restarts or has stopped its engine", () => {
    expect(engineDown({ state: "idle" })).toBe(false);
    expect(engineDown({ state: "running", generation: 1 })).toBe(false);
    expect(engineDown({ state: "restarting", exit, attempt: 2 })).toBe(true);
    expect(engineDown({ state: "stopped", exit })).toBe(true);
    setEngineStateForTest({ state: "stopped", exit });
    expect(engineState().state).toBe("stopped");
    expect(engineDown()).toBe(true);
  });
});
