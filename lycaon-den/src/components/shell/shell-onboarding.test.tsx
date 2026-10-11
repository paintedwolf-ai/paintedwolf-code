import { render, screen, waitFor } from "@solidjs/testing-library";
import { createEffect, type ComponentProps } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createShellOnboarding, ShellOnboardingGate } from "./shell-onboarding.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import type { OnboardingGate } from "../onboarding/OnboardingGate.tsx";
const seams = vi.hoisted(() => ({ completed: true, prefsReady: true, frameReady: true, dismiss: vi.fn(async () => undefined),
  gate: null as unknown as ComponentProps<typeof OnboardingGate>, providers: vi.fn(async () => []), policy: vi.fn(async () => ({})) }));
vi.mock("../../settings/system/onboarding-prefs.ts", () => ({ firstRunSetupCompletedPref: () => seams.completed, onboardingPrefsReady: () => seams.prefsReady }));
vi.mock("../../contributions/contribution-store.ts", () => ({ contributionFrameReady: () => seams.frameReady }));
vi.mock("../../platform/connection/boot-fallback.ts", () => ({ dismissBootFallback: seams.dismiss }));
vi.mock("../../platform/connection/app-connection.ts", () => { const client = { listProviders: seams.providers, getModelPolicySettings: seams.policy }; return { getLycaonClient: () => client }; });
vi.mock("../onboarding/OnboardingGate.tsx", () => ({ OnboardingGate: (p: typeof seams.gate) => { createEffect(() => { seams.gate = { ...p }; }); return <div data-testid="setup-gate" />; } }));

function mountGate() {
  const appStore = createAppStore(); const settingsStore = createSettingsStore();
  let controller!: ReturnType<typeof createShellOnboarding>;
  const view = render(() => {
    controller = createShellOnboarding(appStore, { state: { projects: [] } } as never, settingsStore);
    controller.observeProviders(); controller.observeBootFallback();
    return <ShellOnboardingGate appStore={appStore} settingsStore={settingsStore} gate={controller}><div data-testid="workspace" /></ShellOnboardingGate>;
  });
  return { ...view, controller, appStore, settingsStore };
}

beforeEach(() => { seams.completed = true; seams.prefsReady = true; seams.frameReady = true; vi.clearAllMocks(); });
describe("shell onboarding authority", () => {
  it("loads provider policy before presenting a provider gap in an established workspace", async () => {
    const { controller, appStore } = mountGate();
    expect(screen.getByTestId("workspace")).toBeTruthy(); expect(controller.modelsChecked()).toBe(false);
    appStore.actions.setSidecarStatus("connected");
    await waitFor(() => expect(controller.modelsChecked()).toBe(true));
    expect(seams.providers).toHaveBeenCalledOnce(); expect(seams.policy).toHaveBeenCalledOnce();
    expect(controller.providerGap()).toBeTruthy(); expect(controller.showNoProviderBanner()).toBe(true);
  });

  it("holds the first-run gate behind contribution readiness and releases boot fallback", () => {
    seams.completed = false; seams.frameReady = false;
    const loading = mountGate();
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy(); expect(screen.queryByTestId("workspace")).toBeNull();
    expect(seams.dismiss).toHaveBeenCalled(); loading.unmount();
    seams.frameReady = true;
    mountGate(); expect(screen.getByTestId("setup-gate")).toBeTruthy(); expect(screen.queryByTestId("workspace")).toBeNull();
  });

  it("keeps the workspace covered while first-run preferences are unresolved", () => {
    seams.prefsReady = false; seams.completed = false;
    const { appStore } = mountGate(); appStore.actions.setSidecarStatus("disconnected"); expect(screen.getByTestId("onboarding-first-run-pending")).toBeTruthy();
    expect(screen.queryByTestId("workspace")).toBeNull(); expect(seams.dismiss).toHaveBeenCalled();
  });
});
