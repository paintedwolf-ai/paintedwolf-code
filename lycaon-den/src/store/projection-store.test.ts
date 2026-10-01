import { describe, expect, it, vi } from "vitest";
import { createProjectionStore, PROJECTION_READ_TIMEOUT_MS } from "./projection-store.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

describe("projection store", () => {
  it("keeps the displayed identity when a re-read returns the same data", async () => {
    const record = createProjectionStore<{ rows: { path: string }[] }>().get("a");
    const first = await record.read(async () => ({ rows: [{ path: "README.md" }] }));
    record.invalidate();
    const again = await record.read(async () => ({ rows: [{ path: "README.md" }] }));
    expect(again).toBe(first);
    expect(record.value()).toBe(first);

    record.invalidate();
    const changed = await record.read(async () => ({ rows: [{ path: "AGENTS.md" }] }));
    expect(changed).not.toBe(first);
    expect(record.value()).toEqual({ rows: [{ path: "AGENTS.md" }] });
  });

  it("aborts a read its last holder released and shows the prior value", async () => {
    const record = createProjectionStore<string>().get("a");
    await record.read(async () => "first");
    record.invalidate();
    let seen: AbortSignal | undefined;
    const release = record.retain();
    const read = record.read((signal) => {
      seen = signal;
      return new Promise<string>(() => undefined);
    });
    await Promise.resolve();
    expect(record.state().state).toBe("loading");

    release();
    await read;

    expect(seen?.aborted).toBe(true);
    expect(record.state()).toEqual({ state: "loaded", value: "first" });
  });

  it("keeps a read another holder still needs", async () => {
    const record = createProjectionStore<string>().get("a");
    const pending = deferred<string>();
    let seen: AbortSignal | undefined;
    const releaseA = record.retain();
    const releaseB = record.retain();
    const read = record.read((signal) => {
      seen = signal;
      return pending.promise;
    });
    await Promise.resolve();
    releaseA();
    expect(seen?.aborted).toBe(false);
    pending.resolve("kept");
    await read;
    expect(record.value()).toBe("kept");
    releaseB();
  });

  it("settles a stalled read as a retryable failure and ignores its late response", async () => {
    vi.useFakeTimers();
    try {
      const record = createProjectionStore<string>().get("a");
      const pending = deferred<string>();
      const read = record.read(() => pending.promise);
      await vi.advanceTimersByTimeAsync(PROJECTION_READ_TIMEOUT_MS);
      await read;
      expect(record.state()).toMatchObject({ state: "error", message: "Loading took too long. Try again." });
      pending.resolve("late");
      await Promise.resolve();
      expect(record.value()).toBeUndefined();
      record.invalidate();
      await record.read(async () => "recovered");
      expect(record.value()).toBe("recovered");
    } finally {
      vi.useRealTimers();
    }
  });
  it("shares an in-flight read for one exact identity", async () => {
    const store = createProjectionStore<string[]>();
    const pending = deferred<string[]>();
    const fetch = vi.fn(() => pending.promise);
    const record = store.get("project-a");
    const a = record.read(fetch);
    const b = store.get("project-a").read(fetch);
    expect(a).toBe(b);
    pending.resolve(["one"]);
    await a;
    expect(fetch).toHaveBeenCalledOnce();
    expect(record.value()).toEqual(["one"]);
    expect(store.get("project-b").value()).toBeUndefined();
  });

  it("reconciles an invalidation received during acquisition", async () => {
    const record = createProjectionStore<string>().get("a");
    const first = deferred<string>();
    const fetch = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue("current");
    const read = record.read(fetch);
    record.invalidate();
    first.resolve("obsolete");
    expect(await read).toBe("current");
    expect(record.value()).toBe("current");
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("a command result fences an older read", async () => {
    const record = createProjectionStore<string>().get("a");
    const pending = deferred<string>();
    const read = record.read(() => pending.promise);
    record.publish("committed");
    pending.resolve("before command");
    await read;
    expect(record.value()).toBe("committed");
  });

  it("retains successful data after a failed refresh", async () => {
    const record = createProjectionStore<string[]>().get("a");
    record.publish(["kept"]);
    record.invalidate();
    await record.read(() => Promise.reject(new Error("Disconnected")));
    expect(record.state()).toEqual({ state: "error", previous: ["kept"], message: "Disconnected" });
  });

  it("evicts unused records while retaining mounted consumers", () => {
    const store = createProjectionStore<string>(2);
    const a = store.get("a");
    a.publish("A");
    const release = a.retain();
    store.get("b");
    store.get("c");
    expect(store.size()).toBe(2);
    expect(store.get("a")).toBe(a);
    release();
    store.clear();
    expect(store.size()).toBe(0);
  });
});
