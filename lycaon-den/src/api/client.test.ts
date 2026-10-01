import { describe, expect, it, vi } from "vitest";
import { at } from "../test/at.ts";
import { createLycaonClient } from "./client-impl.ts";
import { LycaonApiError } from "./http.ts";

describe("LycaonClient", () => {
  const connection = {
    baseUrl: "http://127.0.0.1:8787",
    apiToken: "test-token",
  };

  it("resolves artifact bytes without reusing a cached response after deletion", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("synthetic", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient(connection);
    await client.getSessionArtifact("session", "artifact");
    expect(fetchMock).toHaveBeenCalledWith(
      "http://127.0.0.1:8787/v1/sessions/session/artifacts/artifact",
      expect.objectContaining({ cache: "no-store" }),
    );
  });

  it("reads and refreshes the resources snapshot", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ resources: [] }), { status: 200 }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getHostResources();
    await client.refreshHostResources();
    await client.updateHostResource("docker", { access: "ask" });

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      "http://127.0.0.1:8787/v1/host-resources",
      "http://127.0.0.1:8787/v1/host-resources/refresh",
      "http://127.0.0.1:8787/v1/host-resources/docker",
    ]);
    expect((fetchMock.mock.calls[1]![1] as RequestInit).method).toBe("POST");
    expect((fetchMock.mock.calls[2]![1] as RequestInit).method).toBe("PATCH");
  });

  it("sends the pinned filter both ways and omits it when unset", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({ sessions: [], total: 0 }), { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.listProjectSessions("p", { pinned: true, sort: "pin" });
    await client.listProjectSessions("p", { pinned: false, sort: "created" });
    await client.listProjectSessions("p", { sort: "activity" });

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      "http://127.0.0.1:8787/v1/projects/p/sessions?pinned=true&sort=pin",
      "http://127.0.0.1:8787/v1/projects/p/sessions?pinned=false&sort=created",
      "http://127.0.0.1:8787/v1/projects/p/sessions?sort=activity",
    ]);
  });

  it("adds Authorization Bearer on /v1 requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ projects: [] }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.listProjects();

    expect(fetchMock).toHaveBeenCalledOnce();
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(headers.get("Authorization")).toBe("Bearer test-token");
    expect(at(fetchMock.mock.calls, 0)[0]).toBe("http://127.0.0.1:8787/v1/projects");
  });

  it("sends worker_id on source reads so a worker overlay is searched", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getProjectSource("proj-uuid", "internal/cli/root.go", {
      workerId: "job-uuid",
    });

    const url = String(at(fetchMock.mock.calls, 0)[0]);
    expect(url).toContain("path=internal%2Fcli%2Froot.go");
    expect(url).toContain("worker_id=job-uuid");
  });

  it("omits worker_id on source reads outside a worker branch", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getProjectSource("proj-uuid", "src/a.ts");

    expect(String(at(fetchMock.mock.calls, 0)[0])).not.toContain("worker_id");
  });

  it("scopes source and Git status reads to the session checkout", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({}), { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getProjectSource("proj-uuid", "src/a.ts", {
      sessionId: "sess-uuid",
    });
    await client.getGitStatus("proj-uuid", "repo-uuid", "sess-uuid");
    await client.getProjectSourceRaw("proj-uuid", "image.png", {
      sessionId: "sess-uuid",
    });

    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls).toHaveLength(3);
    for (const url of urls) expect(url).toContain("session_id=sess-uuid");
  });

  it("scopes Files workspace metadata to the session checkout", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({}), { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getSourceWorkspace("proj-uuid", "sess-uuid");
    await client.listGitRepos("proj-uuid", "sess-uuid");
    await client.getProjectSourceIndex("proj-uuid", { sessionId: "sess-uuid" });
    await client.getProjectAgentContext("proj-uuid", "root-uuid", {
      path: "src/main.ts",
      sessionId: "sess-uuid",
    });

    const urls = fetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls).toEqual([
      "http://127.0.0.1:8787/v1/projects/proj-uuid/source/workspace?session_id=sess-uuid",
      "http://127.0.0.1:8787/v1/projects/proj-uuid/git/repos?session_id=sess-uuid",
      "http://127.0.0.1:8787/v1/projects/proj-uuid/source/index?session_id=sess-uuid",
      "http://127.0.0.1:8787/v1/projects/proj-uuid/agent-context?root_id=root-uuid&path=src%2Fmain.ts&session_id=sess-uuid",
    ]);
  });

  it("passes session_id query param on board reads", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getBoard("proj-uuid", "sess-uuid");

    expect(fetchMock).toHaveBeenCalledOnce();
    const url = String(at(fetchMock.mock.calls, 0)[0]);
    expect(url).toContain("/v1/projects/proj-uuid/board");
    expect(url).toContain("session_id=sess-uuid");
  });

  it("follows worker history cursors within the selected project and session", async () => {
    const first = { id: "worker-1" };
    const second = { id: "worker-2" };
    const cursor = "opaque+/cursor";
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ workers: [first], next_cursor: cursor }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ workers: [second] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = createLycaonClient(connection);
    expect(await client.listWorkers("proj-uuid", { sessionId: "sess-1" })).toEqual([first, second]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const call of fetchMock.mock.calls) {
      const query = new URL(String(call[0])).searchParams;
      expect(query.get("project_id")).toBe("proj-uuid");
      expect(query.get("session_id")).toBe("sess-1");
      expect(query.get("limit")).toBe("500");
    }
    expect(new URL(String(at(fetchMock.mock.calls, 1)[0])).searchParams.get("cursor")).toBe(cursor);
  });

  it("passes stream identity and cancellation to fetch", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();
    const client = createLycaonClient(connection);

    await client.streamSession("session-1", {
      messageId: "message-1",
      signal: controller.signal,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(
      "http://127.0.0.1:8787/v1/sessions/session-1/stream?message=message-1",
    );
    expect(init.signal).toBe(controller.signal);
  });

  it("throws ApiError on non-2xx JSON body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ message: "nope", code: "bad" }), {
          status: 404,
          statusText: "Not Found",
        }),
      ),
    );

    const client = createLycaonClient(connection);
    await expect(client.getSession("missing")).rejects.toBeInstanceOf(
      LycaonApiError,
    );
  });

  it("uses /v1/settings/model-policy and the global approvals route", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ rules: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getModelPolicySettings();
    await client.updateApprovalsSettings({ rules: [], approval_posture: "strict" });

    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes("/v1/settings/model-policy"))).toBe(true);
    const permCall = fetchMock.mock.calls.find((c) =>
      String(c[0]).includes("/v1/settings/approvals"),
    );
    expect(permCall).toBeTruthy();
    expect((permCall![1] as RequestInit).method).toBe("PATCH");
  });

  it("fails closed when apiToken missing", async () => {
    const client = createLycaonClient({ baseUrl: connection.baseUrl, apiToken: "  " });
    await expect(client.listProjects()).rejects.toThrow(/token missing/i);
  });

  it("calls get/put/refresh pricing settings routes", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes("/refresh")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id: "models-dev",
              label: "models.dev",
              kind: "models-dev",
              enabled: true,
              status: "ok",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            cost_tracking_enabled: false,
            sources: [{ id: "models-dev", enabled: false }],
            available_sources: [
              {
                id: "models-dev",
                label: "models.dev",
                kind: "models-dev",
                enabled: false,
                status: "offline",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getPricingSettings();
    await client.updatePricingSettings({
      cost_tracking_enabled: true,
      sources: [{ id: "models-dev", enabled: true }],
    });
    await client.refreshPricingSource("models-dev");

    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls).toEqual([
      "http://127.0.0.1:8787/v1/settings/pricing",
      "http://127.0.0.1:8787/v1/settings/pricing",
      "http://127.0.0.1:8787/v1/settings/pricing/sources/models-dev/refresh",
    ]);
    expect((fetchMock.mock.calls[1]![1] as RequestInit).method).toBe("PATCH");
    expect((fetchMock.mock.calls[2]![1] as RequestInit).method).toBe("POST");
  });

  it("calls File summaries settings routes", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({ enabled: false }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getFileSummariesSettings();
    await client.updateFileSummariesSettings({ enabled: false });

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      "http://127.0.0.1:8787/v1/settings/file-summaries",
      "http://127.0.0.1:8787/v1/settings/file-summaries",
    ]);
    expect((fetchMock.mock.calls[1]![1] as RequestInit).method).toBe("PATCH");
  });

  it("calls power settings routes", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({
        keep_awake_while_working: true,
        supported: true,
        inhibiting: false,
        active_work_count: 0,
      }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient(connection);
    await client.getPowerSettings();
    await client.updatePowerSettings({ keep_awake_while_working: false });

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      "http://127.0.0.1:8787/v1/settings/power",
      "http://127.0.0.1:8787/v1/settings/power",
    ]);
    expect((fetchMock.mock.calls[1]![1] as RequestInit).method).toBe("PATCH");
  });
});
