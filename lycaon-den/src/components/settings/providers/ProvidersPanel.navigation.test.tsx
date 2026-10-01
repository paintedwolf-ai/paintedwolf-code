import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { providers, kinds } from "../../../test/provider-panel-fixture.tsx";



describe("ProvidersPanel identity-replace remount", () => {
  it("keeps the open card mounted (and a focused field focused) across a Test action", async () => {
    const openai = providers[0]!;
    // Refreshed provider objects preserve the focused card.
    const testProvider = vi.fn().mockResolvedValue({
      ok: true,
      latency_ms: 7,
      models: 1,
      provider: { ...openai },
    });
    const settingsStore = createSettingsStore({ providers: [openai] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([openai]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider,
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={settingsStore.state.providers}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-card-openai")).toBeTruthy();
    });
    const cardBefore = getByTestId("provider-card-openai");
    const apiKeyInput = getByTestId("provider-api-key-openai") as HTMLInputElement;
    apiKeyInput.focus();
    expect(document.activeElement).toBe(apiKeyInput);

    fireEvent.click(getByTestId("provider-test-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-test-result-openai")).toBeTruthy();
    });

    expect(getByTestId("provider-card-openai")).toBe(cardBefore);
    expect(document.activeElement).toBe(apiKeyInput);
  });

  it("does remount the detail when switching to a different provider", async () => {
    const settingsStore = createSettingsStore({ providers });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue(providers),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={settingsStore.state.providers}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => getByTestId("providers-row-openai"));
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => getByTestId("provider-card-openai"));

    fireEvent.click(getByTestId("providers-back"));
    await waitFor(() => getByTestId("providers-row-fireworks"));
    fireEvent.click(getByTestId("providers-row-fireworks"));
    await waitFor(() => {
      expect(getByTestId("provider-card-fireworks")).toBeTruthy();
    });
  });
});


describe("ProvidersPanel settings inbox", () => {
  it("lists one row per provider instance with endpoint disambiguator", async () => {
    const settingsStore = createSettingsStore({ providers });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue(providers),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={providers}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-list")).toBeTruthy();
    });
    expect(getByTestId("providers-row-openai")).toBeTruthy();
    expect(getByTestId("providers-row-fireworks")).toBeTruthy();
    expect(getByTestId("providers-row-ollama")).toBeTruthy();
    expect(getByTestId("providers-row-ollama").textContent).toContain(
      "localhost:11434",
    );
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("providers-detail")).toBeTruthy();
    });
  });

  it("navigates into detail and back without a side split", async () => {
    const settingsStore = createSettingsStore({ providers });
    const { getByTestId, queryByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue(providers),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={providers}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-list")).toBeTruthy();
    });
    expect(queryByTestId("providers-detail")).toBeNull();
    expect(queryByTestId("add-provider-form")).toBeNull();

    fireEvent.click(getByTestId("providers-row-ollama"));
    await waitFor(() => {
      expect(getByTestId("provider-card-ollama")).toBeTruthy();
      expect(getByTestId("providers-back")).toBeTruthy();
      expect(queryByTestId("providers-list")).toBeNull();
    });

    fireEvent.click(getByTestId("providers-back"));
    await waitFor(() => {
      expect(queryByTestId("providers-detail")).toBeNull();
      expect(getByTestId("providers-list")).toBeTruthy();
    });
  });
});


describe("ProvidersPanel display label rename", () => {
  it("commits rename with label-only updateProvider", async () => {
    const updateProvider = vi.fn().mockResolvedValue({});
    const listProviders = vi.fn().mockResolvedValue([
      { ...providers[0]!, label: "Work" },
    ]);
    const settingsStore = createSettingsStore({
      providers: [providers[0]!],
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders,
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            updateProvider,
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[providers[0]!]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-rename-trigger")).toBeTruthy();
    });
    expect(getByTestId("provider-id-openai").textContent).toBe("openai");
    expect(getByTestId("provider-card-openai").textContent).toContain(
      MODELS_SETTINGS_COPY.providerLabelRenameHint,
    );

    fireEvent.click(getByTestId("provider-rename-trigger"));
    const input = getByTestId("provider-rename-input") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "Personal" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(updateProvider).toHaveBeenCalledWith("openai", { label: "Personal" });
    });
  });

  it("Esc cancels without calling updateProvider", async () => {
    const updateProvider = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({
      providers: [providers[0]!],
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([providers[0]!]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            updateProvider,
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[providers[0]!]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-rename-trigger")).toBeTruthy();
    });
    fireEvent.click(getByTestId("provider-rename-trigger"));
    fireEvent.keyDown(getByTestId("provider-rename-input"), { key: "Escape" });
    await waitFor(() => {
      expect(queryByTestId("provider-rename-input")).toBeNull();
    });
    expect(updateProvider).not.toHaveBeenCalled();
    expect(getByTestId("provider-rename-trigger").textContent).toBe("OpenAI");
  });

  it("rejects empty rename without calling updateProvider", async () => {
    const updateProvider = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({
      providers: [providers[0]!],
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([providers[0]!]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            updateProvider,
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[providers[0]!]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-rename-trigger")).toBeTruthy();
    });
    fireEvent.click(getByTestId("provider-rename-trigger"));
    const input = getByTestId("provider-rename-input") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "   " } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => {
      expect(queryByTestId("provider-rename-input")).toBeNull();
    });
    expect(updateProvider).not.toHaveBeenCalled();
    expect(getByTestId("provider-rename-trigger").textContent).toBe("OpenAI");
  });
});
