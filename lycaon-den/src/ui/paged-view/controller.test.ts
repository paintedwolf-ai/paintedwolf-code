import { describe, expect, it } from "vitest";
import { PagedViewController } from "./controller.ts";
import { SegmentedScroll } from "./geometry.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

describe("paged presentation lifetime", () => {
  it("bounds retained detail after visiting distant ranges", async () => {
    const view = new PagedViewController<number>(() => ({ rows: 200, bytes: 1000 }), { rows: 400, pages: 2 });
    for (let at = 0; at < 100; at++) await view.load(String(at), async () => at);
    expect(view.frames()).toEqual([98, 99]);
    expect(view.usage).toEqual({ rows: 400, bytes: 2000, pages: 2, pending: 0 });
  });

  it("deduplicates requests without giving one waiter cancellation authority", async () => {
    const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }));
    const source = deferred<number>();
    const first = new AbortController();
    let calls = 0;
    let aborted = false;
    const load = (signal: AbortSignal) => { calls++; signal.addEventListener("abort", () => { aborted = true; }); return source.promise; };
    const a = view.load("range", load, first.signal).catch(error => error.name);
    const b = view.load("range", load);
    first.abort();
    expect(await a).toBe("AbortError");
    expect(aborted).toBe(false);
    source.resolve(42);
    expect(await b).toBe(42);
    expect(calls).toBe(1);
  });

  it("fences late results across detach and stage return", async () => {
    const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }));
    const old = deferred<number>();
    const pending = view.load("range", () => old.promise).catch(error => error.name);
    view.detach(); view.attach();
    const fresh = view.load("range", async () => 2);
    old.resolve(1);
    expect(await pending).toBe("AbortError");
    expect(await fresh).toBe(2);
    expect(view.frames()).toEqual([2]);
  });

  it("retains the published revision when replacement detail exceeds the budget", async () => {
    const view = new PagedViewController<number>(rows => ({ rows, bytes: 8 }), { rows: 20 });
    view.bind({ viewId: "view", intentRevision: "a", projectionRevision: "1" });
    await view.load("first", async () => 10);
    view.bind({ viewId: "view", intentRevision: "a", projectionRevision: "2" });
    expect(view.frames()).toEqual([10]);
    await expect(view.load("large", async () => 21, undefined, {
      identity: () => ({ viewId: "view", intentRevision: "a", projectionRevision: "2" }),
    })).rejects.toThrow("cache limit");
    expect(view.usage.rows).toBe(10);
  });

  it("an anchored rebase cancels older reads before publishing the replacement", async () => {
    const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }));
    view.bind({ viewId: "view", intentRevision: "intent", projectionRevision: "one" });
    const source = deferred<number>();
    let oldSignal: AbortSignal | undefined;
    const old = view.load("old", signal => { oldSignal = signal; return source.promise; }).catch(error => error.name);
    const publications: number[][] = [];
    view.subscribe(() => publications.push([...view.frames()]));
    await view.load("anchor", async () => 2, undefined,
      { identity: () => ({ viewId: "view", intentRevision: "intent", projectionRevision: "two" }) });
    expect(oldSignal?.aborted).toBe(true);
    expect(await old).toBe("AbortError");
    source.resolve(1);
    await Promise.resolve();
    expect(publications).toEqual([[2]]);
    expect(view.frames()).toEqual([2]);
    view.close();
  });
});

it("keeps ten million rows addressable within the physical scroll limit", () => {
  const geometry = new SegmentedScroll(24);
  const physical = geometry.reveal(9_000_000, 7, 10_000_000, 600);
  expect(geometry.extent(10_000_000)).toBe(4_000_000);
  expect(geometry.row(physical)).toBe(9_000_000);
  const logical = geometry.logicalOffset(physical);
  expect(geometry.logicalOffset(geometry.recenter(physical, 10_000_000, 600))).toBe(logical);
});

it("keeps an anchored read through projection notifications and cancels it on intent replacement", async () => {
  const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }));
  const identity = { viewId: "view", intentRevision: "intent", projectionRevision: "one" };
  view.bind(identity);
  const source = deferred<number>();
  let requestSignal: AbortSignal | undefined;
  const pending = view.load("anchor", signal => { requestSignal = signal; return source.promise; }, undefined,
    { identity: () => ({ ...identity, projectionRevision: "three" }), follow: true });
  view.bind({ ...identity, projectionRevision: "two" });
  expect(requestSignal?.aborted).toBe(false);
  source.resolve(3);
  expect(await pending).toBe(3);
  expect(view.frames()).toEqual([3]);
  const next = view.load("next", signal => new Promise<number>((_resolve, reject) => signal.addEventListener("abort", () => reject(new DOMException("Canceled", "AbortError")))), undefined,
    { identity: () => identity, follow: true }).catch(error => error.name);
  view.bind({ ...identity, intentRevision: "replacement" });
  expect(await next).toBe("AbortError");
  view.close();
});


it("publishes validated retained pages atomically with a new revision", async () => {
  const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }), { rows: 3 });
  const identity = { viewId: "view", intentRevision: "intent", projectionRevision: "one" };
  view.bind(identity);
  await view.load("old", async () => 9);
  const publications: number[][] = [];
  view.subscribe(() => publications.push([...view.frames()]));
  await view.load("anchor", async () => 4, undefined, {
    identity: () => ({ ...identity, projectionRevision: "two" }), retain: () => [1, 2, 3],
  });
  expect(publications).toEqual([[2, 3, 4]]);
  expect(view.usage.rows).toBe(3);
  view.close();
});


it("keeps published rows through repeated invalidation and replaces them in one publication", async () => {
  const view = new PagedViewController<number>(() => ({ rows: 1, bytes: 8 }));
  const identity = { viewId: "view", intentRevision: "intent", projectionRevision: "one" };
  view.bind(identity);
  await view.load("one", async () => 1);
  const publications: number[][] = [];
  view.subscribe(() => publications.push([...view.frames()]));
  view.bind({ ...identity, projectionRevision: "two" });
  view.bind({ ...identity, projectionRevision: "three" });
  expect(publications).toEqual([]);
  expect(view.frames()).toEqual([1]);
  await view.load("three", async () => 3, undefined, { identity: () => ({ ...identity, projectionRevision: "three" }) });
  expect(publications).toEqual([[3]]);
  view.bind({ ...identity, intentRevision: "next" });
  expect(view.frames()).toEqual([]);
  view.close();
});


it("keeps concurrent first pages when preparation resolves an empty presentation", async () => {
  const view = new PagedViewController<number>(() => ({ rows: 200, bytes: 1000 }));
  const identity = { viewId: "view", intentRevision: "intent", projectionRevision: "ready" };
  view.bind({ ...identity, projectionRevision: "preparing" });
  view.bind(identity);
  const first = deferred<number>(), second = deferred<number>();
  let secondSignal: AbortSignal | undefined;
  const a = view.load("0", () => first.promise, undefined, { identity: () => identity });
  const b = view.load("200", signal => { secondSignal = signal; return second.promise; }, undefined, { identity: () => identity });
  first.resolve(0);
  await expect(a).resolves.toBe(0);
  expect(secondSignal?.aborted).toBe(false);
  second.resolve(200);
  await expect(b).resolves.toBe(200);
  expect(view.frames()).toEqual([0, 200]);
  view.close();
});
