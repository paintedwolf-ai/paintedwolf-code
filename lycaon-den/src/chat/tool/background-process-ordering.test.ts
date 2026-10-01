import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BackgroundProcessOutput } from "../../api/types.ts";
import {
  applyBackgroundProcessEvent,
  applyBackgroundProcessOutputs,
  forgetBackgroundProcessSession,
  getBackgroundProcessSnapshot,
  hasHydratedBackgroundProcesses,
  refreshBackgroundProcessSnapshots,
  resetBackgroundProcessStoreForTests,
} from "./background-process-store.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function output(running: boolean, text: string): BackgroundProcessOutput {
  return {
    process_id: "process",
    chunks: [{ offset: 0, stream: "stdout", text }],
    running, ...(!running ? { exit_code: 7 } : {}),
  };
}

function processes(running: boolean, text: string) {
  const snapshot = output(running, text);
  return [{ process_id: snapshot.process_id, running, exit_code: snapshot.exit_code, output: snapshot }];
}

describe("background process snapshot ordering", () => {
  beforeEach(resetBackgroundProcessStoreForTests);

  it.each(["output", "exit", "reset"] as const)(
    "reconciles complete screened output after a newer %s event",
    async (kind) => {
      const stale = deferred<ReturnType<typeof processes>>();
      const completeText = "before\n[redacted]\ncafé 🐺\nafter\n";
      const list = vi.fn()
        .mockImplementationOnce(() => stale.promise)
        .mockResolvedValue(processes(false, completeText));
      const client = {
        listSessionBackgroundProcesses: list,
        getSessionBackgroundProcessOutput: vi.fn(),
      };
      const pending = refreshBackgroundProcessSnapshots(client, "session");
      await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(1));
      applyBackgroundProcessEvent({
        session_id: "session", process_id: "process", end_offset: 900,
        stream: kind === "exit" ? "exit" : "stdout",
        running: kind !== "exit", exit_code: kind === "exit" ? 7 : undefined,
        text: "café 🐺\nafter\n", reset: kind === "reset",
      });
      stale.resolve(processes(true, "before\n"));
      await pending;
      expect(getBackgroundProcessSnapshot("session", "process")).toMatchObject({
        running: false, exitCode: 7, text: completeText,
      });
      expect(list).toHaveBeenCalledTimes(2);
      expect(client.getSessionBackgroundProcessOutput).not.toHaveBeenCalled();
      expect(hasHydratedBackgroundProcesses("session")).toBe(true);
    },
  );

  it("does not remove a process promoted while the process list was in flight", async () => {
    const stale = deferred<ReturnType<typeof processes>>();
    const list = vi.fn().mockImplementationOnce(() => stale.promise)
      .mockResolvedValue(processes(true, "complete history"));
    const pending = refreshBackgroundProcessSnapshots({
      listSessionBackgroundProcesses: list,
      getSessionBackgroundProcessOutput: vi.fn(),
    }, "session");
    applyBackgroundProcessEvent({
      session_id: "session", process_id: "process", end_offset: 500,
      stream: "stdout", text: "recent", running: true,
    });
    stale.resolve([]);
    await pending;
    expect(getBackgroundProcessSnapshot("session", "process")?.text).toBe("complete history");
    expect(list).toHaveBeenCalledTimes(2);
  });

  it.each(["bootstrap", "retirement", "newer refresh"] as const)(
    "abandons late output after %s without restarting obsolete reads",
    async (replacement) => {
      const stale = deferred<ReturnType<typeof processes>>();
      const list = vi.fn(() => stale.promise);
      const pending = refreshBackgroundProcessSnapshots({
        listSessionBackgroundProcesses: list,
        getSessionBackgroundProcessOutput: vi.fn(),
      }, "session");
      await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(1));
      if (replacement === "retirement") forgetBackgroundProcessSession("session");
      else if (replacement === "bootstrap") applyBackgroundProcessOutputs("session", [output(false, "new")]);
      else await refreshBackgroundProcessSnapshots({
        listSessionBackgroundProcesses: vi.fn(async () => processes(false, "new")),
        getSessionBackgroundProcessOutput: vi.fn(),
      }, "session");
      stale.resolve(processes(true, "old"));
      await pending;
      expect(list).toHaveBeenCalledTimes(1);
      if (replacement === "retirement") {
        expect(getBackgroundProcessSnapshot("session", "process")).toBeUndefined();
        expect(hasHydratedBackgroundProcesses("session")).toBe(false);
      } else expect(getBackgroundProcessSnapshot("session", "process")).toMatchObject({
        text: "new", running: false, exitCode: 7,
      });
    },
  );

  it("does not retry for events belonging to another session", async () => {
    const stale = deferred<ReturnType<typeof processes>>();
    const list = vi.fn(() => stale.promise);
    const pending = refreshBackgroundProcessSnapshots({
      listSessionBackgroundProcesses: list,
      getSessionBackgroundProcessOutput: vi.fn(),
    }, "session");
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(1));
    applyBackgroundProcessEvent({
      session_id: "other", process_id: "process", end_offset: 9,
      stream: "exit", running: false, exit_code: 7,
    });
    stale.resolve(processes(true, "current"));
    await pending;
    expect(list).toHaveBeenCalledTimes(1);
    expect(getBackgroundProcessSnapshot("session", "process")?.running).toBe(true);
  });
});
