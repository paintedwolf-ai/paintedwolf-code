import { afterEach, describe, expect, it, vi } from "vitest";
import { MIN_VISIBLE_MS, MinVisibleHold } from "./min-visible-hold.ts";

type Operation = { id: string; kind: "reading" | "editing" };

const sameOperation = (left: Operation, right: Operation) => left.id === right.id && left.kind === right.kind;

function latest<T>(items: readonly T[]): T | undefined {
  return items[items.length - 1];
}

function createHold(snapshots: ReadonlyMap<string, Operation>[]) {
  return new MinVisibleHold<Operation>((next) => snapshots.push(next), { same: sameOperation });
}

describe("MinVisibleHold", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows active state immediately and holds a fast completion", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);

    hold.update(new Map([["src/a.ts", { id: "edit-1", kind: "editing" }]]));
    expect(latest(snapshots)?.get("src/a.ts")?.kind).toBe("editing");

    vi.advanceTimersByTime(80);
    hold.update(new Map());
    vi.advanceTimersByTime(MIN_VISIBLE_MS - 81);
    expect(latest(snapshots)?.has("src/a.ts")).toBe(true);

    vi.advanceTimersByTime(1);
    expect(latest(snapshots)?.has("src/a.ts")).toBe(false);
    hold.dispose();
  });

  it("clears immediately when the work already exceeded the minimum", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);

    hold.update(new Map([["src/a.ts", { id: "read-1", kind: "reading" }]]));
    vi.advanceTimersByTime(MIN_VISIBLE_MS + 20);
    hold.update(new Map());

    expect(latest(snapshots)?.has("src/a.ts")).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    hold.dispose();
  });

  it("restarts the minimum window when a newer state replaces a key", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);

    hold.update(new Map([["src/a.ts", { id: "read-1", kind: "reading" }]]));
    vi.advanceTimersByTime(MIN_VISIBLE_MS - 100);
    hold.update(new Map([["src/a.ts", { id: "edit-1", kind: "editing" }]]));
    hold.update(new Map());

    vi.advanceTimersByTime(100);
    expect(latest(snapshots)?.get("src/a.ts")?.kind).toBe("editing");
    vi.advanceTimersByTime(MIN_VISIBLE_MS - 100);
    expect(latest(snapshots)?.has("src/a.ts")).toBe(false);
    hold.dispose();
  });

  it("keeps the first-shown time while the same state changes value", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, { id: string; progress: number }>[] = [];
    const hold = new MinVisibleHold<{ id: string; progress: number }>((next) => snapshots.push(next), {
      same: (shown, next) => shown.id === next.id,
    });

    hold.update(new Map([["pass", { id: "pass-1", progress: 1 }]]));
    vi.advanceTimersByTime(MIN_VISIBLE_MS - 100);
    hold.update(new Map([["pass", { id: "pass-1", progress: 7 }]]));
    hold.update(new Map());
    expect(snapshots).toHaveLength(1);

    vi.advanceTimersByTime(100);
    expect(latest(snapshots)?.size).toBe(0);
    hold.dispose();
  });

  it("expires independent keys at their own deadlines", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);

    hold.update(new Map([["a.ts", { id: "a", kind: "editing" }]]));
    vi.advanceTimersByTime(500);
    hold.update(
      new Map([
        ["a.ts", { id: "a", kind: "editing" }],
        ["b.ts", { id: "b", kind: "editing" }],
      ]),
    );
    hold.update(new Map());

    vi.advanceTimersByTime(MIN_VISIBLE_MS - 500);
    expect(Array.from(latest(snapshots)?.keys() ?? [])).toEqual(["b.ts"]);
    vi.advanceTimersByTime(500);
    expect(latest(snapshots)?.size).toBe(0);
    hold.dispose();
  });

  it("publishes no duplicate snapshot for unchanged active state", () => {
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);
    const active = new Map([["src/a.ts", { id: "edit-1", kind: "editing" as const }]]);

    hold.update(active);
    hold.update(new Map(active));

    expect(snapshots).toHaveLength(1);
    hold.dispose();
  });

  it("drops held state at once when cleared", () => {
    vi.useFakeTimers();
    const snapshots: ReadonlyMap<string, Operation>[] = [];
    const hold = createHold(snapshots);
    hold.update(new Map([["src/a.ts", { id: "edit-1", kind: "editing" }]]));
    hold.update(new Map());

    hold.clear();

    expect(latest(snapshots)?.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    hold.dispose();
  });

  it("cancels pending expiry on dispose", () => {
    vi.useFakeTimers();
    const onChange = vi.fn();
    const hold = new MinVisibleHold<Operation>(onChange, { same: sameOperation });
    hold.update(new Map([["src/a.ts", { id: "edit-1", kind: "editing" }]]));
    hold.update(new Map());
    expect(vi.getTimerCount()).toBe(1);

    hold.dispose();
    vi.advanceTimersByTime(MIN_VISIBLE_MS);

    expect(vi.getTimerCount()).toBe(0);
    expect(onChange).toHaveBeenCalledTimes(1);
  });
});
