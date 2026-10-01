import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BOARD_COALESCE_MS,
  createBoardCoalescer,
  createStoreBoardCoalescer,
} from "./board-sync.ts";
import { createAppStore } from "./app-state.ts";
import type { BoardView } from "../api/types.ts";

const snapshot = (n: number): BoardView =>
  ({
    project_id: "proj-1",
    pack_content_hash: `v${n}`,
  }) as unknown as BoardView;

describe("createBoardCoalescer", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces burst board events into one apply after quiet period", () => {
    const applied: BoardView[] = [];
    const coalescer = createBoardCoalescer((s) => applied.push(s));

    coalescer.schedule({ snapshot: snapshot(1) } as never);
    vi.advanceTimersByTime(100);
    coalescer.schedule({ snapshot: snapshot(2) } as never);
    vi.advanceTimersByTime(100);
    coalescer.schedule({ snapshot: snapshot(3) } as never);

    expect(applied).toHaveLength(0);
    vi.advanceTimersByTime(BOARD_COALESCE_MS - 1);
    expect(applied).toHaveLength(0);
    vi.advanceTimersByTime(1);
    expect(applied).toHaveLength(1);
    expect(applied[0]?.pack_content_hash).toBe("v3");
  });

  it("store coalescer writes latest snapshot to app state", () => {
    const appStore = createAppStore();
    const coalescer = createStoreBoardCoalescer(appStore.actions);
    coalescer.schedule({ snapshot: snapshot(1) } as never);
    vi.advanceTimersByTime(100);
    coalescer.schedule({ snapshot: snapshot(2) } as never);
    vi.advanceTimersByTime(BOARD_COALESCE_MS);
    expect(appStore.state.board?.pack_content_hash).toBe("v2");
  });

  it("cancel clears pending snapshot", () => {
    const applied: BoardView[] = [];
    const coalescer = createBoardCoalescer((s) => applied.push(s));
    coalescer.schedule({ snapshot: snapshot(1) } as never);
    coalescer.cancel();
    vi.advanceTimersByTime(BOARD_COALESCE_MS);
    expect(applied).toHaveLength(0);
  });
});
