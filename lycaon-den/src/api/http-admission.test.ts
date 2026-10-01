import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { lycaonFetch } from "./http.ts";

const connection = { baseUrl: "http://127.0.0.1:8787", apiToken: "fixture" };
const refusal = (retryAfter?: string) => new Response(JSON.stringify({ code: "rate_limited" }), {
  status: 429, headers: retryAfter ? { "Retry-After": retryAfter } : {},
});

beforeEach(() => { vi.useFakeTimers(); vi.spyOn(Math, "random").mockReturnValue(0); });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("admission refusal", () => {
  it("waits out Retry-After and replays a mutation with its body", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(refusal("2")).mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = lycaonFetch(connection, "/v1/projects/p1", { method: "PATCH", body: "{\"name\":\"one\"}" });
    await vi.advanceTimersByTimeAsync(1_999);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect((await result).status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls.map(call => call[1].body)).toEqual(["{\"name\":\"one\"}", "{\"name\":\"one\"}"]);
  });

  it("backs off without a Retry-After header", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(refusal()).mockResolvedValueOnce(refusal()).mockResolvedValueOnce(new Response("{}"));
    vi.stubGlobal("fetch", fetchMock);
    const result = lycaonFetch(connection, "/v1/projects");
    await vi.advanceTimersByTimeAsync(500 + 1_000);
    expect((await result).status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("returns the refusal once its wait budget is spent", async () => {
    const fetchMock = vi.fn(async () => refusal("10"));
    vi.stubGlobal("fetch", fetchMock);
    const result = lycaonFetch(connection, "/v1/projects");
    await vi.advanceTimersByTimeAsync(30_000);
    const response = await result;
    expect(response.status).toBe(429);
    expect(await response.json()).toEqual({ code: "rate_limited" });
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  it("stops waiting when the caller aborts", async () => {
    const fetchMock = vi.fn(async () => refusal("5"));
    vi.stubGlobal("fetch", fetchMock);
    const abort = new AbortController();
    const result = lycaonFetch(connection, "/v1/projects", { signal: abort.signal });
    const rejected = expect(result).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(1_000);
    abort.abort();
    await rejected;
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not replay a one-shot stream body", async () => {
    const fetchMock = vi.fn(async () => refusal("1"));
    vi.stubGlobal("fetch", fetchMock);
    const body = new ReadableStream();
    const response = await lycaonFetch(connection, "/v1/projects", { method: "POST", body, duplex: "half" } as RequestInit);
    expect(response.status).toBe(429);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
