import { createEffect, createSignal, untrack } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { dismissBootFallback } from "../../platform/connection/boot-fallback.ts";
import { providerConfigGap } from "../../notices/no-provider-card.ts";
import { hasDefaultModel, readyProviders } from "../../settings/providers/models-editor-model.ts";
import { firstRunSetupCompletedPref, onboardingPrefsReady } from "../../settings/system/onboarding-prefs.ts";
import { loadSettingsPanel } from "../../settings/settings-actions.ts";
import { needsOnboardingConfig, shouldShowOnboardingHardGate, shouldSuppressShellForFirstRun } from "../onboarding/onboarding-gate-model.ts";

export function createShellOnboarding(appStore: AppStore, projects: ProjectsStore, settingsStore: SettingsStore) {
  const [modelsChecked, setModelsChecked] = createSignal(false);
  // Onboarding depends on provider policy.
  const observeProviders = () => createEffect(() => {
    if (appStore.state.sidecarStatus !== "connected") return;
    const client = getLycaonClient();
    if (!client) return;
    void loadSettingsPanel(
      settingsStore,
      client,
      untrack(() => projects.state.projects),
      "providers",
    ).finally(
      () => setModelsChecked(true),
    );
  });

  const needsConfig = () =>
    needsOnboardingConfig({
      readyProviderCount: readyProviders(settingsStore.state.providers).length,
      hasDefaultModel: hasDefaultModel(settingsStore.state.modelPolicy),
    });

  const showOnboardingGate = () =>
    shouldShowOnboardingHardGate({
      prefsReady: onboardingPrefsReady(),
      firstRunSetupCompleted: firstRunSetupCompletedPref(),
    });

  const suppressShellForFirstRun = () =>
    shouldSuppressShellForFirstRun({
      prefsReady: onboardingPrefsReady(),
      firstRunSetupCompleted: firstRunSetupCompletedPref(),
    });

  // Home and chat share one missing-provider condition.
  const providerGap = () =>
    providerConfigGap({
      needsConfig: needsConfig(),
      hasReadyProvider:
        readyProviders(settingsStore.state.providers).length > 0,
      sidecarConnected: appStore.state.sidecarStatus === "connected",
      modelsChecked: modelsChecked(),
      firstRunSetupCompleted: firstRunSetupCompletedPref(),
    });
  const showNoProviderBanner = () => providerGap() != null;

  const observeBootFallback = () => createEffect(() => {
    if (showOnboardingGate()) {
      void dismissBootFallback();
      return;
    }
    if (
      suppressShellForFirstRun() &&
      appStore.state.sidecarStatus === "disconnected"
    ) {
      void dismissBootFallback();
    }
  });

  return {
    modelsChecked, showOnboardingGate, suppressShellForFirstRun,
    providerGap, showNoProviderBanner, observeProviders, observeBootFallback
  };
}
