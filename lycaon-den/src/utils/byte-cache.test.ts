import { describe, expect, it } from "vitest";
import { ByteCache, retainedValueBytes } from "./byte-cache.ts";

describe("retained content budgets", () => {
  it("evicts cold values by bytes while preserving a recently opened value", () => {
    const cache = new ByteCache<string, string>(10, 10);
    cache.set("a", "a", 4);
    cache.set("b", "b", 4);
    expect(cache.get("a")).toBe("a");
    cache.set("c", "c", 4);
    expect(cache.get("b")).toBeUndefined();
    expect(cache.get("a")).toBe("a");
    expect(cache.bytes).toBe(8);
  });

  it("bounds tiny entries and replaces accounting without evicting unrelated warm values", () => {
    const cache = new ByteCache<string, number>(100, 2);
    cache.set("a", 1, 10);
    cache.set("b", 2, 10);
    cache.set("a", 3, 20);
    expect(cache.bytes).toBe(30);
    cache.set("c", 4, 10);
    expect([...cache.keys()]).toEqual(["a", "c"]);
    cache.set("large", 5, 101);
    expect(cache.size).toBe(2);
    cache.delete("a");
    expect(cache.bytes).toBe(10);
    cache.clear();
    expect(cache.bytes).toBe(0);
  });

  it("counts text and shared object graphs without serializing or following cycles", () => {
    const child = { text: "x".repeat(1000) };
    const graph: { child: typeof child; again: typeof child; self?: unknown } = { child, again: child };
    graph.self = graph;
    expect(retainedValueBytes(graph)).toBeGreaterThan(2000);
    expect(retainedValueBytes(graph)).toBeLessThan(3000);
  });
});
