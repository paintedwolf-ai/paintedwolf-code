import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { ProviderMeta } from "../../../api/types.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { providers, kinds, bedrockKind, vertexKind, vertexExpressKind, bedrockProvider, renderCredentialModeCard } from "../../../test/provider-panel-fixture.tsx";



describe("ProvidersPanel API key status", () => {
  it("shows Key saved vs No key saved from credential_present", async () => {
    const settingsStore = createSettingsStore({
      providers,
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue(providers),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn().mockResolvedValue({ deleted: true }),
          } as never
        }
        settingsStore={settingsStore}
        providers={providers}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });

    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-key-status-openai")).toBeTruthy();
    });

    const saved = getByTestId("provider-key-status-openai");
    expect(saved.textContent).toBe(MODELS_SETTINGS_COPY.apiKeyStoredStatus);
    expect(saved.getAttribute("data-configured")).toBe("true");
    const savedInput = getByTestId("provider-api-key-openai") as HTMLInputElement;
    expect(savedInput.getAttribute("data-credential")).toBe("stored");
    expect(savedInput.placeholder).toBe(
      MODELS_SETTINGS_COPY.providerApiKeyPlaceholderStored,
    );

    fireEvent.click(getByTestId("providers-back"));
    await waitFor(() => {
      expect(getByTestId("providers-row-fireworks")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-fireworks"));
    await waitFor(() => {
      expect(getByTestId("provider-key-status-fireworks")).toBeTruthy();
    });
    const missing = getByTestId("provider-key-status-fireworks");
    expect(missing.textContent).toBe(MODELS_SETTINGS_COPY.apiKeyMissingStatus);
    expect(missing.getAttribute("data-configured")).toBe("false");
    expect(missing.classList.contains("den-settings-warn")).toBe(true);
    const missingInput = getByTestId(
      "provider-api-key-fireworks",
    ) as HTMLInputElement;
    expect(missingInput.getAttribute("data-credential")).toBe("missing");
    expect(missingInput.getAttribute("data-required")).toBe("true");
    expect(missingInput.placeholder).toBe(
      MODELS_SETTINGS_COPY.providerApiKeyPlaceholder,
    );

    fireEvent.click(getByTestId("providers-back"));
    await waitFor(() => {
      expect(getByTestId("providers-row-ollama")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-ollama"));
    await waitFor(() => {
      expect(getByTestId("provider-card-ollama")).toBeTruthy();
    });
    expect(
      getByTestId("providers-detail").querySelector(
        '[data-testid="provider-api-key-ollama"]',
      ),
    ).toBeNull();
    const ollamaEndpoint = getByTestId(
      "provider-endpoint-ollama",
    ) as HTMLInputElement;
    expect(ollamaEndpoint.value).toBe("http://localhost:11434/v1");
    expect(getByTestId("provider-save-endpoint-ollama")).toBeTruthy();
  });
});


describe("ProvidersPanel credential mode", () => {
  it("offers key-or-ambient on a kind with both, defaulting to the stored key", async () => {
    const provider = bedrockProvider();
    const { getByTestId, queryByTestId, updateProvider } =
      renderCredentialModeCard(provider, [bedrockKind]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    await waitFor(() => {
      expect(getByTestId("provider-credential-mode-bedrock")).toBeTruthy();
    });

    const keyRadio = getByTestId(
      "provider-credential-mode-api-key-bedrock",
    ) as HTMLInputElement;
    const ambientRadio = getByTestId(
      "provider-credential-mode-ambient-bedrock",
    ) as HTMLInputElement;
    expect(keyRadio.checked).toBe(true);
    expect(ambientRadio.checked).toBe(false);
    // Key mode shows the key field and no ambient explainer.
    expect(getByTestId("provider-api-key-bedrock")).toBeTruthy();
    expect(queryByTestId("provider-ambient-hint-bedrock")).toBeNull();

    fireEvent.click(ambientRadio);
    await waitFor(() => {
      expect(updateProvider).toHaveBeenCalledWith("bedrock", {
        requires_api_key: false,
      });
    });
  });

  it("explains the ambient chain in the user's terms, not as env vars only", async () => {
    const provider = bedrockProvider({
      requires_api_key: false,
      credential_present: false,
    });
    const { getByTestId, queryByTestId } = renderCredentialModeCard(provider, [
      bedrockKind,
    ]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    await waitFor(() => {
      expect(getByTestId("provider-ambient-hint-bedrock")).toBeTruthy();
    });

    const hint = getByTestId("provider-ambient-hint-bedrock").textContent ?? "";
    expect(hint).toContain("~/.aws");
    expect(hint).toContain("instance role");
    // Ambient mode with no stored key hides the key controls entirely.
    expect(queryByTestId("provider-api-key-bedrock")).toBeNull();
  });

  it("keeps a stored key reachable after switching to ambient", async () => {
    const provider = bedrockProvider({
      requires_api_key: false,
      credential_present: true,
    });
    const { getByTestId } = renderCredentialModeCard(provider, [bedrockKind]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    // Stored keys remain visible and clearable after a mode change.
    await waitFor(() => {
      expect(getByTestId("provider-api-key-bedrock")).toBeTruthy();
    });
  });

  it("shows no choice for an ambient-only kind", async () => {
    const provider: ProviderMeta = {
      id: "vertex",
      kind: "vertex",
      label: "Google Vertex AI",
      base_url: "",
      configured: true,
      ready_to_assign: false,
      credential_present: false,
      requires_api_key: false,
      ambient_auth: "google-adc",
      features: {
        tool_calls: true,
        thinking: false,
        prompt_cache: "none",
      },
      models: [],
    };
    const { getByTestId, queryByTestId } = renderCredentialModeCard(provider, [
      vertexKind,
    ]);

    await waitFor(() => {
      expect(getByTestId("providers-row-vertex")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-vertex"));
    await waitFor(() => {
      expect(getByTestId("provider-ambient-hint-vertex")).toBeTruthy();
    });
    expect(queryByTestId("provider-credential-mode-vertex")).toBeNull();
    expect(queryByTestId("provider-api-key-vertex")).toBeNull();
  });

  it("reads ambient resolution from the wire credential_source", async () => {
    const provider = bedrockProvider({
      requires_api_key: false,
      credential_present: false,
      credential_source: "ambient",
    });
    const { getByTestId } = renderCredentialModeCard(provider, [bedrockKind]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    await waitFor(() => {
      expect(getByTestId("provider-ambient-hint-bedrock")).toBeTruthy();
    });

    const hint = getByTestId("provider-ambient-hint-bedrock");
    expect(hint.getAttribute("data-ambient-resolved")).toBe("true");
    expect(hint.textContent).toContain(
      MODELS_SETTINGS_COPY.ambientCredentialsActive,
    );
  });

  it("says the ambient chain has not resolved when credential_source is none", async () => {
    const provider = bedrockProvider({
      requires_api_key: false,
      credential_present: false,
      configured: false,
      credential_source: "none",
    });
    const { getByTestId } = renderCredentialModeCard(provider, [bedrockKind]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    await waitFor(() => {
      expect(getByTestId("provider-ambient-hint-bedrock")).toBeTruthy();
    });

    const hint = getByTestId("provider-ambient-hint-bedrock");
    expect(hint.getAttribute("data-ambient-resolved")).toBe("false");
    expect(hint.textContent).toContain(
      MODELS_SETTINGS_COPY.ambientCredentialsMissing,
    );
  });

  it("shows host-observed credential verification in the status hints", async () => {
    const provider = bedrockProvider({
      credential_present: true,
      credential_source: "stored",
      credential_verified_at: "2026-09-01T12:00:00Z",
    });
    const { getByTestId } = renderCredentialModeCard(provider, [bedrockKind]);

    await waitFor(() => {
      expect(getByTestId("providers-row-bedrock")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-bedrock"));
    await waitFor(() => {
      expect(getByTestId("provider-feed-status-bedrock")).toBeTruthy();
    });

    expect(
      getByTestId("provider-feed-status-bedrock").textContent,
    ).toContain("Credentials verified");
  });

  // Express mode uses a key field without an ambient-credential choice.
  it("shows a plain key field for a key-only kind with no ambient chain", async () => {
    const provider: ProviderMeta = {
      id: "vertex-express",
      kind: "vertex-express",
      label: "Google Vertex AI (express mode)",
      base_url: "https://aiplatform.googleapis.com/v1",
      configured: false,
      ready_to_assign: false,
      credential_present: false,
      requires_api_key: true,
      features: {
        tool_calls: true,
        thinking: true,
        prompt_cache: "none",
      },
      models: [],
    };
    const { getByTestId, queryByTestId } = renderCredentialModeCard(provider, [
      vertexExpressKind,
    ]);

    await waitFor(() => {
      expect(getByTestId("providers-row-vertex-express")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-vertex-express"));
    await waitFor(() => {
      expect(getByTestId("provider-api-key-vertex-express")).toBeTruthy();
    });
    expect(queryByTestId("provider-ambient-hint-vertex-express")).toBeNull();
    expect(queryByTestId("provider-credential-mode-vertex-express")).toBeNull();
  });
});
