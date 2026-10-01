import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { LycaonApiError } from "../../../api/http.ts";
import type { ProviderMeta } from "../../../api/types.ts";
import { CLIENT_NOTICES } from "../../../notices/client-notices.generated.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { assignableModelCapabilities, providerModel } from "../../../test/provider-fixtures.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { providers, kinds } from "../../../test/provider-panel-fixture.tsx";



describe("ProvidersPanel configured actions", () => {
  it("shows subdued catalog/discovery status without eligibility lecture", async () => {
    const openai: ProviderMeta = {
      ...providers[0]!,
      catalog_status: "unavailable",
      discovery_status: "error",
    };
    const settingsStore = createSettingsStore({
      providers: [openai],
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([openai]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[openai]}
        onRemoveProvider={() => undefined}
      />
    ));
    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-feed-status-openai")).toBeTruthy();
    });
    const hint = getByTestId("provider-feed-status-openai").textContent ?? "";
    expect(hint).toContain(MODELS_SETTINGS_COPY.catalogUnavailable);
    expect(hint.toLowerCase()).not.toContain("eligib");
    // The failed listing is its own block, with a reason and a next step.
    const failure =
      getByTestId("provider-discovery-failure-openai").textContent ?? "";
    expect(failure).toContain(MODELS_SETTINGS_COPY.discoveryFailed);
    expect(failure).toContain("api.openai.com");
    expect(failure).toContain(MODELS_SETTINGS_COPY.testConnection);
    expect(getByTestId("provider-card-openai").textContent).toContain("Ready");
  });

  // Failed discovery leaves tool support unknown.
  it("says tool-call support is unknown when there is no model list", async () => {
    const unreachable: ProviderMeta = {
      ...providers[0]!,
      ready_to_assign: false,
      discovery_status: "error",
      models: [],
    };
    const settingsStore = createSettingsStore({ providers: [unreachable] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([unreachable]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[unreachable]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    expect(getByTestId("providers-row-openai").textContent).toContain(
      MODELS_SETTINGS_COPY.statusCannotReach,
    );
    expect(getByTestId("providers-row-openai").textContent).not.toContain(
      "Connected",
    );

    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-tool-calls-openai")).toBeTruthy();
    });
    const toolCalls = getByTestId("provider-tool-calls-openai");
    expect(toolCalls.getAttribute("data-tool-calls")).toBe("unknown");
    expect(toolCalls.textContent).toBe(
      MODELS_SETTINGS_COPY.toolCallsUnknownNoModels,
    );
    expect(toolCalls.textContent).not.toContain("can't make tool calls");
    expect(toolCalls.classList.contains("den-settings-warn")).toBe(false);
  });

  it.each(["ok", "empty"] as const)("distinguishes completed %s discovery from an unread model list", async (discovery) => {
    const provider: ProviderMeta = {
      ...providers[0]!, ready_to_assign: false, models: [], discovery_status: discovery,
    };
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={{ listProviders: vi.fn().mockResolvedValue([provider]), listProviderKinds: vi.fn().mockResolvedValue(kinds) } as never}
        settingsStore={createSettingsStore({ providers: [provider] })}
        providers={[provider]}
        onRemoveProvider={() => undefined}
      />
    ));
    fireEvent.click(await waitFor(() => getByTestId("providers-row-openai")));
    const support = await waitFor(() => getByTestId("provider-tool-calls-openai"));
    expect(support.getAttribute("data-tool-calls")).toBe("unknown");
    expect(support.textContent).toBe(discovery === "ok"
      ? MODELS_SETTINGS_COPY.toolCallsUnknownFilteredModels
      : MODELS_SETTINGS_COPY.toolCallsUnknownEmptyModels);
    expect(support.textContent).not.toContain("No models have been listed");
  });

  it.each(["completed", "pending"] as const)("endpoint changes invalidate %s connection checks", async (timing) => {
    let finish!: (value: unknown) => void;
    const pending = new Promise((resolve) => { finish = resolve; });
    const provider = providers[0]!;
    const settingsStore = createSettingsStore({ providers: [provider] });
    const client = {
      listProviders: vi.fn().mockResolvedValue([provider]),
      listProviderKinds: vi.fn().mockResolvedValue(kinds),
      updateProvider: vi.fn().mockResolvedValue({}),
      testProvider: vi.fn().mockReturnValue(pending),
    };
    render(() => <ProvidersPanel client={client as never} settingsStore={settingsStore} providers={[provider]} onRemoveProvider={() => {}} />);
    await waitFor(() => expect(settingsStore.state.providerKinds?.length).toBe(kinds.length));
    fireEvent.click(screen.getByTestId("providers-row-openai"));
    fireEvent.click(await screen.findByTestId("provider-test-openai"));
    if (timing === "completed") {
      finish({ ok: false, message: "Previous endpoint unavailable" });
      await waitFor(() => expect(screen.getByTestId("provider-test-result-openai").textContent).toContain("Previous endpoint"));
    }
    fireEvent.input(screen.getByTestId("provider-endpoint-openai"), { target: { value: "http://localhost:11434/v1" } });
    fireEvent.click(screen.getByTestId("provider-save-endpoint-openai"));
    await waitFor(() => expect((screen.getByTestId("provider-save-endpoint-openai") as HTMLButtonElement).disabled).toBe(false));
    if (timing === "pending") finish({ ok: false, message: "Previous endpoint unavailable" });
    await Promise.resolve();
    await waitFor(() => expect(screen.queryByTestId("provider-test-result-openai")).toBeNull());
    expect((screen.getByTestId("provider-test-openai") as HTMLButtonElement).disabled).toBe(false);
  });

  it.each(["resolve", "reject"] as const)("a stale model refresh cannot %s after a credential change", async (outcome) => {
    let finish!: (value: unknown) => void;
    let fail!: (error: Error) => void;
    const pending = new Promise((resolve, reject) => { finish = resolve; fail = reject; });
    const provider = providers[0]!;
    const settingsStore = createSettingsStore({ providers: [provider] });
    const client = {
      listProviders: vi.fn().mockResolvedValue([provider]),
      listProviderKinds: vi.fn().mockResolvedValue(kinds),
      deleteProviderCredential: vi.fn().mockResolvedValue({}),
      refreshProviderModels: vi.fn().mockReturnValue(pending),
    };
    render(() => <ProvidersPanel client={client as never} settingsStore={settingsStore} providers={[provider]} onRemoveProvider={() => {}} />);
    await waitFor(() => expect(settingsStore.state.providerKinds?.length).toBe(kinds.length));
    fireEvent.click(screen.getByTestId("providers-row-openai"));
    fireEvent.click(await screen.findByTestId("provider-refresh-openai"));
    fireEvent.click(screen.getByTestId("provider-clear-key-openai"));
    await waitFor(() => expect(client.listProviders).toHaveBeenCalled());
    if (outcome === "resolve") finish({ ...provider, models: [providerModel("stale-model")] });
    else fail(new Error("Old credential failed"));
    await Promise.resolve();
    await waitFor(() => expect((screen.getByTestId("provider-refresh-openai") as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByTestId("provider-refresh-failure-openai")).toBeNull();
    expect(settingsStore.state.providers[0]?.models).toEqual(provider.models);
  });

  it("still warns when every listed model says it cannot make tool calls", async () => {
    const noTools: ProviderMeta = {
      ...providers[0]!,
      models: [
        providerModel("gpt-4o", {
          capabilities: assignableModelCapabilities({
            tools: { state: "unsupported" },
          }),
        }),
      ],
    };
    const settingsStore = createSettingsStore({ providers: [noTools] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([noTools]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[noTools]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-tool-calls-openai")).toBeTruthy();
    });
    const toolCalls = getByTestId("provider-tool-calls-openai");
    expect(toolCalls.getAttribute("data-tool-calls")).toBe("unsupported");
    expect(toolCalls.textContent).toBe(
      MODELS_SETTINGS_COPY.noToolCallsWarning,
    );
    expect(toolCalls.classList.contains("den-settings-warn")).toBe(true);
  });

  it("points a rejected key at a new key rather than at the endpoint", async () => {
    const rejected: ProviderMeta = {
      ...providers[0]!,
      ready_to_assign: false,
      discovery_status: "error",
      models: [],
      requires_api_key: true,
      credential_present: true,
    };
    const settingsStore = createSettingsStore({ providers: [rejected] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([rejected]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[rejected]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-discovery-failure-openai")).toBeTruthy();
    });
    expect(
      getByTestId("provider-discovery-failure-openai").textContent,
    ).toContain(MODELS_SETTINGS_COPY.discoveryFailedNextStep);
  });

  it("shows Test and Refresh when configured even with zero models", async () => {
    const emptyConfigured: ProviderMeta = {
      id: "gemini",
      kind: "gemini",
      label: "Gemini",
      base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
      configured: true,
      ready_to_assign: false,
      credential_present: true,
      requires_api_key: true,
      features: {
        tool_calls: true,
        thinking: true,
        prompt_cache: "none",
      },
      models: [],
    };
    const settingsStore = createSettingsStore({
      providers: [emptyConfigured],
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([emptyConfigured]),
            listProviderKinds: vi.fn().mockResolvedValue([
              {
                kind: "gemini",
                label: "Gemini",
                base_url: emptyConfigured.base_url,
                requires_api_key: true,
              },
            ]),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn().mockResolvedValue({ deleted: true }),
            testProvider: vi.fn(),
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[emptyConfigured]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-gemini")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-gemini"));
    await waitFor(() => {
      expect(getByTestId("provider-test-gemini")).toBeTruthy();
    });
    expect(getByTestId("provider-refresh-gemini")).toBeTruthy();
    expect(queryByTestId("provider-toggle-models-gemini")).toBeNull();
  });

  it("Test connection probes without inventing a model id", async () => {
    const gemini: ProviderMeta = {
      id: "gemini",
      kind: "gemini",
      label: "Gemini",
      base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
      configured: true,
      ready_to_assign: true,
      credential_present: true,
      requires_api_key: true,
      features: {
        tool_calls: true,
        thinking: true,
        prompt_cache: "none",
      },
      models: [
        providerModel("gemini-2.0-flash"),
        providerModel("gemini-2.5-flash"),
      ],
    };
    const testProvider = vi.fn().mockResolvedValue({
      ok: true,
      latency_ms: 12,
      models: 2,
      provider: { ...gemini, models: [providerModel("gemini-2.5-flash")] },
    });
    const settingsStore = createSettingsStore({
      providers: [gemini],
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([gemini]),
            listProviderKinds: vi.fn().mockResolvedValue([
              {
                kind: "gemini",
                label: "Gemini",
                base_url: gemini.base_url,
                requires_api_key: true,
              },
            ]),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn().mockResolvedValue({ deleted: true }),
            testProvider,
            refreshProviderModels: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[gemini]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-gemini")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-gemini"));
    await waitFor(() => {
      expect(getByTestId("provider-test-gemini")).toBeTruthy();
    });
    getByTestId("provider-test-gemini").click();
    await waitFor(() => {
      expect(testProvider).toHaveBeenCalledTimes(1);
    });
    expect(testProvider).toHaveBeenCalledWith("gemini");
    expect(testProvider.mock.calls[0]).toHaveLength(1);
  });

  it("waits for policy cleanup before atomically deleting a provider", async () => {
    let finishPolicyCleanup: (() => void) | undefined;
    const onRemoveProvider = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishPolicyCleanup = resolve;
        }),
    );
    const deleteProvider = vi.fn().mockResolvedValue({ deleted: true });
    const deleteProviderCredential = vi.fn().mockResolvedValue({ deleted: true });
    const settingsStore = createSettingsStore({ providers: [providers[0]!] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            deleteProviderCredential,
            deleteProvider,
          } as never
        }
        settingsStore={settingsStore}
        providers={[providers[0]!]}
        onRemoveProvider={onRemoveProvider}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-card-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("provider-remove-openai"));
    await waitFor(() => expect(onRemoveProvider).toHaveBeenCalledWith("openai"));
    expect(deleteProvider).not.toHaveBeenCalled();
    expect(deleteProviderCredential).not.toHaveBeenCalled();

    finishPolicyCleanup?.();
    await waitFor(() => expect(deleteProvider).toHaveBeenCalledWith("openai"));
    expect(deleteProviderCredential).not.toHaveBeenCalled();
    expect(getByTestId("providers-list")).toBeTruthy();
  });

  it("keeps the provider when policy cleanup fails", async () => {
    const deleteProvider = vi.fn().mockResolvedValue({ deleted: true });
    const deleteProviderCredential = vi.fn().mockResolvedValue({ deleted: true });
    const settingsStore = createSettingsStore({ providers: [providers[0]!] });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([providers[0]!]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            deleteProviderCredential,
            deleteProvider,
          } as never
        }
        settingsStore={settingsStore}
        providers={[providers[0]!]}
        onRemoveProvider={() => Promise.reject(new Error("policy write failed"))}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-card-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("provider-remove-openai"));
    await waitFor(() => {
      expect(settingsStore.state.error).toBe("policy write failed");
    });
    expect(deleteProvider).not.toHaveBeenCalled();
    expect(deleteProviderCredential).not.toHaveBeenCalled();
    expect(screen.getByTestId("provider-card-openai")).toBeTruthy();
  });
});


// Failed refreshes retain cached models and allow retrying.
describe("ProvidersPanel model refresh on a dead network", () => {
  const renderWithRefresh = (refreshProviderModels: () => Promise<never>) => {
    const openai = providers[0]!;
    const settingsStore = createSettingsStore({ providers: [openai] });
    return render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([openai]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            replaceProviderCredential: vi.fn(),
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
            testProvider: vi.fn(),
            refreshProviderModels,
          } as never
        }
        settingsStore={settingsStore}
        providers={[openai]}
        onRemoveProvider={() => undefined}
      />
    ));
  };

  const openCard = async (getByTestId: (id: string) => HTMLElement) => {
    await waitFor(() => {
      expect(getByTestId("providers-row-openai")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-row-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-refresh-openai")).toBeTruthy();
    });
  };

  it("keeps the cached list and offers a retry when the host reports it unreachable", async () => {
    const refreshProviderModels = vi
      .fn()
      .mockRejectedValue(
        new LycaonApiError(
          "The provider did not answer, so the model list could not be refreshed.",
          503,
          "model_catalog_unreachable",
          { title: "Could not refresh the model list" },
        ),
      );
    const { getByTestId } = renderWithRefresh(
      refreshProviderModels as never,
    );
    await openCard(getByTestId);

    fireEvent.click(getByTestId("provider-refresh-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-refresh-failure-openai")).toBeTruthy();
    });

    const failure =
      getByTestId("provider-refresh-failure-openai").textContent ?? "";
    expect(failure).toContain("Could not refresh the model list");
    expect(failure).toContain("could not be refreshed");

    fireEvent.click(getByTestId("provider-toggle-models-openai"));
    await waitFor(() => {
      expect(
        getByTestId("provider-model-list-openai").textContent,
      ).toContain("gpt-4o");
    });

    expect(
      getByTestId("provider-refresh-openai").textContent,
    ).toContain(MODELS_SETTINGS_COPY.refreshModels);
    fireEvent.click(getByTestId("provider-refresh-retry-openai"));
    await waitFor(() => {
      expect(refreshProviderModels).toHaveBeenCalledTimes(2);
    });
  });

  it("falls back to client copy when the request never reached the sidecar", async () => {
    const { getByTestId } = renderWithRefresh(
      vi.fn().mockRejectedValue(new TypeError("Failed to fetch")) as never,
    );
    await openCard(getByTestId);

    fireEvent.click(getByTestId("provider-refresh-openai"));
    await waitFor(() => {
      expect(getByTestId("provider-refresh-failure-openai")).toBeTruthy();
    });
    const failure =
      getByTestId("provider-refresh-failure-openai").textContent ?? "";
    expect(failure).toContain(
      CLIENT_NOTICES.model_catalog_refresh_failed.title,
    );
    expect(failure).not.toContain("Failed to fetch");
  });
});
