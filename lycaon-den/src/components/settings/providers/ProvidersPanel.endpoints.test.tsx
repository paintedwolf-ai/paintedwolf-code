import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { ProviderMeta } from "../../../api/types.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { providerModel } from "../../../test/provider-fixtures.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { providers, kinds, bedrockKind, vertexKind, renderCredentialModeCard } from "../../../test/provider-panel-fixture.tsx";



describe("ProvidersPanel provider endpoint semantics", () => {
  function renderAddForm(
    kindList: unknown[],
    createProvider: ReturnType<typeof vi.fn>,
  ) {
    const settingsStore = createSettingsStore({ providers: [] });
    render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue(kindList),
            createProvider,
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));
  }

  it("treats a Bedrock region as a region rather than an HTTP URL", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    renderAddForm([bedrockKind], createProvider);

    fireEvent.click(await screen.findByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-bedrock"));
    const input = (await screen.findByTestId(
      "add-provider-base-url",
    )) as HTMLInputElement;
    expect(input.value).toBe("us-east-1");
    expect(
      screen.getByText(MODELS_SETTINGS_COPY.providerRegionLabel),
    ).toBeTruthy();
    fireEvent.click(screen.getByTestId("add-provider-btn"));

    await waitFor(() => {
      expect(createProvider).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "bedrock", base_url: "us-east-1" }),
      );
    });
  });

  it("omits a derived Vertex endpoint", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    renderAddForm([vertexKind], createProvider);

    fireEvent.click(await screen.findByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-vertex"));
    await screen.findByTestId("add-provider-derived-endpoint-hint");
    expect(screen.queryByTestId("add-provider-base-url")).toBeNull();
    fireEvent.click(screen.getByTestId("add-provider-btn"));

    await waitFor(() => expect(createProvider).toHaveBeenCalled());
    const request = createProvider.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(request.kind).toBe("vertex");
    expect(request).not.toHaveProperty("base_url");
  });

  it("requires and preserves Azure deployment names", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const azureKind = {
      kind: "azure",
      label: "Azure OpenAI",
      base_url: "https://YOUR_RESOURCE.openai.azure.com",
      endpoint_style: "url",
      requires_api_key: true,
    };
    renderAddForm([azureKind], createProvider);

    fireEvent.click(await screen.findByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-azure"));
    const addButton = screen.getByTestId("add-provider-btn") as HTMLButtonElement;
    expect(addButton.disabled).toBe(true);
    fireEvent.input(screen.getByTestId("add-provider-models"), {
      target: { value: "chat-prod=gpt-4.1, summarizer" },
    });
    expect(addButton.disabled).toBe(false);
    fireEvent.click(addButton);

    await waitFor(() => {
      expect(createProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "azure",
          models: [
            { id: "chat-prod", priced_as: "gpt-4.1" },
            { id: "summarizer" },
          ],
        }),
      );
    });
  });

  it("edits the configured Azure deployment list independently of discovery", async () => {
    const azure: ProviderMeta = {
      id: "azure-work",
      kind: "azure",
      label: "Azure work",
      base_url: "https://work.openai.azure.com",
      endpoint_style: "url",
      configured: true,
      ready_to_assign: true,
      credential_present: true,
      requires_api_key: true,
      configured_models: [providerModel("chat-prod", { priced_as: "gpt-4.1" })],
      features: {
        tool_calls: true,
        thinking: true,
        prompt_cache: "automatic_prefix",
      },
      models: [providerModel("chat-prod", { priced_as: "gpt-4.1" })],
    };
    const updateProvider = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({ providers: [azure] });
    const azureKind = {
      kind: "azure",
      label: "Azure OpenAI",
      base_url: "https://YOUR_RESOURCE.openai.azure.com",
      endpoint_style: "url",
      requires_api_key: true,
    };
    render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([azure]),
            listProviderKinds: vi.fn().mockResolvedValue([azureKind]),
            updateProvider,
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[azure]}
        onRemoveProvider={() => undefined}
      />
    ));

    await screen.findByText("Azure OpenAI");
    fireEvent.click(await screen.findByTestId("providers-row-azure-work"));
    const input = (await screen.findByTestId(
      "provider-models-azure-work",
    )) as HTMLInputElement;
    expect(input.value).toBe("chat-prod=gpt-4.1");
    fireEvent.input(input, {
      target: { value: "chat-prod=gpt-4.1, summarizer" },
    });
    fireEvent.click(screen.getByTestId("provider-save-models-azure-work"));

    await waitFor(() => {
      expect(updateProvider).toHaveBeenCalledWith("azure-work", {
        models: [
          { id: "chat-prod", priced_as: "gpt-4.1" },
          { id: "summarizer" },
        ],
      });
    });
  });
});


describe("ProvidersPanel secret screen trust", () => {
  it("reads the wire decision and sends a toggle as the new decision", async () => {
    const ollama: ProviderMeta = { ...providers[2]!, secret_screen_trusted: false };
    const { getByTestId, updateProvider } = renderCredentialModeCard(
      ollama,
      kinds,
    );
    await waitFor(() => {
      expect(getByTestId("providers-row-ollama")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-ollama"));
    await waitFor(() => {
      expect(getByTestId("provider-secret-trust-ollama")).toBeTruthy();
    });
    const box = getByTestId("provider-secret-trust-ollama") as HTMLInputElement;
    expect(box.checked).toBe(false);
    fireEvent.click(box);
    await waitFor(() => {
      expect(updateProvider).toHaveBeenCalledWith("ollama", {
        secret_screen_trusted: true,
      });
    });
  });

  it("shows a trusted provider as trusted and withdraws on untick", async () => {
    const ollama: ProviderMeta = { ...providers[2]!, secret_screen_trusted: true };
    const { getByTestId, updateProvider } = renderCredentialModeCard(
      ollama,
      kinds,
    );
    await waitFor(() => {
      expect(getByTestId("providers-row-ollama")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-ollama"));
    const box = (await waitFor(() =>
      getByTestId("provider-secret-trust-ollama"),
    )) as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(box);
    await waitFor(() => {
      expect(updateProvider).toHaveBeenCalledWith("ollama", {
        secret_screen_trusted: false,
      });
    });
  });

  it("says what the endpoint address does and does not prove", async () => {
    const local = renderCredentialModeCard(providers[2]!, kinds);
    await waitFor(() => {
      expect(local.getByTestId("providers-row-ollama")).toBeTruthy();
    });
    fireEvent.click(local.getByTestId("providers-row-ollama"));
    const localHint = await waitFor(
      () => local.getByTestId("provider-secret-trust-hint-ollama").textContent ?? "",
    );
    expect(localHint).toContain(MODELS_SETTINGS_COPY.secretScreenTrustHint);
    expect(localHint).toContain(
      MODELS_SETTINGS_COPY.secretScreenTrustLoopbackNote,
    );
    local.unmount();

    const remote = renderCredentialModeCard(providers[0]!, kinds);
    await waitFor(() => {
      expect(remote.getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(remote.getByTestId("providers-row-openai"));
    const remoteHint = await waitFor(
      () => remote.getByTestId("provider-secret-trust-hint-openai").textContent ?? "",
    );
    expect(remoteHint).toContain(MODELS_SETTINGS_COPY.secretScreenTrustHint);
    expect(remoteHint).toContain(MODELS_SETTINGS_COPY.secretScreenTrustRemoteNote);
    expect(remoteHint).not.toContain(
      MODELS_SETTINGS_COPY.secretScreenTrustLoopbackNote,
    );
  });
});
