import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { operationHandler } from "../api/mocks/operation-handler.ts";
import { providerModel } from "../test/provider-fixtures.ts";
import {
  createSettingsStore,
  INITIAL_SETTINGS_STATE,
} from "../store/settings-store.ts";
import { loadSettingsPanel } from "./settings-actions.ts";
import { mswServer } from "../api/mocks/setup.ts";
import { createLycaonClient } from "../api/client-impl.ts";
import { emptyProjects } from "../test/projects-fixture.ts";

const base = "http://127.0.0.1:8787";

describe("settings-actions MSW", () => {
  beforeAll(() => mswServer.listen({ onUnhandledRequest: "error" }));
  afterEach(() => mswServer.resetHandlers());
  afterAll(() => mswServer.close());

  it("loads model policy via MSW-backed client", async () => {
    mswServer.use(
      operationHandler("getModelPolicySettings", () => ({
        coordinator: { provider_id: "openai", model: "gpt-4o" },
        lite: { provider_id: "openai", model: "gpt-4o" },
        agent_pool: {
          selection: "round_robin" as const,
          models: [{ provider_id: "openai", model: "gpt-4o" }],
        },
      })),
      operationHandler("listProviders", () => ({
        providers: [
          {
            id: "openai",
            kind: "openai",
            features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" as const },
            configured: true,
            ready_to_assign: true,
            credential_present: true,
            requires_api_key: true,
            models: [providerModel("gpt-4o")],
          },
        ],
      })),
    );

    const client = createLycaonClient({
      baseUrl: base,
      apiToken: "tok",
    });
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });

    await loadSettingsPanel(settingsStore, client, emptyProjects, "providers");
    expect(settingsStore.state.modelPolicy?.coordinator?.model).toBe("gpt-4o");
    expect(settingsStore.state.providers[0]?.id).toBe("openai");

  });

  it("loads pricing settings into the store", async () => {
    const client = createLycaonClient({
      baseUrl: base,
      apiToken: "tok",
    });
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });

    await loadSettingsPanel(settingsStore, client, emptyProjects, "pricing");
    expect(settingsStore.state.pricing?.cost_tracking_enabled).toBe(true);
    expect(settingsStore.state.pricing?.available_sources?.length).toBeGreaterThan(
      0,
    );
  });
});
