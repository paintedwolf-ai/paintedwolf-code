import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import {
  dispatchKeyboardEvent,
  registerCommandHandler,
  resetDispatcherForTests,
  setDispatcherPlatformForTests,
} from "../../shortcuts/dispatcher.ts";
import { OnboardingGate } from "./OnboardingGate.tsx";
import { onboardingSetupBlocker } from "./onboarding-gate-model.ts";
import { tryDismissOnboardingWelcome } from "./onboarding-welcome-dismiss.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import {
  resetLayoutStoreForTests,
  saveStartupCompanion,
  saveWorkspaceOrientation,
  startupCompanionPref,
} from "../../shell/layout-store.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import { stageLabelFor } from "../stage/stage-registry.tsx";
import { ONBOARDING_LAYOUT_COPY } from "./onboarding-layout-copy.ts";

let customChrome = true;
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => customChrome,
}));

vi.mock("../../store/app-state-snapshot.ts", () => ({
  loadSharedAppState: vi.fn().mockResolvedValue(undefined),
  getAppStateSnapshot: vi.fn(() => ({ version: 1, recents: [] })),
  persistAppState: vi.fn().mockResolvedValue(undefined),
}));

const completeFirstRunSetup = vi.fn().mockResolvedValue(undefined);
vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  completeFirstRunSetup: () => completeFirstRunSetup(),
}));


const readyProvider: ProviderMeta = {
  id: "local",
  kind: "ollama",
  label: "Local",
  base_url: "http://localhost:11434/v1",
  configured: true,
  ready_to_assign: true,
  credential_present: false,
  requires_api_key: false,
  features: {
    tool_calls: true,
    thinking: true,
    prompt_cache: "local_kv",
  },
  models: [providerModel("llama3.1")],
};

const policyWithDefault: ModelPolicy = {
  coordinator: { provider_id: "local", model: "llama3.1" },
  lite: { provider_id: "", model: "" },
  agent_pool: {
    selection: "first",
    models: [{ provider_id: "local", model: "llama3.1" }],
  },
};

function mockClient(overrides: Partial<LycaonClient> = {}): LycaonClient {
  return {
    listProviders: vi.fn().mockResolvedValue([readyProvider]),
    listProviderKinds: vi.fn().mockResolvedValue([
      {
        kind: "ollama",
        label: "Local",
        base_url: "http://localhost:11434/v1",
        requires_api_key: false,
      },
      {
        kind: "openai",
        label: "Cloud",
        base_url: "https://api.openai.com/v1",
        requires_api_key: true,
      },
    ]),
    testProvider: vi.fn().mockResolvedValue({ ok: true, latency_ms: 12 }),
    updateModelPolicySettings: vi.fn(async (p: Partial<ModelPolicy>) => ({
      ...policyWithDefault,
      ...p,
    })),
    updateProvider: vi.fn(),
    deleteProvider: vi.fn(),
    replaceProviderCredential: vi.fn(),
    deleteProviderCredential: vi.fn(),
    refreshProviderModels: vi.fn(),
    ...overrides,
  } as never;
}

async function advanceToLayout(): Promise<void> {
  fireEvent.click(screen.getByTestId("onboarding-summarizer-continue"));
  await waitFor(() => {
    expect(screen.getByTestId("onboarding-layout")).toBeTruthy();
  });
}

async function advanceToWelcome(): Promise<void> {
  await advanceToLayout();
  fireEvent.click(screen.getByTestId("onboarding-layout-continue"));
  await waitFor(() => {
    expect(screen.getByTestId("onboarding-welcome")).toBeTruthy();
  });
}

function pressEscape(): void {
  dispatchKeyboardEvent({
    key: "Escape",
    code: "Escape",
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    target: document.body,
  });
}

afterEach(() => {
  cleanup();
  customChrome = true;
  completeFirstRunSetup.mockClear();
  resetDispatcherForTests();
  setDispatcherPlatformForTests(null);
  resetLayoutStoreForTests();
});

describe("onboarding-gate-model continue helpers", () => {
  it("onboardingSetupBlocker names the missing half", () => {
    expect(
      onboardingSetupBlocker({ readyProviderCount: 0, hasDefaultModel: true }),
    ).toBe("provider");
    expect(
      onboardingSetupBlocker({ readyProviderCount: 1, hasDefaultModel: false }),
    ).toBe("default_model");
    expect(
      onboardingSetupBlocker({ readyProviderCount: 1, hasDefaultModel: true }),
    ).toBeNull();
  });
});

describe("OnboardingGate setup page", () => {
  it("mounts setup with continue gated until provider + default model exist", () => {
    const settingsStore = createSettingsStore({
      providers: [],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-setup")).toBeTruthy();
    const cont = getByTestId("onboarding-setup-continue") as HTMLButtonElement;
    expect(cont.getAttribute("aria-disabled")).toBe("true");
    expect(queryByTestId("onboarding-welcome")).toBeNull();
    expect(getByTestId("onboarding-setup").textContent).toContain(
      MODELS_SETTINGS_COPY.onboardingSetupTitle,
    );
  });

  it("says what Continue is waiting for, and keeps it reachable", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));

    const hint = getByTestId("onboarding-setup-continue-hint");
    expect(hint.textContent).toBe(
      MODELS_SETTINGS_COPY.onboardingContinueNeedProvider,
    );
    const cont = getByTestId("onboarding-setup-continue") as HTMLButtonElement;
    // Focusable and announced rather than removed from the tab order.
    expect(cont.disabled).toBe(false);
    expect(cont.getAttribute("aria-disabled")).toBe("true");
    expect(cont.getAttribute("aria-describedby")).toBe(hint.id);
    expect(cont.getAttribute("data-tip")).toBe(
      MODELS_SETTINGS_COPY.onboardingContinueNeedProvider,
    );

    // With a provider ready, the ask moves to the other half.
    settingsStore.actions.setProviders([readyProvider]);
    await waitFor(() => {
      expect(getByTestId("onboarding-setup-continue-hint").textContent).toBe(
        MODELS_SETTINGS_COPY.onboardingContinueNeedDefaultModel,
      );
    });

    settingsStore.actions.setModelPolicy(policyWithDefault);
    await waitFor(() => {
      expect(
        (
          getByTestId("onboarding-setup-continue") as HTMLButtonElement
        ).getAttribute("aria-disabled"),
      ).toBeNull();
    });
  });

  it("is a labelled modal landmark that takes focus on mount", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { getByTestId, container } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));

    const gate = getByTestId("onboarding-gate");
    expect(gate.getAttribute("role")).toBe("dialog");
    expect(gate.getAttribute("aria-modal")).toBe("true");
    expect(gate.getAttribute("aria-labelledby")).toBe("onboarding-setup-title");
    expect(container.querySelector("main")).toBeTruthy();
    await waitFor(() => {
      expect(document.activeElement).toBe(
        gate.querySelector(":scope > .den-scrollport__viewport"),
      );
    });
  });

  it("puts a heading between the page title and the provider groups", () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { container } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    const levels = [...container.querySelectorAll("h1, h2, h3, h4, h5, h6")].map(
      (h) => Number(h.tagName.slice(1)),
    );
    expect(levels[0]).toBe(1);
    expect(levels[1]).toBe(2);
  });

  it("opens on summarizer when config is already ready and latch is unset", () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    expect(queryByTestId("onboarding-setup")).toBeNull();
    expect(queryByTestId("onboarding-welcome")).toBeNull();
    expect(getByTestId("onboarding-summarizer").textContent).toContain(
      MODELS_SETTINGS_COPY.onboardingSummarizerTitle,
    );
  });

  it("waits for the boot settings read before routing a configured profile", async () => {
    const [settingsChecked, setSettingsChecked] = createSignal(false);
    const settingsStore = createSettingsStore({ providers: [] });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={settingsChecked()}
      />
    ));

    expect(getByTestId("onboarding-setup")).toBeTruthy();
    settingsStore.actions.setProviders([readyProvider]);
    settingsStore.actions.setModelPolicy(policyWithDefault);
    expect(getByTestId("onboarding-setup")).toBeTruthy();

    setSettingsChecked(true);
    await waitFor(() => {
      expect(getByTestId("onboarding-summarizer")).toBeTruthy();
      expect(queryByTestId("onboarding-setup")).toBeNull();
    });
  });

  it("setup Skip latches and exits the whole first-run", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-setup-skip")).toBeTruthy();
    fireEvent.click(getByTestId("onboarding-setup-skip"));
    await waitFor(() => {
      expect(completeFirstRunSetup).toHaveBeenCalledOnce();
    });
    expect(queryByTestId("onboarding-summarizer-skip")).toBeNull();
  });

  it("welcome fades in the stage wash background", async () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    await advanceToWelcome();
    expect(getByTestId("onboarding-welcome-wash")).toBeTruthy();
    expect(queryByTestId("onboarding-setup")).toBeNull();
    expect(
      getByTestId("onboarding-gate").classList.contains(
        "onboarding-gate--welcome",
      ),
    ).toBe(true);
  });

  it("welcome teach reuses Settings dock chrome with AI providers selected and the nav dot", async () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    await advanceToWelcome();
    expect(getByTestId("onboarding-welcome").textContent).toContain(
      MODELS_SETTINGS_COPY.onboardingWelcomeSettingsHint,
    );
    expect(getByTestId("onboarding-welcome").textContent).toContain(
      MODELS_SETTINGS_COPY.onboardingWelcomeSettingsHintSub,
    );
    const dock = getByTestId("onboarding-welcome-settings-dock");
    expect(dock.hasAttribute("inert")).toBe(true);
    expect(dock.querySelectorAll(".den-nav-marker")).toHaveLength(1);
    const providers = getByTestId("settings-nav-providers");
    expect(providers.classList.contains("den-shell-nav-sub-link-active")).toBe(
      true,
    );
    expect(dock.querySelector(".den-shell-dock-link-active")?.textContent).toContain(
      "Settings",
    );
    fireEvent.click(getByTestId("settings-nav-approvals"));
    expect(providers.classList.contains("den-shell-nav-sub-link-active")).toBe(
      true,
    );
    expect(
      getByTestId("settings-nav-approvals").classList.contains(
        "den-shell-nav-sub-link-active",
      ),
    ).toBe(false);
  });

  it("summarizer mirrors settings select and centers add-provider chrome", async () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-summarizer").textContent).toContain(
      MODELS_SETTINGS_COPY.onboardingSummarizerOptional,
    );
    expect(getByTestId("onboarding-summarizer-local-tip").textContent).toContain(
      "save money",
    );
    expect(getByTestId("onboarding-summarizer-settings")).toBeTruthy();
    expect(getByTestId("default-summarizer-model-select")).toBeTruthy();
    expect(queryByTestId("onboarding-summarizer-provider-embed")).toBeNull();

    fireEvent.click(getByTestId("onboarding-summarizer-add-provider"));
    await waitFor(() => {
      expect(getByTestId("onboarding-summarizer-provider-embed")).toBeTruthy();
      expect(getByTestId("providers-add")).toBeTruthy();
    });
    expect(getByTestId("providers-row-local")).toBeTruthy();

    fireEvent.click(getByTestId("providers-add"));
    await waitFor(() => {
      expect(screen.getByTestId("providers-add-dialog")).toBeTruthy();
    });
  });

  it("setup page uses the same AI providers panel as the summarizer, unhidden", async () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { queryByTestId, getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={false}
      />
    ));
    await waitFor(() => {
      expect(getByTestId("default-model-select")).toBeTruthy();
    });
    expect(queryByTestId("onboarding-summarizer-add-provider")).toBeNull();
    expect(getByTestId("providers-list-chrome")).toBeTruthy();
    expect(getByTestId("providers-add")).toBeTruthy();
    expect(getByTestId("providers-row-local")).toBeTruthy();
    expect(queryByTestId("default-summarizer-model-select")).toBeNull();
  });

  it("Continue validates via testProvider then advances to summarizer then welcome", async () => {
    const testProvider = vi.fn().mockResolvedValue({ ok: true, latency_ms: 9 });
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient({ testProvider })}
        settingsStore={settingsStore}
        settingsChecked={false}
      />
    ));
    await waitFor(() => {
      expect(
        getByTestId("onboarding-setup-continue").getAttribute("aria-disabled"),
      ).toBeNull();
    });
    fireEvent.click(getByTestId("onboarding-setup-continue"));
    await waitFor(() => {
      expect(testProvider).toHaveBeenCalledWith("local", expect.any(AbortSignal));
      expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    });
    expect(queryByTestId("onboarding-setup")).toBeNull();
    expect(queryByTestId("onboarding-summarizer-skip")).toBeNull();
    fireEvent.click(getByTestId("onboarding-summarizer-continue"));
    await waitFor(() => {
      expect(getByTestId("onboarding-layout")).toBeTruthy();
    });
    fireEvent.click(getByTestId("onboarding-layout-continue"));
    await waitFor(() => {
      expect(getByTestId("onboarding-welcome")).toBeTruthy();
    });
    fireEvent.click(getByTestId("onboarding-welcome-dismiss"));
    await waitFor(() => {
      expect(completeFirstRunSetup).toHaveBeenCalledOnce();
    });
  });

  it("key check is cancellable and still lets the user continue", async () => {
    let rejectCheck: ((err: Error) => void) | undefined;
    const testProvider = vi.fn(
      (_id: string, signal?: AbortSignal) =>
        new Promise<never>((_resolve, reject) => {
          rejectCheck = reject;
          signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient({ testProvider })}
        settingsStore={settingsStore}
        settingsChecked={false}
      />
    ));
    await waitFor(() => {
      expect(
        getByTestId("onboarding-setup-continue").getAttribute("aria-disabled"),
      ).toBeNull();
    });

    fireEvent.click(getByTestId("onboarding-setup-continue"));
    await waitFor(() => {
      expect(getByTestId("onboarding-setup-stop-check")).toBeTruthy();
    });
    expect(queryByTestId("onboarding-setup-continue-anyway")).toBeNull();

    fireEvent.click(getByTestId("onboarding-setup-stop-check"));
    await waitFor(() => {
      expect(getByTestId("onboarding-setup-continue-anyway")).toBeTruthy();
      expect(queryByTestId("onboarding-setup-stop-check")).toBeNull();
    });
    expect(getByTestId("onboarding-setup-error").textContent).toContain(
      CLIENT_NOTICES.provider_check_unfinished.title,
    );

    fireEvent.click(getByTestId("onboarding-setup-continue-anyway"));
    await waitFor(() => {
      expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    });
    expect(rejectCheck).toBeTypeOf("function");
  });

  it("a timed-out key check offers continue rather than trapping the user", async () => {
    const testProvider = vi
      .fn()
      .mockRejectedValue(new Error("network timeout"));
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient({ testProvider })}
        settingsStore={settingsStore}
        settingsChecked={false}
      />
    ));
    await waitFor(() => {
      expect(
        getByTestId("onboarding-setup-continue").getAttribute("aria-disabled"),
      ).toBeNull();
    });
    fireEvent.click(getByTestId("onboarding-setup-continue"));

    await waitFor(() => {
      expect(getByTestId("onboarding-setup-continue-anyway")).toBeTruthy();
    });
    const error = getByTestId("onboarding-setup-error").textContent ?? "";
    expect(error).toContain(CLIENT_NOTICES.provider_check_unfinished.message);
    expect(error).not.toContain("network timeout");

    fireEvent.click(getByTestId("onboarding-setup-continue-anyway"));
    await waitFor(() => {
      expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    });
  });

  it("Back walks welcome → layout → summarizer → setup without latching", async () => {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    await advanceToWelcome();
    expect(queryByTestId("onboarding-welcome-skip")).toBeNull();
    fireEvent.click(getByTestId("onboarding-welcome-back"));
    await waitFor(() => {
      expect(getByTestId("onboarding-layout")).toBeTruthy();
    });
    expect(queryByTestId("onboarding-welcome")).toBeNull();
    expect(queryByTestId("onboarding-layout-skip")).toBeNull();

    fireEvent.click(getByTestId("onboarding-layout-back"));
    await waitFor(() => {
      expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    });

    fireEvent.click(getByTestId("onboarding-summarizer-back"));
    await waitFor(() => {
      expect(getByTestId("onboarding-setup")).toBeTruthy();
    });
    expect(getByTestId("onboarding-setup-skip")).toBeTruthy();
    expect(queryByTestId("onboarding-setup-back")).toBeNull();
    expect(completeFirstRunSetup).not.toHaveBeenCalled();
  });

  it("Escape on setup skips the whole first-run and latches", async () => {
    setDispatcherPlatformForTests("macos");
    registerCommandHandler("overlay.dismiss", () => {
      tryDismissOnboardingWelcome();
    });
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-setup")).toBeTruthy();
    dispatchKeyboardEvent({
      key: "Escape",
      code: "Escape",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
      target: document.body,
    });
    await waitFor(() => {
      expect(completeFirstRunSetup).toHaveBeenCalledOnce();
    });
  });

  it("Escape closes the provider picker before the setup page", async () => {
    const settingsStore = createSettingsStore({ providers: [] });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));

    fireEvent.click(getByTestId("providers-add"));
    await waitFor(() => {
      expect(screen.getByTestId("add-provider-filter")).toBeTruthy();
    });
    fireEvent.keyDown(screen.getByTestId("add-provider-filter"), {
      key: "Escape",
    });
    await waitFor(() => {
      expect(queryByTestId("providers-add-dialog")).toBeNull();
      expect(getByTestId("onboarding-setup")).toBeTruthy();
    });
    expect(completeFirstRunSetup).not.toHaveBeenCalled();
  });

  it("Escape steps summarizer → layout → welcome without latching", async () => {
    setDispatcherPlatformForTests("macos");
    registerCommandHandler("overlay.dismiss", () => {
      tryDismissOnboardingWelcome();
    });
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-summarizer")).toBeTruthy();
    pressEscape();
    await waitFor(() => {
      expect(getByTestId("onboarding-layout")).toBeTruthy();
    });
    pressEscape();
    await waitFor(() => {
      expect(getByTestId("onboarding-welcome")).toBeTruthy();
    });
    expect(completeFirstRunSetup).not.toHaveBeenCalled();
    // Stepping past the layout page keeps the default.
    expect(startupCompanionPref()).toBeNull();
  });

  it("dismisses welcome via Escape through overlay.dismiss", async () => {
    setDispatcherPlatformForTests("macos");
    registerCommandHandler("overlay.dismiss", () => {
      tryDismissOnboardingWelcome();
    });
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    await advanceToWelcome();
    expect(getByTestId("onboarding-welcome")).toBeTruthy();
    await waitFor(() => {
      dispatchKeyboardEvent({
        key: "Escape",
        code: "Escape",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
        target: document.body,
      });
      expect(completeFirstRunSetup).toHaveBeenCalledOnce();
    });
  });

  it("keeps setup and shows an error when validation fails", async () => {
    const testProvider = vi.fn().mockResolvedValue({
      ok: false,
      message: "connection refused",
    });
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={mockClient({ testProvider })}
        settingsStore={settingsStore}
        settingsChecked={false}
      />
    ));
    await waitFor(() => {
      expect(
        getByTestId("onboarding-setup-continue").getAttribute("aria-disabled"),
      ).toBeNull();
    });
    fireEvent.click(getByTestId("onboarding-setup-continue"));
    await waitFor(() => {
      expect(getByTestId("onboarding-setup-error").textContent).toContain(
        "connection refused",
      );
    });
    expect(queryByTestId("onboarding-welcome")).toBeNull();
    expect(queryByTestId("onboarding-summarizer")).toBeNull();
    expect(getByTestId("onboarding-setup")).toBeTruthy();
  });

  it("omits the drag strip when custom chrome is off", () => {
    customChrome = false;
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { container } = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(container.querySelector(".onboarding-gate__chrome-drag")).toBeNull();
  });

  it("renders setup with a waiting state and a usable Skip when client is null", () => {
    const settingsStore = createSettingsStore({
      providers: [],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={null}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-setup")).toBeTruthy();
    expect(getByTestId("onboarding-setup-waiting")).toBeTruthy();
    expect(queryByTestId("default-model-select")).toBeNull();
    expect(getByTestId("onboarding-setup-skip")).toBeTruthy();
  });

  it("swaps waiting state for the provider panel when the client arrives", async () => {
    const [client, setClient] = createSignal<LycaonClient | null>(null);
    const settingsStore = createSettingsStore({
      providers: [],
      modelPolicy: {
        coordinator: { provider_id: "", model: "" },
        lite: { provider_id: "", model: "" },
        agent_pool: { selection: "first", models: [] },
      },
    });
    const { getByTestId, queryByTestId } = render(() => (
      <OnboardingGate
        client={client()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    expect(getByTestId("onboarding-setup-waiting")).toBeTruthy();

    setClient(mockClient());
    await waitFor(() => {
      expect(queryByTestId("onboarding-setup-waiting")).toBeNull();
      expect(getByTestId("providers-settings")).toBeTruthy();
    });
  });

  it("skipping with a null client still latches first-run", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
    });
    const { getByTestId } = render(() => (
      <OnboardingGate
        client={null}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    fireEvent.click(getByTestId("onboarding-setup-skip"));
    await waitFor(() => {
      expect(completeFirstRunSetup).toHaveBeenCalledOnce();
    });
  });
});

describe("OnboardingGate starting layout page", () => {
  async function renderAtLayout() {
    const settingsStore = createSettingsStore({
      providers: [readyProvider],
      modelPolicy: policyWithDefault,
    });
    const view = render(() => (
      <OnboardingGate
        client={mockClient()}
        settingsStore={settingsStore}
        settingsChecked={true}
      />
    ));
    await advanceToLayout();
    return view;
  }

  const radio = (testId: string) =>
    screen.getByTestId(testId) as HTMLInputElement;

  it("sits between the summarizer and welcome as step 3 of 4", async () => {
    const { getByTestId } = await renderAtLayout();
    expect(getByTestId("onboarding-progress").textContent).toBe("3 / 4");
    const gate = getByTestId("onboarding-gate");
    expect(gate.getAttribute("aria-labelledby")).toBe("onboarding-layout-title");
    expect(gate.classList.contains("onboarding-gate--layout")).toBe(true);
    expect(getByTestId("onboarding-layout").textContent).toContain(
      ONBOARDING_LAYOUT_COPY.title,
    );
  });

  it("arrives with Chat chosen, which is the unset default", async () => {
    await renderAtLayout();
    expect(radio("onboarding-layout-chat-radio").checked).toBe(true);
    expect(radio("onboarding-layout-split-radio").checked).toBe(false);
    expect(startupCompanionPref()).toBeNull();
  });

  it("writes Open at launch as soon as a card is picked", async () => {
    const { getByTestId } = await renderAtLayout();
    fireEvent.click(radio("onboarding-layout-split-radio"));
    await waitFor(() => {
      expect(startupCompanionPref()).toBe("files");
    });
    expect(radio("onboarding-layout-split-radio").checked).toBe(true);
    expect(getByTestId("onboarding-layout-split").textContent).toContain(
      ONBOARDING_LAYOUT_COPY.splitName(stageLabelFor("files")),
    );

    fireEvent.click(radio("onboarding-layout-chat-radio"));
    await waitFor(() => {
      expect(startupCompanionPref()).toBeNull();
    });
    expect(completeFirstRunSetup).not.toHaveBeenCalled();
  });

  it("groups named radios under the page title and hides the previews", async () => {
    const { getByTestId } = await renderAtLayout();
    const group = getByTestId("onboarding-layout-choices");
    expect(group.tagName).toBe("FIELDSET");
    expect(group.getAttribute("aria-labelledby")).toBe("onboarding-layout-title");
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    expect(
      screen.getByRole("radio", { name: ONBOARDING_LAYOUT_COPY.chatName }),
    ).toBe(radio("onboarding-layout-chat-radio"));
    expect(
      screen.getByRole("radio", {
        name: ONBOARDING_LAYOUT_COPY.splitName(stageLabelFor("files")),
      }),
    ).toBe(radio("onboarding-layout-split-radio"));
    expect(
      getByTestId("onboarding-layout-preview-split").closest("[aria-hidden='true']"),
    ).toBeTruthy();
  });

  it("names a companion already set in Settings instead of replacing it", async () => {
    await saveStartupCompanion("search");
    const { getByTestId } = await renderAtLayout();
    expect(radio("onboarding-layout-split-radio").checked).toBe(true);
    expect(getByTestId("onboarding-layout-split").textContent).toContain(
      ONBOARDING_LAYOUT_COPY.splitName(stageLabelFor("search")),
    );
    fireEvent.click(radio("onboarding-layout-split-radio"));
    expect(startupCompanionPref()).toBe("search");
  });

  it("mirrors the previews with the workspace", async () => {
    await saveWorkspaceOrientation("mirrored");
    const { getByTestId } = await renderAtLayout();
    for (const kind of ["chat", "split"]) {
      expect(
        getByTestId(`onboarding-layout-preview-${kind}`).classList.contains(
          "onboarding-layout-preview--mirrored",
        ),
      ).toBe(true);
    }
  });
});
