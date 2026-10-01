// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createLycaonClient } from "./client-impl.ts";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("concurrent GET requests", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shares an in-flight response for the same resource and evicts it after settle", async () => {
    let resolveFetch: ((response: Response) => void) | undefined;
    const fetchMock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });

    const first = client.getSessionBootstrap("s1");
    const second = client.getSessionBootstrap("s1");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    resolveFetch?.(jsonResponse({ session: { id: "s1" } }));
    await Promise.all([first, second]);

    const third = client.getSessionBootstrap("s1");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    resolveFetch?.(jsonResponse({ session: { id: "s1" } }));
    await third;
  });

  it("gives a signaled bootstrap read its own request", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ session: { id: "s1" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });

    await Promise.all([
      client.getSessionBootstrap("s1", { signal: new AbortController().signal }),
      client.getSessionBootstrap("s1", { signal: new AbortController().signal }),
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("joins a shared bootstrap read and aborts only the signaled caller", async () => {
    let resolveFetch: ((response: Response) => void) | undefined;
    const fetchMock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });

    const shared = client.getSessionBootstrap("s1");
    const controller = new AbortController();
    const joined = client.getSessionBootstrap("s1", { signal: controller.signal });
    expect(fetchMock).toHaveBeenCalledTimes(1);

    controller.abort(new Error("superseded"));
    await expect(joined).rejects.toThrow("superseded");
    resolveFetch?.(jsonResponse({ session: { id: "s1" } }));
    await expect(shared).resolves.toMatchObject({ session: { id: "s1" } });
  });

  it("passes file-history cancellation through to its transport", async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn((_url: unknown, init?: RequestInit) => {
      const received = init?.signal;
      return new Promise<Response>((_resolve, reject) => {
        received?.addEventListener("abort", () => {
          reject(received.reason instanceof Error ? received.reason : new Error("Request aborted"));
        }, { once: true });
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({ baseUrl: "http://127.0.0.1:8787", apiToken: "token" });
    const request = client.listProjectSourceVersions("project", { fileId: "file" }, { lane: "git", signal: controller.signal });
    const rejected = expect(request).rejects.toThrow("closed tab");
    controller.abort(new Error("closed tab"));
    await rejected;
    expect(fetchMock.mock.calls[0]?.[1]?.signal?.aborted).toBe(true);
  });

  it("never deduplicates writes", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "s1" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });

    await Promise.all([
      client.updateSession("s1", { title: "one" }),
      client.updateSession("s1", { title: "two" }),
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe("managed secret writes", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("uses PATCH for metadata and PUT for value replacement", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ state: "active" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });
    const details = { name: "Updated fixture", purpose: "Settings test" };
    const replacement = { secret_value: "disposable-fixture" };

    await client.updateProjectManagedSecret("p1", "secret1", details);
    await client.replaceProjectManagedSecretValue("p1", "secret1", replacement);

    expect(fetchMock.mock.calls).toEqual([
      ["http://127.0.0.1:8787/v1/projects/p1/secrets/secret1", expect.objectContaining({
        method: "PATCH", body: JSON.stringify(details),
      })],
      ["http://127.0.0.1:8787/v1/projects/p1/secrets/secret1/value", expect.objectContaining({
        method: "PUT", body: JSON.stringify(replacement),
      })],
    ]);
  });
});

describe("detachProjectRoot", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("sends the explicit force query only for confirmed removal", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: "p1", roots: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });

    await client.detachProjectRoot("p1", "r1", { force: true });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "http://127.0.0.1:8787/v1/projects/p1/roots/r1?force=true",
    );
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: "DELETE" });
  });
});

describe("getContributions", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("sends If-None-Match on a second device fetch", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        const match = new Headers(init?.headers).get("If-None-Match");
        if (match) return new Response(null, { status: 304 });
        return jsonResponse({ frame_revision: "frame-1" });
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "token",
    });
    await client.getContributions();
    await client.getContributions();
    const second = fetchMock.mock.calls[1]?.[1] as RequestInit | undefined;
    expect(new Headers(second?.headers).get("If-None-Match")).toBe('"frame-1"');
  });
});

describe("review comparison selectors", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("preserves a zero unread boundary and keeps reviewed history separate from scope mode", async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse({ in_range: false, location_changed: false }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient({ baseUrl: "http://127.0.0.1:8787", apiToken: "fixture-token" });
    await client.getProjectSourceComparison("p1", {
      fileId: "file", baseline: "presentation", presentationAfterOrdinal: 0,
    });
    const unread = new URL(fetchMock.mock.calls[0]![0] as string).searchParams;
    expect(unread.get("presentation_after_ordinal")).toBe("0");
    expect(unread.has("reviewed_through_ordinal")).toBe(false);
    await client.getProjectSourceComparison("p1", { fileId: "file", reviewedThroughOrdinal: 4 });
    const reviewed = new URL(fetchMock.mock.calls[1]![0] as string).searchParams;
    expect(reviewed.get("reviewed_through_ordinal")).toBe("4");
    expect(reviewed.has("baseline")).toBe(false);
    expect(reviewed.has("presentation_after_ordinal")).toBe(false);
  });
});
