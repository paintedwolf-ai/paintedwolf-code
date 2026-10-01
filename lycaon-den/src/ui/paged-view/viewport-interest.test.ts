import { afterEach, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { createSourceViewsClient } from "../../api/source-views-client.ts";
import { ViewportInterest } from "./viewport-interest.ts";

afterEach(() => vi.useRealTimers());

function fixture(json: (path: string, init?: RequestInit) => Promise<unknown>) {
  return new ViewportInterest(createSourceViewsClient(async <T>(path: string, init?: RequestInit) => await json(path, init) as T), "project");
}

it("coalesces motion into the latest bounded window at any repository offset", async () => {
  vi.useFakeTimers();
  const json = vi.fn(async (_path: string, _init?: RequestInit) => undefined);
  const interest = fixture(json);
  interest.update("view", "presentation", 0, 60, 1);
  interest.update("view", "presentation", 900_000_000, 900_000_060, 1);
  interest.update("view", "presentation", 500_000_000, 500_000_060, -1);
  await vi.advanceTimersByTimeAsync(100);
  expect(json).toHaveBeenCalledTimes(1);
  const [url, init] = json.mock.calls[0]!;
  expect(url).toContain("/source/views/view/interests/");
  expect(JSON.parse(String(init?.body))).toEqual({ presentation_id: "presentation", sequence: 3, start: 500_000_000, end: 500_000_200, direction: -1 });
  interest.clear();
  expect(json).toHaveBeenLastCalledWith(url, { method: "DELETE" });
});

it("serializes updates and releases after pending admission settles", async () => {
  vi.useFakeTimers();
  let finish!: () => void;
  const json = vi.fn((_path: string, init?: RequestInit) => init?.method === "PUT"
    ? new Promise<void>((resolve) => { finish = resolve; }) : Promise.resolve());
  const interest = fixture(json);
  interest.update("view", "presentation", 0, 60, 1);
  await vi.advanceTimersByTimeAsync(100);
  interest.update("view", "presentation", 500, 550, 1);
  await vi.advanceTimersByTimeAsync(100);
  expect(json).toHaveBeenCalledTimes(1);
  interest.clear();
  expect(json).toHaveBeenCalledTimes(1);
  finish();
  await vi.advanceTimersByTimeAsync(0);
  expect(json).toHaveBeenCalledTimes(2);
  expect(json.mock.calls[1]?.[1]?.method).toBe("DELETE");
});

it("honors Retry-After and sends only the latest demand after backpressure", async () => {
  vi.useFakeTimers();
  const json = vi.fn(async (_path: string, _init?: RequestInit) => undefined);
  json.mockRejectedValueOnce(new LycaonApiError("busy", 429, "rate_limited", { retryAfterMs: 2000 }));
  const interest = fixture(json);
  interest.update("view", "presentation", 0, 60, 1);
  await vi.advanceTimersByTimeAsync(100);
  interest.update("view", "presentation", 1000, 1100, 1);
  await vi.advanceTimersByTimeAsync(1999);
  expect(json).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1);
  expect(json).toHaveBeenCalledTimes(2);
  expect(JSON.parse(String(json.mock.calls[1]?.[1]?.body)).start).toBe(1000);
  interest.clear();
});

it("keeps an old view's cleanup separate from the new view", async () => {
  vi.useFakeTimers();
  let finish!: () => void;
  const json = vi.fn((_path: string, init?: RequestInit) => init?.method === "PUT" && _path.includes("/old/")
    ? new Promise<void>((resolve) => { finish = resolve; }) : Promise.resolve());
  const interest = fixture(json);
  interest.update("old", "presentation", 0, 60, 1);
  await vi.advanceTimersByTimeAsync(100);
  interest.update("new", "presentation", 0, 60, 1);
  await vi.advanceTimersByTimeAsync(100);
  finish();
  await vi.advanceTimersByTimeAsync(0);
  expect(json.mock.calls.filter(([, init]) => init?.method === "DELETE").map(([path]) => path)).toEqual([json.mock.calls[0]?.[0]]);
  interest.clear();
});

it("keeps active demand through prolonged backpressure and stops when cleared", async () => {
  vi.useFakeTimers();
  const json = vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === "PUT") throw new LycaonApiError("busy", 503, "rate_limited");
  });
  const interest = fixture(json);
  interest.update("view", "presentation", 0, 60, 1);
  await vi.advanceTimersByTimeAsync(10_000);
  const puts = () => json.mock.calls.filter(([, init]) => init?.method === "PUT").length;
  expect(puts()).toBeGreaterThan(4);
  interest.clear();
  const before = puts();
  await vi.advanceTimersByTimeAsync(120_000);
  expect(puts()).toBe(before);
});
