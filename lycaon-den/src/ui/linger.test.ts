import { afterEach, describe, expect, it, vi } from "vitest";
import { Linger, type Lingered } from "./linger.ts";

type Operation = { id: string; kind: "reading" | "editing" | "waiting" };

const LINGER_MS = 8_000;
const sameOperation = (left: Operation, right: Operation) => left.id === right.id && left.kind === right.kind;

function latest<T>(items: readonly T[]): T | undefined {
  return items[items.length - 1];
}

function createLinger(snapshots: ReadonlyMap<string, Lingered<Operation>>[], lingers?: (value: Operation) => boolean) {
  return new Linger<Operation>((next) => snapshots.push(next), { lingerMs: LINGER_MS, same: sameOperation, lingers });
}

const op = (id: string, kind: Operation["kind"] = "editing"): Operation => ({ id, kind });

describe("Linger", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("publishes a reported value as live in the same update", () => {
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);

    linger.update(new Map([["a.ts", op("edit-1")]]));

    expect(latest(snapshots)?.get("a.ts")).toEqual({ value: op("edit-1"), phase: "live", endedAt: null });
    linger.dispose();
  });

  it("marks an ended value past with its last value and end time, then removes it at the bound", () => {
    vi.useFakeTimers({ now: 1_000 });
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);

    linger.update(new Map([["a.ts", op("edit-1")]]));
    vi.advanceTimersByTime(20_000);
    linger.update(new Map());

    expect(latest(snapshots)?.get("a.ts")).toEqual({ value: op("edit-1"), phase: "past", endedAt: 21_000 });
    vi.advanceTimersByTime(LINGER_MS - 1);
    expect(latest(snapshots)?.has("a.ts")).toBe(true);
    vi.advanceTimersByTime(1);
    expect(latest(snapshots)?.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    linger.dispose();
  });

  it("returns a past value to live in place, with one entry and one timer throughout a burst", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);

    for (let index = 0; index < 1_000; index++) {
      linger.update(new Map([["a.ts", op("edit-1")]]));
      expect(latest(snapshots)?.get("a.ts")?.phase).toBe("live");
      linger.update(new Map());
      expect(latest(snapshots)?.get("a.ts")?.phase).toBe("past");
      expect(latest(snapshots)?.size).toBe(1);
      expect(vi.getTimerCount()).toBe(1);
      vi.advanceTimersByTime(1);
    }

    vi.advanceTimersByTime(LINGER_MS);
    expect(latest(snapshots)?.size).toBe(0);
    linger.dispose();
  });

  it("removes every one of many values within the bound of its own end", () => {
    vi.useFakeTimers({ now: 0 });
    const ended = new Map<string, number>();
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);

    for (let index = 0; index < 1_000; index++) {
      const key = `f${index}.ts`;
      linger.update(new Map([[key, op(key)]]));
      linger.update(new Map());
      ended.set(key, Date.now());
      vi.advanceTimersByTime(1);
    }
    expect(vi.getTimerCount()).toBe(1);

    for (let tick = 0; tick <= LINGER_MS; tick += 250) {
      for (const [key, entry] of latest(snapshots) ?? new Map()) {
        expect(Date.now() - ended.get(key)!).toBeLessThan(LINGER_MS);
        expect(entry.phase).toBe("past");
      }
      vi.advanceTimersByTime(250);
    }
    expect(latest(snapshots)?.size).toBe(0);
    linger.dispose();
  });

  it("updates a live value in place when its state changes", () => {
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);

    linger.update(new Map([["a.ts", op("call-1", "reading")]]));
    linger.update(new Map([["a.ts", op("call-1", "editing")]]));

    expect(snapshots.map((snapshot) => snapshot.get("a.ts")?.phase)).toEqual(["live", "live"]);
    expect(latest(snapshots)?.get("a.ts")?.value.kind).toBe("editing");
    linger.dispose();
  });

  it("removes a value that does not linger in the update that ends it", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots, (value) => value.kind !== "waiting");

    linger.update(new Map([["a.ts", op("call-1", "waiting")], ["b.ts", op("call-2")]]));
    linger.update(new Map());

    expect([...latest(snapshots)!.keys()]).toEqual(["b.ts"]);
    linger.dispose();
  });

  it("publishes nothing for a reported value equal to the shown one, but keeps the newest", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Lingered<{ id: string; progress: number }>>[] = [];
    const linger = new Linger<{ id: string; progress: number }>((next) => snapshots.push(next), {
      lingerMs: LINGER_MS,
      same: (shown, next) => shown.id === next.id,
    });

    linger.update(new Map([["pass", { id: "pass-1", progress: 1 }]]));
    linger.update(new Map([["pass", { id: "pass-1", progress: 7 }]]));
    expect(snapshots).toHaveLength(1);

    linger.update(new Map());
    expect(latest(snapshots)?.get("pass")?.value.progress).toBe(7);
    linger.dispose();
  });

  it("clears at once and leaves no timer", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Lingered<Operation>>[] = [];
    const linger = createLinger(snapshots);
    linger.update(new Map([["a.ts", op("edit-1")]]));
    linger.update(new Map());

    linger.clear();

    expect(latest(snapshots)?.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    linger.dispose();
  });

  it("publishes nothing after dispose", () => {
    vi.useFakeTimers();
    const onChange = vi.fn();
    const linger = new Linger<Operation>(onChange, { lingerMs: LINGER_MS });
    linger.update(new Map([["a.ts", op("edit-1")]]));
    linger.update(new Map());
    expect(vi.getTimerCount()).toBe(1);

    linger.dispose();
    vi.advanceTimersByTime(LINGER_MS);
    linger.update(new Map([["b.ts", op("edit-2")]]));
    linger.clear();

    expect(vi.getTimerCount()).toBe(0);
    expect(onChange).toHaveBeenCalledTimes(2);
  });
});
