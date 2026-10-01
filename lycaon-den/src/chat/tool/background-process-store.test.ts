import { describe, expect, it, vi } from "vitest";
import {
  applyBackgroundProcessEvent,
  forgetBackgroundProcessSession,
  getBackgroundProcessSnapshot,
  hasHydratedBackgroundProcesses,
  refreshBackgroundProcessSnapshots,
  resetBackgroundProcessStoreForTests,
  subscribeBackgroundProcessStore,
} from "./background-process-store.ts";

describe("background-process-store", () => {
  it("accumulates stdout chunks by process", () => {
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessEvent({
      process_id: "h1",
      session_id: "s1",
      stream: "stdout",
      text: "line1",
      end_offset: 0,
      running: true,
    });
    applyBackgroundProcessEvent({
      process_id: "h1",
      session_id: "s1",
      stream: "stdout",
      text: "line2",
      end_offset: 5,
      running: true,
    });
    const snap = getBackgroundProcessSnapshot("s1", "h1");
    expect(snap?.text).toBe("line1line2");
    expect(snap?.running).toBe(true);
  });

  it("does not wake unrelated cards or status subscribers for output-only events", () => {
    resetBackgroundProcessStoreForTests();
    const status = vi.fn(); const output = vi.fn(); const other = vi.fn();
    subscribeBackgroundProcessStore(status, () => ({ sessionId: "s", statusOnly: true }));
    subscribeBackgroundProcessStore(output, () => ({ sessionId: "s", processId: "h" }));
    subscribeBackgroundProcessStore(other, () => ({ sessionId: "another" }));
    const event = { session_id: "s", process_id: "h", running: true, end_offset: 0, stream: "stdout", text: "a" };
    applyBackgroundProcessEvent(event);
    applyBackgroundProcessEvent({ ...event, end_offset: 1, text: "b" });
    expect(status).toHaveBeenCalledTimes(1);
    expect(output).toHaveBeenCalledTimes(2);
    expect(other).not.toHaveBeenCalled();
    applyBackgroundProcessEvent({ ...event, stream: "exit", running: false, exit_code: 0 });
    expect(status).toHaveBeenCalledTimes(2);
  });

  it("tracks running state per process", () => {
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessEvent({
      process_id: "h1",
      session_id: "s1",
      stream: "stdout",
      text: "a",
      end_offset: 0,
      running: true,
    });
    applyBackgroundProcessEvent({
      process_id: "h2",
      session_id: "s1",
      stream: "exit",
      end_offset: 0,
      running: false,
      exit_code: 0,
    });
    expect(getBackgroundProcessSnapshot("s1", "h1")?.running).toBe(true);
    expect(getBackgroundProcessSnapshot("s1", "h2")?.running).toBe(false);
  });

  it("rehydrates process output from the host after a frontend reload", async () => {
    resetBackgroundProcessStoreForTests();
    const listSessionBackgroundProcesses = vi.fn(async () => [
      { process_id: "h1", running: true, output: { process_id: "h1", chunks: [{ offset: 0, stream: "stdout", text: "running\n" }], running: true } },
    ]);
    const getSessionBackgroundProcessOutput = vi.fn(async () => ({
      process_id: "h1",
      chunks: [
        { offset: 0, stream: "stdout", text: "running\n" },
      ],
      running: true,
    }));

    await refreshBackgroundProcessSnapshots(
      { listSessionBackgroundProcesses, getSessionBackgroundProcessOutput },
      "s1",
    );

    expect(getSessionBackgroundProcessOutput).not.toHaveBeenCalled();
    expect(getBackgroundProcessSnapshot("s1", "h1")).toMatchObject({
      running: true,
      text: "running\n",
    });
    expect(hasHydratedBackgroundProcesses("s1")).toBe(true);
  });

  it("drops stale process cards once the host snapshot is authoritative", async () => {
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessEvent({
      process_id: "evicted",
      session_id: "s1",
      stream: "stdout",
      text: "old",
      end_offset: 0,
      running: true,
    });

    await refreshBackgroundProcessSnapshots(
      {
        listSessionBackgroundProcesses: vi.fn(async () => []),
        getSessionBackgroundProcessOutput: vi.fn(async () => ({
          process_id: "late",
          chunks: [],
          running: true,
        })),
      },
      "s1",
    );

    expect(getBackgroundProcessSnapshot("s1", "evicted")).toBeUndefined();
    expect(hasHydratedBackgroundProcesses("s1")).toBe(true);
  });

  it("keeps terminal details when a host snapshot replaces an SSE-only card", async () => {
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessEvent({
      process_id: "h1",
      session_id: "s1",
      stream: "stdout",
      text: "old",
      end_offset: 0,
      running: true,
    });
    await refreshBackgroundProcessSnapshots(
      {
        listSessionBackgroundProcesses: vi.fn(async () => [
          { process_id: "h1", running: false, output: { process_id: "h1", chunks: [{ offset: 0, stream: "stdout", text: "done" }], running: false, exit_code: 0 } },
        ]),
        getSessionBackgroundProcessOutput: vi.fn(async () => ({
          process_id: "h1",
          chunks: [{ offset: 0, stream: "stdout", text: "done" }],
          running: false,
          exit_code: 0,
        })),
      },
      "s1",
    );
    expect(getBackgroundProcessSnapshot("s1", "h1")).toMatchObject({
      running: false,
      exitCode: 0,
      text: "done",
    });
  });

  it("caps accumulated text while retaining current output and explicit truncation", () => {
    resetBackgroundProcessStoreForTests();
    const chunk = "x".repeat(50_000);
    for (let i = 0; i < 10; i++) {
      applyBackgroundProcessEvent({
        process_id: "h1",
        session_id: "s1",
        stream: "stdout",
        text: i === 0 ? `HEAD${chunk}` : i === 9 ? `${chunk}TAIL` : chunk,
        end_offset: i,
        running: true,
      });
    }
    const text = getBackgroundProcessSnapshot("s1", "h1")?.text ?? "";
    expect(text.length).toBeLessThanOrEqual(8192);
    expect(text.endsWith("TAIL")).toBe(true);
    expect(getBackgroundProcessSnapshot("s1", "h1")?.truncated).toBe(true);
  });

  it("evicts the oldest session's snapshots past the session cap", () => {
    resetBackgroundProcessStoreForTests();
    for (let i = 0; i < 70; i++) {
      applyBackgroundProcessEvent({
        process_id: "h1",
        session_id: `s${i}`,
        stream: "stdout",
        text: "out",
        end_offset: 0,
        running: true,
      });
    }
    expect(getBackgroundProcessSnapshot("s0", "h1")).toBeUndefined();
    expect(hasHydratedBackgroundProcesses("s0")).toBe(false);
    expect(getBackgroundProcessSnapshot("s69", "h1")?.text).toBe("out");
  });

  it("forgets retired sessions and rejects a late hydration result", async () => {
    resetBackgroundProcessStoreForTests();
    let resolveList: ((value: Array<{ process_id: string; running: boolean }>) => void) |
      undefined;
    const pending = refreshBackgroundProcessSnapshots(
      {
        listSessionBackgroundProcesses: () =>
          new Promise((resolve) => {
            resolveList = resolve;
          }),
        getSessionBackgroundProcessOutput: vi.fn(),
      },
      "s1",
    );
    forgetBackgroundProcessSession("s1");
    resolveList?.([{ process_id: "late", running: true }]);
    await pending;

    expect(getBackgroundProcessSnapshot("s1", "late")).toBeUndefined();
    expect(hasHydratedBackgroundProcesses("s1")).toBe(false);
  });
});
