import { beforeEach, describe, expect, it } from "vitest";
import {
  CLOSED_BUFFER_RING_CAP,
  closedEntryFromBuffer,
  loadClosedBufferRingStore,
  popClosedBuffer,
  pushClosedBuffer,
  resetClosedBufferRingForTests,
  toClosedBufferRingStore,
} from "./closed-buffer-ring.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

const PROJECT = "proj-ring";

describe("closed-buffer-ring", () => {
  beforeEach(() => {
    resetClosedBufferRingForTests();
  });

  it("caps at 20 and drops the oldest", () => {
    for (let i = 0; i < CLOSED_BUFFER_RING_CAP + 1; i++) {
      pushClosedBuffer(PROJECT, {
        key: fileBufferKey("r", `f${i}.ts`),
        rootId: "r",
        path: `f${i}.ts`,
      });
    }
    const list = (toClosedBufferRingStore().byProject[PROJECT] ?? []);
    expect(list).toHaveLength(CLOSED_BUFFER_RING_CAP);
    expect(list[0]?.path).toBe(`f${CLOSED_BUFFER_RING_CAP}.ts`);
    expect(list.some((e) => e.path === "f0.ts")).toBe(false);
  });

  it("preserves tab presentation on the ring entry", () => {
    const entry = closedEntryFromBuffer({
      key: fileBufferKey("r", "a.ts"),
      rootId: "r",
      path: "a.ts",
      pinned: true,
      decodeAs: "utf-16be",
    });
    expect(entry?.pinned).toBe(true);
    pushClosedBuffer(PROJECT, entry!);
    loadClosedBufferRingStore(toClosedBufferRingStore());
    const popped = popClosedBuffer(PROJECT);
    expect(popped?.path).toBe("a.ts");
    expect(popped?.decodeAs).toBe("utf-16be");
    expect(popClosedBuffer(PROJECT)).toBeNull();
  });

  it("excludes job-scoped buffers", () => {
    const entry = closedEntryFromBuffer({
      key: fileBufferKey("r", "a.ts", "job-1"),
      rootId: "r",
      path: "a.ts",
      jobId: "job-1",
    });
    expect(entry).toBeNull();
    expect((toClosedBufferRingStore().byProject[PROJECT] ?? [])).toHaveLength(0);
  });

  it("serializes per project", () => {
    pushClosedBuffer(PROJECT, {
      key: fileBufferKey("r", "a.ts"),
      rootId: "r",
      path: "a.ts",
    });
    const store = toClosedBufferRingStore();
    expect(store.byProject[PROJECT]?.[0]?.path).toBe("a.ts");
  });
});
