import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { createLycaonClient } from "./client-impl.ts";
import { mswServer } from "./mocks/setup.ts";

describe("LycaonClient MSW integration", () => {
  beforeAll(() => mswServer.listen({ onUnhandledRequest: "error" }));
  afterEach(() => mswServer.resetHandlers());
  afterAll(() => mswServer.close());

  const connection = {
    baseUrl: "http://127.0.0.1:8787",
    apiToken: "msw-tok",
  };

  it("listProjects and createSession via OpenAPI-shaped mocks", async () => {
    const client = createLycaonClient(connection);
    const projects = await client.listProjects();
    expect(projects[0]?.id).toBe("proj-1");

    const session = await client.createSession({
      project_id: "proj-1",
      posture: "build",
    });
    expect(session.id).toBe("sess-1");
  });

  it("MCP list and check without live sidecar", async () => {
    const client = createLycaonClient(connection);
    const providers = await client.listMcpProviders();
    expect(providers[0]?.id).toBe("fixture");
    const rows = await client.checkMcpProviders();
    expect(rows[0]?.status).toBe("healthy");
  });

  it("pricing settings get/put/refresh without live sidecar", async () => {
    const client = createLycaonClient(connection);
    const got = await client.getPricingSettings();
    expect(got.cost_tracking_enabled).toBe(true);
    const put = await client.updatePricingSettings({
      cost_tracking_enabled: false,
      sources: [
        { id: "models-dev", enabled: true },
        { id: "litellm", enabled: true },
        { id: "ai-pricing-fyi", enabled: true },
      ],
    });
    expect(put.cost_tracking_enabled).toBe(false);
    const meta = await client.refreshPricingSource("models-dev");
    expect(meta.id).toBe("models-dev");
    expect(meta.status).toBe("ok");
  });
});
