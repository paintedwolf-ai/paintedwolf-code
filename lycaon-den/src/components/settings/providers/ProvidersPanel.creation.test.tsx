import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { providerModel } from "../../../test/provider-fixtures.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { kinds, bedrockKind } from "../../../test/provider-panel-fixture.tsx";

describe("ProvidersPanel add-form API key", () => {
  it("controls modal focus, Escape, background inertness, and focus return", async () => {
    const settingsStore = createSettingsStore({ providers: [] });
    const { getByTestId, queryByTestId } = render(() => (
      <>
        <aside class="den-shell-aside" />
        <main class="den-shell-stage" />
        <ProvidersPanel
          client={
            {
              listProviders: vi.fn().mockResolvedValue([]),
              listProviderKinds: vi.fn().mockResolvedValue(kinds),
              createProvider: vi.fn(),
              replaceProviderCredential: vi.fn(),
              deleteProviderCredential: vi.fn(),
              deleteProvider: vi.fn(),
            } as never
          }
          settingsStore={settingsStore}
          providers={[]}
          onRemoveProvider={() => undefined}
        />
      </>
    ));

    const opener = getByTestId("providers-add");
    opener.focus();
    fireEvent.click(opener);
    await waitFor(() => {
      expect(document.activeElement).toBe(
        screen.getByTestId("add-provider-filter"),
      );
    });
    expect(document.querySelector(".den-shell-aside")?.hasAttribute("inert")).toBe(
      true,
    );
    expect(document.querySelector(".den-shell-stage")?.hasAttribute("inert")).toBe(
      true,
    );

    fireEvent.click(screen.getByTestId("add-provider-kind-ollama"));
    await waitFor(() => {
      expect(document.activeElement).toBe(
        screen.getByTestId("add-provider-label"),
      );
    });
    fireEvent.keyDown(screen.getByTestId("add-provider-label"), {
      key: "Escape",
    });
    await waitFor(() => {
      expect(queryByTestId("providers-add-dialog")).toBeNull();
      expect(document.activeElement).toBe(opener);
    });
    expect(document.querySelector(".den-shell-aside")?.hasAttribute("inert")).toBe(
      false,
    );
    expect(document.querySelector(".den-shell-stage")?.hasAttribute("inert")).toBe(
      false,
    );

    fireEvent.click(opener);
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-filter")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("add-provider-cancel"));
    await waitFor(() => expect(document.activeElement).toBe(opener));
  });

  it("shows key field for key-required kinds and saves it on add", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const replaceProviderCredential = vi.fn().mockResolvedValue({});
    const listProviders = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([
        {
          id: "openai-1",
          kind: "openai",
          label: "OpenAI 1",
          base_url: "https://api.openai.com/v1",
          configured: true,
          ready_to_assign: true,
          credential_present: true,
          requires_api_key: true,
          features: {
            tool_calls: true,
            thinking: true,
            prompt_cache: "automatic_prefix",
          },
          models: [providerModel("gpt-4o")],
        },
      ]);
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders,
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            createProvider,
            replaceProviderCredential,
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-add")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-add"));
    await waitFor(() => {
      expect(screen.getByTestId("providers-add-dialog")).toBeTruthy();
      expect(screen.getByTestId("add-provider-filter")).toBeTruthy();
      expect(screen.getByTestId("add-provider-kind-list")).toBeTruthy();
    });
    expect(screen.queryByTestId("add-provider-api-key")).toBeNull();
    expect(getByTestId("providers-list-chrome")).toBeTruthy();
    expect(queryByTestId("providers-back")).toBeNull();

    const scrollHost = screen.getByTestId("add-provider-kind-list");
    const stableList = scrollHost.querySelector("ul");
    expect(scrollHost.tagName).toBe("DIV");
    expect(stableList?.tagName).toBe("UL");
    fireEvent.input(screen.getByTestId("add-provider-filter"), {
      target: { value: "f" },
    });
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-kind-fireworks")).toBeTruthy();
      expect(screen.queryByTestId("add-provider-kind-ollama")).toBeNull();
    });
    expect(scrollHost.querySelector("ul")).toBe(stableList);

    fireEvent.input(screen.getByTestId("add-provider-filter"), {
      target: { value: "openai" },
    });
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-kind-openai")).toBeTruthy();
      expect(screen.queryByTestId("add-provider-kind-ollama")).toBeNull();
    });

    fireEvent.click(screen.getByTestId("add-provider-kind-openai"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-api-key")).toBeTruthy();
    });
    expect(screen.getByTestId("add-provider-api-key-hint").textContent).toBe(
      MODELS_SETTINGS_COPY.providerApiKeyOptionalHint,
    );

    fireEvent.input(screen.getByTestId("add-provider-api-key"), {
      target: { value: "sk-test-key" },
    });
    fireEvent.click(screen.getByTestId("add-provider-btn"));

    await waitFor(() => {
      expect(createProvider).toHaveBeenCalled();
    });
    expect(replaceProviderCredential).toHaveBeenCalledWith("openai-1", "sk-test-key");
  });

  it("hides key field for keyless kinds and skips credential save", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const replaceProviderCredential = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            createProvider,
            replaceProviderCredential,
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-add")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-add"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-kind-list")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("add-provider-kind-ollama"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-base-url")).toBeTruthy();
    });
    expect(screen.queryByTestId("add-provider-api-key")).toBeNull();

    fireEvent.click(screen.getByTestId("add-provider-btn"));
    await waitFor(() => {
      expect(createProvider).toHaveBeenCalled();
    });
    expect(replaceProviderCredential).not.toHaveBeenCalled();
  });

  it.each(["create", "credential", "refresh"] as const)("keeps an add failure at %s inside the dialog and permits retry", async (stage) => {
    const client = {
      listProviders: vi.fn().mockResolvedValue([]),
      listProviderKinds: vi.fn().mockResolvedValue(kinds),
      createProvider: vi.fn().mockResolvedValue({}),
      replaceProviderCredential: vi.fn().mockResolvedValue({}),
    };
    const fail = stage === "create" ? client.createProvider
      : stage === "credential" ? client.replaceProviderCredential : client.listProviders;
    fail.mockRejectedValueOnce(new Error("Provider configuration could not be saved"));
    const settingsStore = createSettingsStore({ providers: [] });
    render(() => <ProvidersPanel client={client as never} settingsStore={settingsStore} providers={[]} onRemoveProvider={() => { }} />);
    fireEvent.click(await screen.findByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-fireworks"));
    fireEvent.input(screen.getByTestId("add-provider-label"), { target: { value: "Connection check" } });
    fireEvent.input(screen.getByTestId("add-provider-api-key"), { target: { value: "fixture-key" } });
    fireEvent.click(screen.getByTestId("add-provider-btn"));
    await waitFor(() => expect(screen.getByTestId("providers-add-dialog").querySelector('[role="alert"]')?.textContent).toBe("Provider configuration could not be saved"));
    expect((screen.getByTestId("add-provider-label") as HTMLInputElement).value).toBe("Connection check");
    expect((screen.getByTestId("add-provider-api-key") as HTMLInputElement).value).toBe("fixture-key");
    expect((screen.getByTestId("add-provider-btn") as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByTestId("add-provider-btn"));
    await waitFor(() => expect(screen.queryByTestId("providers-add-dialog")).toBeNull());
  });

  it("allows adding a key-required provider without a key", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const replaceProviderCredential = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId } = render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue(kinds),
            createProvider,
            replaceProviderCredential,
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(getByTestId("providers-add")).toBeTruthy();
    });
    fireEvent.click(getByTestId("providers-add"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-kind-list")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("add-provider-kind-fireworks"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-api-key")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("add-provider-btn"));
    await waitFor(() => {
      expect(createProvider).toHaveBeenCalled();
    });
    expect(replaceProviderCredential).not.toHaveBeenCalled();
    await waitFor(() => {
    });
  });
});

describe("ProvidersPanel add-form credential mode", () => {
  it("adds a key-mode instance by default and saves the pasted key", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const replaceProviderCredential = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({ providers: [] });
    render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue([bedrockKind]),
            createProvider,
            replaceProviderCredential,
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("providers-add")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-bedrock"));

    await waitFor(() => {
      expect(screen.getByTestId("add-provider-credential-mode")).toBeTruthy();
    });
    expect(
      (
        screen.getByTestId(
          "add-provider-credential-mode-api-key",
        ) as HTMLInputElement
      ).checked,
    ).toBe(true);
    expect(screen.getByTestId("add-provider-api-key")).toBeTruthy();

    fireEvent.input(screen.getByTestId("add-provider-api-key"), {
      target: { value: "bedrock-api-key" },
    });
    fireEvent.click(screen.getByTestId("add-provider-btn"));

    await waitFor(() => {
      expect(createProvider).toHaveBeenCalledWith(
        expect.objectContaining({ requires_api_key: true }),
      );
    });
    expect(replaceProviderCredential).toHaveBeenCalledWith(
      expect.any(String),
      "bedrock-api-key",
    );
  });

  it("drops the key field and any typed key when ambient is chosen", async () => {
    const createProvider = vi.fn().mockResolvedValue({});
    const replaceProviderCredential = vi.fn().mockResolvedValue({});
    const settingsStore = createSettingsStore({ providers: [] });
    render(() => (
      <ProvidersPanel
        client={
          {
            listProviders: vi.fn().mockResolvedValue([]),
            listProviderKinds: vi.fn().mockResolvedValue([bedrockKind]),
            createProvider,
            replaceProviderCredential,
            deleteProviderCredential: vi.fn(),
            deleteProvider: vi.fn(),
          } as never
        }
        settingsStore={settingsStore}
        providers={[]}
        onRemoveProvider={() => undefined}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("providers-add")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("providers-add"));
    fireEvent.click(await screen.findByTestId("add-provider-kind-bedrock"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-api-key")).toBeTruthy();
    });

    fireEvent.input(screen.getByTestId("add-provider-api-key"), {
      target: { value: "typed-then-abandoned" },
    });
    fireEvent.click(screen.getByTestId("add-provider-credential-mode-ambient"));

    await waitFor(() => {
      expect(screen.queryByTestId("add-provider-api-key")).toBeNull();
    });
    fireEvent.click(screen.getByTestId("add-provider-btn"));

    await waitFor(() => {
      expect(createProvider).toHaveBeenCalledWith(
        expect.objectContaining({ requires_api_key: false }),
      );
    });
    // Cancelling leaves the entered key unsaved.
    expect(replaceProviderCredential).not.toHaveBeenCalled();
  });
});
