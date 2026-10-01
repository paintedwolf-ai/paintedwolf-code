import { invalidateSourceQueries } from "../files/source/source-invalidation.ts";
import { createRoot, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { createSourceSearchRefresh } from "./source-search-refresh.ts";

afterEach(() => vi.useRealTimers());

it("coalesces source changes and refreshes a hidden surface when it returns", async () => {
  vi.useFakeTimers();
  const refresh = vi.fn(async () => {});
  const [active, setActive] = createSignal(true);
  const dispose = createRoot((dispose) => {
    createSourceSearchRefresh(active, refresh);
    return dispose;
  });
  try {
    invalidateSourceQueries({ projectId: "project" });
    invalidateSourceQueries({ projectId: "project" });
    await vi.advanceTimersByTimeAsync(450);
    expect(refresh).toHaveBeenCalledTimes(1);
    setActive(false);
    invalidateSourceQueries({ projectId: "project" });
    await vi.advanceTimersByTimeAsync(3000);
    expect(refresh).toHaveBeenCalledTimes(1);
    setActive(true);
    await vi.advanceTimersByTimeAsync(450);
    expect(refresh).toHaveBeenCalledTimes(2);
    invalidateSourceQueries({ projectId: "project" });
  } finally {
    dispose();
  }
  await vi.advanceTimersByTimeAsync(3000);
  expect(refresh).toHaveBeenCalledTimes(2);
});
