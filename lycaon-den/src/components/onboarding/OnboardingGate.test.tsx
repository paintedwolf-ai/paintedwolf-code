import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import {
  resetDispatcherForTests,
  registerCommandHandler,
  dispatchKeyboardEvent,
  setDispatcherPlatformForTests,
} from "../../shortcuts/dispatcher.ts";
import { tryDismissOnboardingWelcome } from "./onboarding-welcome-dismiss.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { OnboardingGate } from "./OnboardingGate.tsx";
import { resetLayoutForTests } from "../../layout/layout-test-support.ts";
import {
  saveStartupCompanion,
  saveWorkspaceOrientation,
  startupCompanionPref,
} from "../../shell/workspace-layout.ts";
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

let firstRunComplete = false;
const completeFirstRunSetup = vi.fn().mockResolvedValue(undefined);
vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  firstRunSetupCompletedPref: () => firstRunComplete,
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
  fireEvent.click(screen.getByRole("button", { name: "Continue without a local model" }));
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
  firstRunComplete = false;
  completeFirstRunSetup.mockClear();
  resetDispatcherForTests();
  setDispatcherPlatformForTests(null);
  resetLayoutForTests();
});

describe("Local model setup", () => {
  it("starts with device selection even with a configured provider", async () => {
    const client = mockClient();
    const store = createSettingsStore({ providers: [readyProvider], modelPolicy: policyWithDefault });
    render(() => <OnboardingGate client={client} settingsStore={store} settingsChecked={true} />);
    expect(screen.getByRole("dialog").getAttribute("aria-labelledby")).toBe("onboarding-local_model-title");
    expect(screen.getByTestId("onboarding-progress").textContent).toBe("1 / 4");
    await advanceToWelcome();
    expect(screen.getByTestId("onboarding-welcome-settings-teach").textContent).toContain("AI providers");
    expect(client.testProvider).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("onboarding-welcome-dismiss"));
    expect(completeFirstRunSetup).toHaveBeenCalledOnce();
  });

  it("opens just one page on an existing installation", () => {
    firstRunComplete = true;
    render(() => <OnboardingGate client={null} settingsStore={createSettingsStore()} settingsChecked={false} />);
    expect(screen.getByTestId("onboarding-progress").textContent).toBe("1 / 1");
    fireEvent.click(screen.getByRole("button", {name: "Continue without a local model"}));
    expect(completeFirstRunSetup).toHaveBeenCalledOnce();
  });
});

describe("Setup navigation and accessibility", () => {
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
    expect(gate.getAttribute("aria-labelledby")).toBe("onboarding-local_model-title");
    expect(container.querySelector("main")).toBeTruthy();
    await waitFor(() => {
      expect(document.activeElement).toBe(
        gate.querySelector(":scope > .den-scrollport__viewport"),
      );
    });
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
    expect(queryByTestId("onboarding-local-model")).toBeNull();
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

  it("Back walks welcome → layout → summarizer → model choice without latching", async () => {
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
      expect(getByTestId("onboarding-local-model")).toBeTruthy();
    });
    expect(queryByTestId("onboarding-setup-back")).toBeNull();
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
    fireEvent.click(screen.getByRole("button", {name: "Continue without a local model"}));
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
