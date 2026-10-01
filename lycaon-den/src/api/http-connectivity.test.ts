import { afterEach, describe, expect, it, vi } from "vitest";
import { lycaonFetch } from "./http.ts";
import {
  BackendTransportError,
  setBackendReachabilityObserver,
} from "../platform/connection/request-connectivity.ts";

const connection = { baseUrl: "http://127.0.0.1:8787", apiToken: "fixture" };

afterEach(() => {
  setBackendReachabilityObserver(null);
  vi.unstubAllGlobals();
});

describe("gateway reachability", () => {
  it.each([502, 503, 504])("does not replay a mutation after HTTP %i", async (status) => {
    const reachable = vi.fn();
    const unreachable = vi.fn();
    setBackendReachabilityObserver({ reachable, unreachable });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response("Gateway unavailable", { status }))
      .mockRejectedValueOnce(new TypeError("Connection refused"));
    vi.stubGlobal("fetch", fetchMock);

    await expect(lycaonFetch(connection, "/v1/projects/p1", { method: "PATCH" }))
      .rejects.toMatchObject({ name: "BackendTransportError", reachability: "unreachable" });

    expect(reachable).not.toHaveBeenCalled();
    expect(unreachable).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[0]?.[1].method).toBe("PATCH");
    expect(fetchMock.mock.calls[1]?.[0]).toBe(`${connection.baseUrl}/health`);
    expect(fetchMock.mock.calls[1]?.[1].method).toBeUndefined();
  });

  it.each([
    [503, JSON.stringify({ status: "ok" })],
    [200, "<html>Proxy landing page</html>"],
    [200, JSON.stringify({ status: "unknown" })],
  ])("does not accept an invalid health response (%i, %s)", async (status, body) => {
    vi.stubGlobal("fetch", vi.fn()
      .mockRejectedValueOnce(new TypeError("Network failure"))
      .mockResolvedValueOnce(new Response(body, { status })));

    await expect(lycaonFetch(connection, "/v1/projects"))
      .rejects.toMatchObject({ reachability: "unreachable" });
  });

  it.each(["ok", "recovery"])("preserves the API response when health is %s", async (status) => {
    const reachable = vi.fn();
    const unreachable = vi.fn();
    setBackendReachabilityObserver({ reachable, unreachable });
    const response = new Response(JSON.stringify({ error: "Store unavailable" }), { status: 503 });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(response)
      .mockResolvedValueOnce(new Response(JSON.stringify({ status }))));

    expect(await lycaonFetch(connection, "/v1/projects")).toBe(response);
    expect(await response.json()).toEqual({ error: "Store unavailable" });
    expect(reachable).toHaveBeenCalledOnce();
    expect(unreachable).not.toHaveBeenCalled();
  });

  it("does not probe or change connectivity for a cancelled request", async () => {
    const reachable = vi.fn();
    const unreachable = vi.fn();
    setBackendReachabilityObserver({ reachable, unreachable });
    const controller = new AbortController();
    const error = new DOMException("Cancelled", "AbortError");
    controller.abort(error);
    const fetchMock = vi.fn().mockRejectedValue(error);
    vi.stubGlobal("fetch", fetchMock);

    await expect(lycaonFetch(connection, "/v1/projects", { signal: controller.signal }))
      .rejects.toBe(error);
    expect(error).not.toBeInstanceOf(BackendTransportError);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(reachable).not.toHaveBeenCalled();
    expect(unreachable).not.toHaveBeenCalled();
  });
});
