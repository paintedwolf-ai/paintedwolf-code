import { afterEach, describe, expect, it, vi } from "vitest";
import {
  formatQuery,
  lycaonBlob,
  lycaonDownload,
  lycaonFetch,
  lycaonJson,
} from "./http.ts";
import {
  BackendTransportError,
  setBackendReachabilityObserver,
} from "../platform/connection/request-connectivity.ts";

describe("formatQuery", () => {
  it("returns empty string when no params", () => {
    expect(formatQuery({})).toBe("");
  });

  it("omits absent and empty values", () => {
    expect(
      formatQuery({
        scope: undefined,
        cursor: null,
        project_id: "",
      }),
    ).toBe("");
  });

  it("sends false, which a tri-state filter reads as a value", () => {
    expect(formatQuery({ pinned: false })).toBe("?pinned=false");
  });

  it("encodes present params with leading question mark", () => {
    expect(formatQuery({ scope: "global", project_id: "p1" })).toBe(
      "?scope=global&project_id=p1",
    );
  });

  it("serializes true booleans as true", () => {
    expect(formatQuery({ dry_run: true })).toBe("?dry_run=true");
  });

  it("serializes numbers", () => {
    expect(formatQuery({ limit: 10 })).toBe("?limit=10");
  });
});

describe("lycaonFetch", () => {
  afterEach(() => {
    setBackendReachabilityObserver(null);
    vi.unstubAllGlobals();
  });

  it("adds Authorization Bearer on /v1 routes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await lycaonFetch(
      { baseUrl: "http://127.0.0.1:8787", apiToken: "secret-bearer" },
      "/v1/projects",
    );

    expect(fetchMock).toHaveBeenCalledOnce();
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Headers).get("Authorization")).toBe(
      "Bearer secret-bearer",
    );
  });

  it("fails fast when apiToken is missing", async () => {
    await expect(
      lycaonFetch({ baseUrl: "http://127.0.0.1:8787", apiToken: "  " }, "/v1/projects"),
    ).rejects.toThrow(/token missing/i);
  });

  it("rejects non-/v1 paths", async () => {
    await expect(
      lycaonFetch(
        { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
        "/health",
      ),
    ).rejects.toThrow(/only serves \/v1/);
  });

  it("stamps confirmed backend unreachability at the fetch boundary", async () => {
    const unreachable = vi.fn();
    setBackendReachabilityObserver({ reachable: vi.fn(), unreachable });
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("localized")));

    const error = await lycaonFetch(
      { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
      "/v1/projects",
    ).catch((err: unknown) => err);

    expect(error).toBeInstanceOf(BackendTransportError);
    expect((error as BackendTransportError).reachability).toBe("unreachable");
    expect(unreachable).toHaveBeenCalledOnce();
  });

  it("does not mark the backend offline when the independent health probe answers", async () => {
    const reachable = vi.fn();
    const unreachable = vi.fn();
    setBackendReachabilityObserver({ reachable, unreachable });
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("localized"))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: "recovery" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const error = await lycaonFetch(
      { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
      "/v1/projects",
    ).catch((err: unknown) => err);

    expect((error as BackendTransportError).reachability).toBe("reachable");
    expect(reachable).toHaveBeenCalledOnce();
    expect(unreachable).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls[1]?.[0]).toBe("http://127.0.0.1:8787/health");
  });
});

describe("lycaonJson", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("parses JSON success bodies", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ id: "p1" }), { status: 200 }),
      ),
    );
    const project = await lycaonJson<{ id: string }>(
      { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
      "/v1/projects/p1",
    );
    expect(project.id).toBe("p1");
  });

  it("classifies a connection lost during the JSON body as a transport failure", async () => {
    const broken = new Response(new ReadableStream({ start(controller) { controller.error(new TypeError("connection reset")); } }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(broken).mockResolvedValueOnce(new Response('{"status":"ok"}')));
    await expect(lycaonJson({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok" }, "/v1/projects/p1"))
      .rejects.toBeInstanceOf(BackendTransportError);
  });

  it.each(["headers", "body"])("retains the timeout reason when fetching %s reports an abort", async (phase) => {
    const controller = new AbortController();
    const timeout = new DOMException("Request timed out", "TimeoutError");
    const response = new Response("{}");
    const abort = async () => {
      controller.abort(timeout);
      throw new DOMException("Body aborted", "AbortError");
    };
    vi.spyOn(response, "json").mockImplementation(abort);
    vi.stubGlobal("fetch", phase === "headers" ? vi.fn(abort) : vi.fn().mockResolvedValue(response));
    await expect(lycaonJson({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok" }, "/v1/projects/p1", { signal: controller.signal }))
      .rejects.toBe(timeout);
  });

  it("does not mistake malformed JSON for a network outage", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("invalid JSON"));
    vi.stubGlobal("fetch", fetchMock);
    await expect(lycaonJson({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok" }, "/v1/projects/p1"))
      .rejects.toBeInstanceOf(SyntaxError);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("shares one request between concurrent GETs of the same URL", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(
        () =>
          new Promise((resolve) =>
            setTimeout(
              () => resolve(new Response(JSON.stringify({ id: "p1" }), { status: 200 })),
              0,
            ),
          ),
      );
    vi.stubGlobal("fetch", fetchMock);
    const conn = { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" };

    const [a, b] = await Promise.all([
      lycaonJson<{ id: string }>(conn, "/v1/projects/p1"),
      lycaonJson<{ id: string }>(conn, "/v1/projects/p1"),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(a.id).toBe("p1");
    expect(b.id).toBe("p1");
    expect(b).not.toBe(a);
  });

  it("goes back to the network once the shared request settles", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(
        async () => new Response(JSON.stringify({ id: "p1" }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);
    const conn = { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" };

    await lycaonJson(conn, "/v1/projects/p1");
    await lycaonJson(conn, "/v1/projects/p1");

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("does not share writes or aborted-signal reads", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(
        () =>
          new Promise((resolve) =>
            setTimeout(() => resolve(new Response(null, { status: 204 })), 0),
          ),
      );
    vi.stubGlobal("fetch", fetchMock);
    const conn = { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" };

		await Promise.all([
			lycaonJson(conn, "/v1/projects/p1", { method: "PATCH" }),
			lycaonJson(conn, "/v1/projects/p1", { method: "PATCH" }),
      lycaonJson(conn, "/v1/projects/p1", { signal: new AbortController().signal }),
      lycaonJson(conn, "/v1/projects/p1", { signal: new AbortController().signal }),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  it("preserves structured errors for binary responses", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            message: "Export blocked",
            code: "export_blocked",
            title: "Export unavailable",
            retryable: true,
            suggested_action: "Retry later",
            scope: "session",
            resolution: "Wait for the active turn",
            tier: "non_catastrophic",
            details: { session_id: "s1" },
          }),
          { status: 409, statusText: "Conflict" },
        ),
      ),
    );

    await expect(
      lycaonBlob(
        { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
        "/v1/sessions/s1/export",
      ),
    ).rejects.toMatchObject({
      message: "Export blocked",
      status: 409,
      code: "export_blocked",
      title: "Export unavailable",
      retryable: true,
      suggestedAction: "Retry later",
      scope: "session",
      resolution: "Wait for the active turn",
      tier: "non_catastrophic",
      details: { session_id: "s1" },
    });
  });
});

describe("lycaonDownload", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads encoded, quoted, and fallback filenames", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response("one", {
          headers: {
            "Content-Disposition": "attachment; filename*=UTF-8''report%20one.pdf",
          },
        }),
      )
      .mockResolvedValueOnce(
        new Response("two", {
          headers: { "Content-Disposition": 'attachment; filename="report-two.pdf"' },
        }),
      )
      .mockResolvedValueOnce(new Response("three"));
    vi.stubGlobal("fetch", fetchMock);
    const connection = {
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    };

		expect((await lycaonDownload(connection, "/v1/sessions/s1/export", "fallback.pdf")).filename)
			.toBe("report one.pdf");
		expect((await lycaonDownload(connection, "/v1/sessions/s1/export", "fallback.pdf")).filename)
			.toBe("report-two.pdf");
		expect((await lycaonDownload(connection, "/v1/sessions/s1/export", "fallback.pdf")).filename)
			.toBe("fallback.pdf");
  });
});

describe("createLycaonClient", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("listProjects uses bearer auth", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ projects: [] }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { createLycaonClient } = await import("./client-impl.ts");
    await createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "client-tok",
    }).listProjects();
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Headers).get("Authorization")).toBe(
      "Bearer client-tok",
    );
  });
});
