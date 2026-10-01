import { HttpResponse } from "msw";
import type { CreateMcpProviderRequest, PricingSourceMeta, SettingsPricing, UpdateMcpProviderRequest } from "../types.ts";
import {
  mockMcpCheck,
  mockMcpProviders,
  mockProjects,
  mockSession,
} from "./fixtures.ts";
import { operationHandler } from "./operation-handler.ts";
import { TEST_HOST_INFO } from "../../platform/connection/host-identity-test.ts";

const [fixtureProvider] = mockMcpProviders;
const provider = (id: string, fields: Partial<typeof fixtureProvider> = {}) => ({ ...fixtureProvider!, id, ...fields });
const pricingSource = (id: string, enabled: boolean, status: PricingSourceMeta["status"] = "offline"): PricingSourceMeta =>
  ({ id, label: id, kind: id, enabled, status });
const noContent = () => new HttpResponse(null, { status: 204 });

/** Default Den test host: each handler answers one OpenAPI operation. */
export const lycaonHandlers = [
  operationHandler("getHost", () => TEST_HOST_INFO),
  operationHandler("listProjects", () => ({ projects: mockProjects })),
  operationHandler("createSession", () => HttpResponse.json(mockSession, { status: 201 })),
  operationHandler("getSession", () => mockSession),
  operationHandler("listMcpRecipes", () => ({ recipes: [] })),
  operationHandler("listMcpProviders", () => ({ providers: mockMcpProviders })),
  operationHandler("createMcpProvider", async ({ request }) => {
    const body = (await request.json()) as CreateMcpProviderRequest;
    return HttpResponse.json(provider(body.id ?? body.recipe_id ?? "fixture", { enabled: body.enabled ?? false }), { status: 201 });
  }),
  operationHandler("updateMcpProvider", async ({ request, params }) => {
    const body = (await request.json()) as UpdateMcpProviderRequest;
    return provider(String(params.provider_id), { enabled: body.enabled ?? false });
  }),
  operationHandler("deleteMcpProvider", noContent),
  operationHandler("listMcpProviderTools", () => ({ tools: [{ name: "mcp_fixture_query", description: "query" }] })),
  operationHandler("refreshMcpProvider", ({ params }) => provider(String(params.provider_id), { enabled: true, status: "ready" })),
  operationHandler("startMcpOAuth", ({ params }) => ({
    authorize_url: `https://auth.example/authorize?client_id=dyn-1&state=st-${params.provider_id}`,
    state: `st-${params.provider_id}`,
    redirect_uri: "http://127.0.0.1:8766/mcp/oauth/callback",
  })),
  operationHandler("completeMcpOAuth", ({ params }) => provider(String(params.provider_id), { enabled: true, status: "ready" })),
  operationHandler("revokeMcpOAuth", noContent),
  operationHandler("checkMcpProviders", () => ({ providers: mockMcpCheck })),
  operationHandler("getPricingSettings", () => ({
    cost_tracking_enabled: true,
    sources: [
      { id: "models-dev", enabled: true },
      { id: "litellm", enabled: true },
      { id: "ai-pricing-fyi", enabled: true },
    ],
    available_sources: ["models-dev", "litellm", "ai-pricing-fyi"].map((id) => pricingSource(id, true)),
  })),
  operationHandler("updatePricingSettings", async ({ request }) => {
    const body = (await request.json()) as SettingsPricing;
    const sources = body.sources ?? [];
    return {
      cost_tracking_enabled: body.cost_tracking_enabled ?? false,
      cost_tracking_since_at: body.cost_tracking_enabled ? "2025-07-01T12:00:00Z" : undefined,
      sources,
      available_sources: sources.map((s) => pricingSource(s.id, s.enabled)),
    };
  }),
  operationHandler("refreshPricingSource", ({ params }) => ({
    ...pricingSource(String(params.source_id), true, "ok"),
    model_count: 1,
  })),
];
