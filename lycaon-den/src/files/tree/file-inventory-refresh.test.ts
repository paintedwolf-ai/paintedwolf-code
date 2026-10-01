import { afterEach, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceSearchResponse } from "../../api/types.ts";
import { watchIndexedFiles } from "./file-inventory.ts";

afterEach(() => vi.useRealTimers());

function page(refreshing: boolean, paths: string[]): SourceSearchResponse {
  return { state: "ready", revision: refreshing ? 1 : 2, refreshing, coverage: [],
    matches: paths.map(path => ({ root_id: "r", path, highlights: [] })) };
}

it("publishes a readable partial page and later matches without changing the query", async () => {
  vi.useFakeTimers();
  const searchProjectSource = vi.fn().mockResolvedValueOnce(page(true, ["early.txt"]))
    .mockResolvedValue(page(false, ["early.txt", "late.txt"]));
  const publish = vi.fn();
  const run = watchIndexedFiles(stubClient({ searchProjectSource }), "p", "txt", publish);
  await vi.advanceTimersByTimeAsync(0);
  expect(publish.mock.calls[0]?.[0].map((file: { path: string }) => file.path)).toEqual(["early.txt"]);
  await vi.advanceTimersByTimeAsync(750);
  await run;
  expect(publish.mock.calls[1]?.[0].map((file: { path: string }) => file.path)).toEqual(["early.txt", "late.txt"]);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(searchProjectSource).toHaveBeenCalledTimes(2);
  expect(vi.getTimerCount()).toBe(0);
});

it("cancels pending discovery without publishing a late response", async () => {
  vi.useFakeTimers();
  const controller = new AbortController();
  let finish!: (response: SourceSearchResponse) => void;
  const searchProjectSource = vi.fn(() => new Promise<SourceSearchResponse>(resolve => { finish = resolve; }));
  const publish = vi.fn();
  const run = watchIndexedFiles(stubClient({ searchProjectSource }), "p", "txt", publish, { signal: controller.signal });
  const failure = expect(run).rejects.toMatchObject({ name: "AbortError" });
  controller.abort();
  finish(page(true, ["obsolete.txt"]));
  await failure;
  expect(publish).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

it("ends retrying on a failed refresh after preserving the published page", async () => {
  vi.useFakeTimers();
  const searchProjectSource = vi.fn().mockResolvedValueOnce(page(true, ["early.txt"]))
    .mockRejectedValue(new Error("offline"));
  const publish = vi.fn();
  const run = watchIndexedFiles(stubClient({ searchProjectSource }), "p", "txt", publish);
  const failure = expect(run).rejects.toThrow("offline");
  await vi.advanceTimersByTimeAsync(750);
  await failure;
  expect(publish).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(searchProjectSource).toHaveBeenCalledTimes(2);
});
