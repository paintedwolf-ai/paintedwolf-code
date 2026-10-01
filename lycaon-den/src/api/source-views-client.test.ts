import { describe, expect, it, vi } from "vitest";
import { createSourceViewsClient, encodeSourceViewAnchor } from "./source-views-client.ts";

function decodeAnchor(value: string): unknown {
  const binary = atob(value.replaceAll("-", "+").replaceAll("_", "/"));
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(binary, char => char.charCodeAt(0))));
}

describe("source view transport", () => {
  it("preserves Unicode paths without treating an anchor as authority", () => {
    const address = { root_id: "root", path: ".paintedwolf/配置/😀.yaml" };
    expect(decodeAnchor(encodeSourceViewAnchor(address))).toEqual(address);
  });

  it("sends disclosure updates and releases views", async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = [];
    const json = <T>(path: string, init?: RequestInit): Promise<T> => {
      calls.push({ path, init });
      return Promise.resolve(undefined as T);
    };
    const client = createSourceViewsClient(json);
    const request = { kind: "tree" as const, operation_id: "request", expected_intent_revision: "intent", command: { kind: "disclose" as const, disclosures: [{ address: { root_id: "root", path: "." }, open: true, recursive: true }] } };
    await client.applySourceViewIntent("project", "view", request);
    await client.releaseSourceView("project", "view");
    expect(calls).toEqual([
      { path: "/v1/projects/project/source/views/view/apply", init: { method: "POST", body: JSON.stringify(request) } },
      { path: "/v1/projects/project/source/views/view", init: { method: "DELETE" } },
    ]);
  });

  it("sends a stable basis and source anchor with viewport cancellation", async () => {
    const json = vi.fn().mockResolvedValue({});
    const client = createSourceViewsClient(json);
    const controller = new AbortController();
    await client.getSourceViewRows("project", "view", { presentation_id: "presentation", offset: 9_000_000, limit: 100, anchor: { row: 123 }, retain: [{ end: 200, fingerprint: "a".repeat(64) }] }, controller.signal);
    const [path, init] = json.mock.calls[0]!;
    const url = new URL(path, "http://localhost");
    expect(url.pathname).toBe("/v1/projects/project/source/views/view/presentations/presentation/rows");
    expect(url.searchParams.get("offset")).toBe("9000000");
    expect(decodeAnchor(url.searchParams.get("anchor")!)).toEqual({ row: 123 });
    expect(init.signal).toBe(controller.signal);
    expect(JSON.parse(url.searchParams.get("retain")!)).toEqual([{ end: 200, fingerprint: "a".repeat(64) }]);
  });
});
