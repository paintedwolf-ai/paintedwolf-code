import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  flushFilesDraftSync,
  flushFilesDraftSyncFor,
  resetFilesDraftSyncForTests,
  scheduleFilesDraftSync,
} from "./files-draft-sync.ts";

const PROJECT = "p-sync";
const KEY = "r1\0a.ts";

describe("files draft sync", () => {
  beforeEach(() => {
    resetFilesDraftSyncForTests();
    vi.useFakeTimers();
  });

  it("coalesces a typing burst into one write", () => {
    const writes: string[] = [];
    for (const text of ["a", "ab", "abc"]) {
      scheduleFilesDraftSync(PROJECT, KEY, () => writes.push(text));
    }
    expect(writes).toEqual([]);
    vi.runAllTimers();
    expect(writes).toEqual(["abc"]);
  });

  it("keeps a write per buffer", () => {
    const writes: string[] = [];
    scheduleFilesDraftSync(PROJECT, "r1\0a.ts", () => writes.push("a"));
    scheduleFilesDraftSync(PROJECT, "r1\0b.ts", () => writes.push("b"));
    vi.runAllTimers();
    expect(writes.sort()).toEqual(["a", "b"]);
  });

  it("flushes one buffer on demand and leaves the others queued", () => {
    const writes: string[] = [];
    scheduleFilesDraftSync(PROJECT, "r1\0a.ts", () => writes.push("a"));
    scheduleFilesDraftSync(PROJECT, "r1\0b.ts", () => writes.push("b"));

    flushFilesDraftSyncFor(PROJECT, "r1\0a.ts");
    expect(writes).toEqual(["a"]);

    vi.runAllTimers();
    expect(writes).toEqual(["a", "b"]);
  });

  it("runs a queued write at most once", () => {
    const write = vi.fn();
    scheduleFilesDraftSync(PROJECT, KEY, write);
    flushFilesDraftSync();
    flushFilesDraftSync();
    vi.runAllTimers();
    expect(write).toHaveBeenCalledTimes(1);
  });
});
