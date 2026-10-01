import { stubClient } from "../../test/client-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ModelsEditor } from "./ModelsEditor.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";

const catalog: ProviderMeta[] = [
  {
    id: "openai",
    kind: "openai",
    label: "OpenAI",
    base_url: "https://api.openai.com/v1",
    configured: true,
    ready_to_assign: true,
    credential_present: true,
    requires_api_key: true,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("gpt-4o"), providerModel("gpt-4o-mini")],
  },
  {
    id: "ollama",
    kind: "ollama",
    label: "Ollama",
    base_url: "http://localhost:11434/v1",
    configured: true,
    ready_to_assign: true,
    credential_present: false,
    requires_api_key: false,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("llama3.1")],
  },
  {
    id: "fireworks",
    kind: "fireworks",
    label: "Fireworks",
    base_url: "https://api.fireworks.ai/inference/v1",
    configured: false,
    ready_to_assign: false,
    credential_present: false,
    requires_api_key: true,
    features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
    models: [providerModel("accounts/fireworks/models/llama-v3p1-70b-instruct")],
  },
];

const policy: ModelPolicy = {
  coordinator: { provider_id: "openai", model: "gpt-4o-mini" },
  lite: { provider_id: "openai", model: "gpt-4o-mini" },
  agent_pool: {
    selection: "round_robin",
    models: [{ provider_id: "openai", model: "gpt-4o-mini" }],
  },
};

function mockClient(overrides: Partial<LycaonClient> = {}) {
  return stubClient({
    updateModelPolicySettings: vi.fn(),
    getModelPolicySettings: vi.fn().mockResolvedValue(policy),
    replaceProviderCredential: vi.fn(),
    deleteProviderCredential: vi.fn(),
    listProviders: vi.fn(),
    testProvider: vi.fn(),
    ...overrides,
  });
}

function renderEditor(
  settingsStore: ReturnType<typeof createSettingsStore>,
  client: LycaonClient = mockClient(),
) {
  const appStore = createAppStore();
  return render(() => (
    <ModelsEditor
      projects={mockProjectsStore([wireProject("/tmp/demo")])}
      client={client}
      appStore={appStore}
      settingsStore={settingsStore}
      projectDir="/tmp/demo"
    />
  ));
}

describe("ModelsEditor", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows main override off and hides editors for an empty project overlay", async () => {
    const emptyPolicy: ModelPolicy = {
      coordinator: { provider_id: "", model: "" },
      lite: { provider_id: "", model: "" },
      agent_pool: { selection: "first", models: [] },
    };
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: emptyPolicy,
    });
    const client = mockClient({
      updateModelPolicySettings: vi.fn().mockImplementation(async (next: ModelPolicy) => next),
    });

    const { getByTestId, queryByTestId } = renderEditor(settingsStore, client);

    await waitFor(() => {
      expect(getByTestId("project-settings-override")).toBeTruthy();
    });
    expect(getByTestId("settings-scope-badge").textContent).toBe("Project");
    expect(getByTestId("project-settings-override").getAttribute("data-enabled")).toBe(
      "false",
    );
    expect(queryByTestId("coordinator-model-picker")).toBeNull();
    // Empty overlay does not PUT.
    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(400);
    expect(client.updateModelPolicySettings).not.toHaveBeenCalled();
  });

  it("shows summarizer model slot when override is on", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });

    const { getByTestId } = renderEditor(settingsStore);

    await waitFor(() => {
      expect(getByTestId("lite-model-picker")).toBeTruthy();
    });
  });

  it("shows role and worker pickers when override is on", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });

    const { getByTestId, queryByTestId } = renderEditor(settingsStore);

    await waitFor(() => {
      expect(getByTestId("lite-model-picker")).toBeTruthy();
      expect(getByTestId("coordinator-model-picker")).toBeTruthy();
      expect(getByTestId("worker-model-picker")).toBeTruthy();
    });
    expect(getByTestId("models-role-section")).toBeTruthy();
    expect(getByTestId("models-worker-section")).toBeTruthy();
    expect(queryByTestId("models-worker-pool-panel")).toBeNull();
    expect(queryByTestId("pool-add-row")).toBeNull();
    expect(queryByTestId("routing-coordinator-model-picker")).toBeNull();
  });

  it("shows the saved coordinator model in the picker", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });

    const { getByTestId } = renderEditor(settingsStore);

    await waitFor(() => {
      const picker = getByTestId("coordinator-model-picker");
      expect(picker.getAttribute("data-value")).toContain("openai");
      expect(picker.getAttribute("data-value")).toContain("gpt-4o-mini");
    });
  });

  it("reveals worker pool controls and auto-saves when the pool toggle is on", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });
    const client = mockClient({
      updateModelPolicySettings: vi.fn().mockImplementation(async (next: ModelPolicy) => next),
    });

    const { getByTestId, queryByTestId } = renderEditor(settingsStore, client);

    await waitFor(() => getByTestId("models-worker-pool-toggle"));
    expect(queryByTestId("models-worker-pool-panel")).toBeNull();

    fireEvent.click(getByTestId("models-worker-pool-toggle"));

    await waitFor(
      () => {
        expect(getByTestId("models-worker-pool-panel")).toBeTruthy();
        expect(getByTestId("pool-add-row")).toBeTruthy();
      },
      { timeout: 3000 },
    );

    await waitFor(
      () => {
        expect(client.updateModelPolicySettings).toHaveBeenCalled();
      },
      { timeout: 3000 },
    );
    expect(queryByTestId("models-save")).toBeNull();
  });

  it("auto-saves lite model slot changes", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });
    const client = mockClient({
      updateModelPolicySettings: vi.fn().mockImplementation(async (next: ModelPolicy) => next),
    });

    const { getByTestId } = renderEditor(settingsStore, client);

    await waitFor(() => getByTestId("lite-model-picker"));
    fireEvent.click(getByTestId("lite-model-picker"));
    await waitFor(() => {
      expect(screen.getByTestId("lite-model-picker-list")).toBeTruthy();
    });
    fireEvent.click(
      screen.getByTestId("lite-model-picker-option-ollama-llama3.1"),
    );

    await waitFor(
      () => {
        expect(client.updateModelPolicySettings).toHaveBeenCalled();
      },
      { timeout: 3000 },
    );
  });

  it("keeps ModelPolicyPanel mounted across successive field edits", async () => {
    // Use a fresh policy because the settings store mutates its input object.
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: {
        coordinator: { provider_id: "openai", model: "gpt-4o-mini" },
        lite: { provider_id: "openai", model: "gpt-4o-mini" },
        agent_pool: {
          selection: "round_robin",
          models: [{ provider_id: "openai", model: "gpt-4o-mini" }],
        },
      },
    });
    const client = mockClient({
      updateModelPolicySettings: vi.fn().mockImplementation(async (next: ModelPolicy) => next),
    });

    const { getByTestId } = renderEditor(settingsStore, client);

    await waitFor(() => getByTestId("lite-model-picker"));
    // Every edit yields a new draft object; the panel's DOM must survive two edits.
    const panelBefore = getByTestId("model-policy-panel");

    fireEvent.click(getByTestId("lite-model-picker"));
    await waitFor(() => screen.getByTestId("lite-model-picker-list"));
    fireEvent.click(
      screen.getByTestId("lite-model-picker-option-ollama-llama3.1"),
    );
    await waitFor(
      () => expect(client.updateModelPolicySettings).toHaveBeenCalledTimes(1),
      { timeout: 3000 },
    );
    expect(getByTestId("model-policy-panel")).toBe(panelBefore);

    await waitFor(() => expect((getByTestId("coordinator-model-picker") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(getByTestId("coordinator-model-picker"));
    await waitFor(() => screen.getByTestId("coordinator-model-picker-list"));
    fireEvent.click(
      screen.getByTestId("coordinator-model-picker-option-ollama-llama3.1"),
    );
    await waitFor(
      () => expect(client.updateModelPolicySettings).toHaveBeenCalledTimes(2),
      { timeout: 3000 },
    );
    expect(getByTestId("model-policy-panel")).toBe(panelBefore);
  });

  it("does not persist when the provider list changes", async () => {
    const settingsStore = createSettingsStore({
      providers: catalog,
      modelPolicy: policy,
    });
    const client = mockClient({
      updateModelPolicySettings: vi.fn().mockImplementation(async (next: ModelPolicy) => next),
    });

    renderEditor(settingsStore, client);
    await waitFor(() => screen.getByTestId("coordinator-model-picker"));
    vi.useFakeTimers();
    settingsStore.actions.setProviders([]);
    await vi.advanceTimersByTimeAsync(400);
    expect(client.updateModelPolicySettings).not.toHaveBeenCalled();
  });
});
