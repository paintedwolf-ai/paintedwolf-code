import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceSymbolsResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { fetchCachedSourceSymbols, resetSourceSymbolsCacheForTests } from "./source-symbols-cache.ts";

const response = (sha256 = "sha"): SourceSymbolsResponse => ({
  sha256, symbols: [{ name: "main", kind: "function", line: 1 }], truncated: false,
});
function fixture() {
  let resolve!: (value: SourceSymbolsResponse) => void;
  const list = vi.fn<LycaonClient["listProjectSourceSymbols"]>(() => new Promise<SourceSymbolsResponse>((done) => { resolve = done; }));
  const client = stubClient({ listProjectSourceSymbols: list });
  const args = { client, projectId: "p", rootId: "r", path: "main.go", sha256: "sha" };
  return { args, list, resolve: (value = response()) => resolve(value) };
}
beforeEach(resetSourceSymbolsCacheForTests);

describe("source symbol revisions", () => {
  it("shares in-flight work and retains immutable results across time", async () => {
    const f = fixture();
    const first = fetchCachedSourceSymbols(f.args);
    const second = fetchCachedSourceSymbols(f.args);
    expect(f.list).toHaveBeenCalledTimes(1);
    f.resolve();
    expect(await first).toBe(await second);
    expect(Object.isFrozen(await first)).toBe(true);
    expect(await fetchCachedSourceSymbols(f.args)).toBe(await first);
    expect(f.list).toHaveBeenCalledTimes(1);
  });

  it("rejects a response for a different disk revision without poisoning the cache", async () => {
    const f = fixture();
    const first = fetchCachedSourceSymbols(f.args);
    f.resolve(response("changed"));
    expect(await first).toEqual([]);
    const retry = fetchCachedSourceSymbols(f.args);
    expect(f.list).toHaveBeenCalledTimes(2);
    f.resolve();
    expect(await retry).toEqual(response().symbols);
  });

  it("cancels subscribers independently and aborts work when nobody needs it", async () => {
    const f = fixture();
    const a = new AbortController();
    const b = new AbortController();
    const first = fetchCachedSourceSymbols({ ...f.args, signal: a.signal });
    const second = fetchCachedSourceSymbols({ ...f.args, signal: b.signal });
    const firstFailure = expect(first).rejects.toMatchObject({ name: "AbortError" });
    a.abort();
    await firstFailure;
    const sharedSignal = f.list.mock.calls[0]![2]!.signal!;
    expect(sharedSignal.aborted).toBe(false);
    const secondFailure = expect(second).rejects.toMatchObject({ name: "AbortError" });
    b.abort();
    await secondFailure;
    expect(sharedSignal.aborted).toBe(true);
    f.resolve();
  });

  it("isolates connections and evicts old revisions", async () => {
    const list = vi.fn(async (_project: string, path: string) => response(path));
    const client = stubClient({ listProjectSourceSymbols: list });
    const args = { client, projectId: "p", rootId: "r", path: "0", sha256: "0" };
    for (let i = 0; i < 257; i++) await fetchCachedSourceSymbols({ ...args, path: String(i), sha256: String(i) });
    await fetchCachedSourceSymbols(args);
    expect(list).toHaveBeenCalledTimes(258);
    await fetchCachedSourceSymbols({ ...args, client: stubClient({ listProjectSourceSymbols: list }) });
    expect(list).toHaveBeenCalledTimes(259);
  });
});

it("backs off structured analysis failures while allowing a changed revision immediately", async () => {
  const { LycaonApiError } = await import("../../api/http.ts");
  const error = new LycaonApiError("Analysis budget exceeded", 503, "source_analysis_incomplete");
  const list = vi.fn().mockRejectedValue(error);
  const args = { client: stubClient({ listProjectSourceSymbols: list }), projectId: "p", rootId: "r", path: "large.ts", sha256: "old" };
  await expect(fetchCachedSourceSymbols(args)).rejects.toBe(error);
  await expect(fetchCachedSourceSymbols(args)).rejects.toBe(error);
  expect(list).toHaveBeenCalledTimes(1);
  await expect(fetchCachedSourceSymbols({ ...args, sha256: "new" })).rejects.toBe(error);
  expect(list).toHaveBeenCalledTimes(2);
});
