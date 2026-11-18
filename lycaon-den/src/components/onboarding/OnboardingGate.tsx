import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { createOverlayScopeFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import {
  isIncludedProvider,
  summarizerOverrideRef,
  summarizerPatch,
} from "../../settings/providers/models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { showAllModelsPref } from "../../settings/appearance/display-prefs.ts";
import { LocalModelSelector } from "../settings/providers/LocalModelSelector.tsx";
import { completeFirstRunSetup, completeLocalModelOnboarding, firstRunSetupCompletedPref, localModelOnboardingCompletedPref } from "../../settings/system/onboarding-prefs.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { soleWindowControls } from "../shell/WindowControls.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { BrandLockup } from "../primitives/BrandLockup.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ModelSlotPicker } from "../model/ModelSlotPicker.tsx";
import {
  ONBOARDING_GATE_STEP_COUNT,
  onboardingEscapeTarget,
  onboardingGateStep,
  onboardingNextPage,
  onboardingPreviousPage,
  type OnboardingGatePage,
} from "./onboarding-gate-model.ts";
import { ONBOARDING_LAYOUT_COPY } from "./onboarding-layout-copy.ts";
import { setOnboardingWelcomeDismissSink } from "./onboarding-welcome-dismiss.ts";
import { OnboardingLayoutChoices } from "./OnboardingLayoutChoices.tsx";
import { OnboardingSettingsTeachNav } from "./OnboardingSettingsTeachNav.tsx";
import { SummarizerModelHint } from "../model/SummarizerModelHint.tsx";
import { OnboardingStepPage } from "./OnboardingStepPage.tsx";

type Props = {
  client: LycaonClient | null;
  settingsStore: SettingsStore;
  settingsChecked: boolean;
};

export function OnboardingGate(props: Props) {
  const upgrade = firstRunSetupCompletedPref();
  const [page, setPage] = createSignal<OnboardingGatePage>(localModelOnboardingCompletedPref() ? "summarizer" : "local_model");
  const [dismissing, setDismissing] = createSignal(false);
  const [summarizerSaving, setSummarizerSaving] = createSignal(false);
  const [summarizerError, setSummarizerError] = createSignal("");
  const localProvider = () => props.settingsStore.state.providers.find(isIncludedProvider);
  const policy = () => props.settingsStore.state.modelPolicy;
  const summarizerRef = () => {
    const p = policy();
    return p ? summarizerOverrideRef(p) : { provider_id: "", model: "" };
  };

  const advance = () => setPage((p) => onboardingNextPage(p) ?? p);
  const finishModelStep = async () => {
    if (dismissing()) return;
    setDismissing(true);
    try {
      await completeLocalModelOnboarding();
      if (!upgrade) setPage("summarizer");
    } finally {
      setDismissing(false);
    }
  };
  const goBack = () => setPage((p) => onboardingPreviousPage(p) ?? p);

  const finishOnboarding = async () => {
    if (dismissing()) return;
    setDismissing(true);
    try {
      await completeFirstRunSetup();
    } finally {
      setDismissing(false);
    }
  };

  createEffect(() => {
    const target = onboardingEscapeTarget(page());
    setOnboardingWelcomeDismissSink(() => {
      if (target === "finish") void finishOnboarding();
      else setPage(target);
    });
    onCleanup(() => setOnboardingWelcomeDismissSink(null));
  });

  const applySummarizerRef = async (ref: {
    provider_id: string;
    model: string;
  }) => {
    const client = props.client;
    if (!client) return;
    const p = policy();
    if (!p) return;
    setSummarizerSaving(true);
    setSummarizerError("");
    try {
      const updated = await client.updateModelPolicySettings(summarizerPatch(ref));
      props.settingsStore.actions.setModelPolicy(updated);
    } catch (err) {
      setSummarizerError(err instanceof Error ? err.message : String(err));
    } finally {
      setSummarizerSaving(false);
    }
  };

  const step = () => onboardingGateStep(page());
  const pageTitleId = () => `onboarding-${page()}-title`;

  let gateRef: HTMLDivElement | undefined;
  // Focus the viewport so keyboard scrolling follows the active step.
  let gateViewport: HTMLDivElement | undefined;
  // Overlay scope leaves Escape available to the dismissal chain.
  createOverlayScopeFocusTrap(() => true, () => gateRef, { autoFocus: false });
  createEffect(() => {
    page();
    queueMicrotask(() => gateViewport?.focus());
  });

  return (
    <Scrollport
      ref={(el) => (gateRef = el)}
      viewportRef={(el) => (gateViewport = el)}
      class="onboarding-gate"
      classList={{
        "onboarding-gate--layout": page() === "layout",
        "onboarding-gate--welcome": page() === "welcome",
      }}
      contentClass="onboarding-gate__content"
      viewport={{ tabIndex: -1 }}
      data-testid="onboarding-gate"
      role="dialog"
      aria-modal="true"
      aria-labelledby={pageTitleId()}
    >
      <Show when={page() === "welcome"}>
        <div
          class="onboarding-gate__wash den-stage-wash-fill"
          aria-hidden="true"
          data-testid="onboarding-welcome-wash"
        />
      </Show>
      <ChromeDragSurface class="onboarding-gate__chrome-drag" />
      <div class="onboarding-gate__window-controls">{soleWindowControls()}</div>
      <main class="onboarding-gate__inner">
        <BrandLockup class="onboarding-gate__brand" />
        <div class="onboarding-gate__stack">
          <p
            class="onboarding-gate__progress"
            aria-hidden="true"
            data-testid="onboarding-progress"
          >
            {step()} / {upgrade ? 1 : ONBOARDING_GATE_STEP_COUNT}
          </p>

          <Show when={page() === "local_model"}>
            <OnboardingStepPage data-testid="onboarding-local-model" aria-labelledby="onboarding-local_model-title">
              <header class="onboarding-gate__header">
                <h1 class="onboarding-gate__title" id="onboarding-local_model-title" {...chromeProps()}>Choose a local model</h1>
                <p class="onboarding-gate__lede">Run a model on this device. You can change your choice in Settings.</p>
              </header>
              <div class="onboarding-gate__card">
                <Show when={props.client && localProvider()} fallback={
                  <div>
                    <p role="status">{props.settingsChecked && props.client ? "Local generation is unavailable on this device." : "Waiting for the local engine…"}</p>
                    <DenButton variant="primary" disabled={dismissing()} onClick={() => void finishModelStep()}>Continue without a local model</DenButton>
                  </div>
                }>
                  {provider => <LocalModelSelector client={props.client!} provider={provider()}
                    onRefresh={async () => {
                      if (props.client) props.settingsStore.actions.setProviders(await props.client.listProviders());
                    }}
                    onContinue={() => void finishModelStep()} />}
                </Show>
              </div>
            </OnboardingStepPage>
          </Show>

          <Show when={page() === "summarizer"}>
            <OnboardingStepPage
              data-testid="onboarding-summarizer"
              aria-labelledby="onboarding-summarizer-title"
            >
              <header class="onboarding-gate__header">
                <h1
                  class="onboarding-gate__title"
                  id="onboarding-summarizer-title"
                  {...chromeProps()}
                >
                  {MODELS_SETTINGS_COPY.onboardingSummarizerTitle}{" "}
                  <span class="onboarding-gate__optional">
                    {MODELS_SETTINGS_COPY.onboardingSummarizerOptional}
                  </span>
                </h1>
                <p class="onboarding-gate__lede">
                  {MODELS_SETTINGS_COPY.onboardingSummarizerLede}
                </p>
              </header>
              <div class="onboarding-gate__card onboarding-gate__card--stack">
                <div
                  class="onboarding-gate__tip"
                  data-testid="onboarding-summarizer-local-tip"
                >
                  <p>
                    {MODELS_SETTINGS_COPY.onboardingSummarizerLocalSaveHint}
                  </p>
                </div>

                <div
                  class="den-settings-default-model"
                  data-testid="onboarding-summarizer-settings"
                >
                  <label
                    class="den-settings-default-model-label"
                    for="onboarding-summarizer-select"
                  >
                    {MODELS_SETTINGS_COPY.defaultSummarizerModelLabel}
                  </label>
                  <p class="den-settings-hint">
                    <SummarizerModelHint />
                  </p>
                  <ModelSlotPicker
                    compact
                    id="onboarding-summarizer-select"
                    label={MODELS_SETTINGS_COPY.defaultSummarizerModelLabel}
                    providers={props.settingsStore.state.providers}
                    disabled={summarizerSaving()}
                    roleSlot="lite"
                    showAllModels={showAllModelsPref()}
                    value={summarizerRef()}
                    onChange={(ref) => void applySummarizerRef(ref)}
                    data-testid="default-summarizer-model-select"
                  />
                </div>
              </div>
              <Show when={summarizerError()}><p class="den-settings-warn" role="alert">{summarizerError()}</p></Show>
              <div class="onboarding-gate__actions onboarding-gate__actions--split">
                <DenButton
                  variant="ghost"
                  data-testid="onboarding-summarizer-back"
                  onClick={goBack}
                >
                  {MODELS_SETTINGS_COPY.onboardingBack}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="onboarding-summarizer-continue"
                  disabled={summarizerSaving()}
                  aria-busy={summarizerSaving()}
                  onClick={advance}
                >
                  {MODELS_SETTINGS_COPY.onboardingContinue}
                </DenButton>
              </div>
            </OnboardingStepPage>
          </Show>

          <Show when={page() === "layout"}>
            <OnboardingStepPage
              data-testid="onboarding-layout"
              aria-labelledby="onboarding-layout-title"
            >
              <header class="onboarding-gate__header">
                <h1
                  class="onboarding-gate__title"
                  id="onboarding-layout-title"
                  {...chromeProps()}
                >
                  {ONBOARDING_LAYOUT_COPY.title}
                </h1>
                <p class="onboarding-gate__lede">{ONBOARDING_LAYOUT_COPY.lede}</p>
              </header>
              <OnboardingLayoutChoices labelledBy="onboarding-layout-title" />
              <p class="onboarding-gate__footnote">
                {ONBOARDING_LAYOUT_COPY.settingsHint}
              </p>
              <div class="onboarding-gate__actions onboarding-gate__actions--split">
                <DenButton
                  variant="ghost"
                  data-testid="onboarding-layout-back"
                  onClick={goBack}
                >
                  {MODELS_SETTINGS_COPY.onboardingBack}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="onboarding-layout-continue"
                  onClick={advance}
                >
                  {MODELS_SETTINGS_COPY.onboardingContinue}
                </DenButton>
              </div>
            </OnboardingStepPage>
          </Show>

          <Show when={page() === "welcome"}>
            <OnboardingStepPage
              data-testid="onboarding-welcome"
              aria-labelledby="onboarding-welcome-title"
            >
              <header class="onboarding-gate__header">
                <h1
                  class="onboarding-gate__title"
                  id="onboarding-welcome-title"
                  {...chromeProps()}
                >
                  {MODELS_SETTINGS_COPY.onboardingWelcomeTitle}
                </h1>
                <p class="onboarding-gate__lede">
                  {MODELS_SETTINGS_COPY.onboardingWelcomeBody}
                </p>
              </header>
              <div
                class="onboarding-gate__teach"
                data-testid="onboarding-welcome-settings-teach"
              >
                <div class="onboarding-gate__teach-rail">
                  <OnboardingSettingsTeachNav />
                </div>
                <div class="onboarding-gate__teach-callout">
                  <svg
                    class="onboarding-gate__teach-arrow"
                    viewBox="0 0 40 16"
                    aria-hidden="true"
                  >
                    <path
                      class="onboarding-gate__teach-arrow-shaft"
                      d="M38 8H12"
                    />
                    <path
                      class="onboarding-gate__teach-arrow-head"
                      d="M14 2.5 4 8l10 5.5"
                    />
                  </svg>
                  <div class="onboarding-gate__teach-copy">
                    <p class="onboarding-gate__teach-copy-lead">
                      {MODELS_SETTINGS_COPY.onboardingWelcomeSettingsHint}
                    </p>
                    <p class="onboarding-gate__teach-copy-sub">
                      {MODELS_SETTINGS_COPY.onboardingWelcomeSettingsHintSub}
                    </p>
                  </div>
                </div>
              </div>
              <div class="onboarding-gate__actions onboarding-gate__actions--split">
                <DenButton
                  variant="ghost"
                  data-testid="onboarding-welcome-back"
                  disabled={dismissing()}
                  onClick={goBack}
                >
                  {MODELS_SETTINGS_COPY.onboardingBack}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="onboarding-welcome-dismiss"
                  disabled={dismissing()}
                  aria-busy={dismissing()}
                  onClick={() => void finishOnboarding()}
                >
                  {MODELS_SETTINGS_COPY.onboardingWelcomeDismiss}
                </DenButton>
              </div>
            </OnboardingStepPage>
          </Show>
        </div>
      </main>
    </Scrollport>
  );
}
