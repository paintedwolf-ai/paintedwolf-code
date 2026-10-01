import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { createOverlayScopeFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import {
  defaultModelRef,
  hasDefaultModel,
  providerRemovalPatch,
  readyProviders,
  summarizerOverrideRef,
  summarizerPatch,
} from "../../settings/providers/models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { showAllModelsPref } from "../../settings/appearance/display-prefs.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { completeFirstRunSetup } from "../../settings/system/onboarding-prefs.ts";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ProvidersSettingsPanel } from "../settings/providers/ProvidersSettingsPanel.tsx";
import { ProvidersPanel } from "../settings/providers/ProvidersPanel.tsx";
import { BrandLockup } from "../primitives/BrandLockup.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ModelSlotPicker } from "../model/ModelSlotPicker.tsx";
import {
  ONBOARDING_GATE_STEP_COUNT,
  needsOnboardingConfig,
  onboardingEscapeTarget,
  onboardingGatePage,
  onboardingGateStep,
  onboardingNextPage,
  onboardingPreviousPage,
  onboardingSetupBlocker,
  type OnboardingGatePage,
  type OnboardingSetupBlocker,
} from "./onboarding-gate-model.ts";
import { ONBOARDING_LAYOUT_COPY } from "./onboarding-layout-copy.ts";
import { setOnboardingWelcomeDismissSink } from "./onboarding-welcome-dismiss.ts";
import { OnboardingLayoutChoices } from "./OnboardingLayoutChoices.tsx";
import { OnboardingSettingsTeachNav } from "./OnboardingSettingsTeachNav.tsx";
import { OnboardingStepPage } from "./OnboardingStepPage.tsx";

type Props = {
  client: LycaonClient | null;
  settingsStore: SettingsStore;
  settingsChecked: boolean;
};

const SETUP_CONTINUE_HINT_ID = "onboarding-setup-continue-hint";

function setupBlockerCopy(blocker: OnboardingSetupBlocker): string {
  return blocker === "provider"
    ? MODELS_SETTINGS_COPY.onboardingContinueNeedProvider
    : MODELS_SETTINGS_COPY.onboardingContinueNeedDefaultModel;
}

export function OnboardingGate(props: Props) {
  const initialNeedsConfig = () =>
    needsOnboardingConfig({
      readyProviderCount: readyProviders(props.settingsStore.state.providers)
        .length,
      hasDefaultModel: hasDefaultModel(props.settingsStore.state.modelPolicy),
    });

  let waitingForInitialSettings = props.settingsChecked === false;
  const [page, setPage] = createSignal<OnboardingGatePage>(
    waitingForInitialSettings ? "setup" : onboardingGatePage(initialNeedsConfig()),
  );

  createEffect(() => {
    if (!waitingForInitialSettings || props.settingsChecked !== true) return;
    waitingForInitialSettings = false;
    setPage(onboardingGatePage(initialNeedsConfig()));
  });
  const [validating, setValidating] = createSignal(false);
  const [validationError, setValidationError] = createSignal<
    string | undefined
  >();
  const [continueAnyway, setContinueAnyway] = createSignal(false);
  let validationAbort: AbortController | undefined;
  const stopValidation = () => {
    validationAbort?.abort();
    validationAbort = undefined;
  };
  onCleanup(stopValidation);
  const [dismissing, setDismissing] = createSignal(false);
  const [summarizerSaving, setSummarizerSaving] = createSignal(false);
  const [showAddProvider, setShowAddProvider] = createSignal(false);
  const readyCount = () =>
    readyProviders(props.settingsStore.state.providers).length;
  const defaultReady = () =>
    hasDefaultModel(props.settingsStore.state.modelPolicy);
  const setupBlocker = () =>
    onboardingSetupBlocker({
      readyProviderCount: readyCount(),
      hasDefaultModel: defaultReady(),
    });
  const setupBlockerHint = () => {
    const blocker = setupBlocker();
    return blocker ? setupBlockerCopy(blocker) : undefined;
  };

  const policy = () => props.settingsStore.state.modelPolicy;
  const summarizerRef = () => {
    const p = policy();
    return p ? summarizerOverrideRef(p) : { provider_id: "", model: "" };
  };

  const removeProvider = async (id: string) => {
    const client = props.client;
    if (!client) return;
    const current = policy();
    if (!current) return;
    const patch = providerRemovalPatch(current, id);
    if (patch) {
      try {
        const updated = await client.updateModelPolicySettings(patch);
        props.settingsStore.actions.setModelPolicy(updated);
      } catch (err) {
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : String(err),
        );
        throw err;
      }
    }
  };

  const advance = () => setPage((p) => onboardingNextPage(p) ?? p);
  const goBack = () => setPage((p) => onboardingPreviousPage(p) ?? p);

  // Persist completion before closing.
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
    const updated = await client.updateModelPolicySettings(summarizerPatch(ref));
    props.settingsStore.actions.setModelPolicy(updated);
  };

  const runSummarizerContinue = async () => {
    setSummarizerSaving(true);
    try {
      await applySummarizerRef(summarizerRef());
      advance();
    } catch (err) {
      props.settingsStore.actions.setError(
        err instanceof Error ? err.message : String(err),
      );
    } finally {
      setSummarizerSaving(false);
    }
  };

  const runSetupContinue = async () => {
    setValidationError(undefined);
    setContinueAnyway(false);
    const blocker = setupBlocker();
    if (blocker) {
      setValidationError(setupBlockerCopy(blocker));
      return;
    }
    const current = policy();
    if (!current || !hasDefaultModel(current)) {
      setValidationError(setupBlockerCopy("default_model"));
      return;
    }
    const providerId = defaultModelRef(current).provider_id.trim();
    if (!providerId) {
      setValidationError(setupBlockerCopy("default_model"));
      return;
    }
    setValidating(true);
    const abort = new AbortController();
    validationAbort = abort;
    try {
      const client = props.client;
      if (!client) {
        setValidationError(MODELS_SETTINGS_COPY.onboardingValidationFailed);
        setContinueAnyway(true);
        return;
      }
      const res = await client.testProvider(providerId, abort.signal);
      if (!res.ok) {
        // Keep rejected credentials editable.
        setValidationError(
          res.message?.trim() || MODELS_SETTINGS_COPY.onboardingValidationFailed,
        );
        setContinueAnyway(true);
        return;
      }
      const providers = await client.listProviders();
      props.settingsStore.actions.setProviders(providers);
      setPage("summarizer");
    } catch {
      // Preserve the saved key after an unfinished check.
      const copy = CLIENT_NOTICES.provider_check_unfinished;
      setValidationError(`${copy.title} ${copy.message}`);
      setContinueAnyway(true);
    } finally {
      validationAbort = undefined;
      setValidating(false);
    }
  };

  const step = () => onboardingGateStep(page());
  const pageTitleId = () => `onboarding-${page()}-title`;

  // Focus the step container on entry and page changes so its label is announced.
  let gateRef: HTMLDivElement | undefined;
  // The scrolling viewport takes focus so the keyboard scrolls the step.
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
      <main class="onboarding-gate__inner">
        <BrandLockup class="onboarding-gate__brand" />
        <div class="onboarding-gate__stack">
          <p
            class="onboarding-gate__progress"
            aria-hidden="true"
            data-testid="onboarding-progress"
          >
            {step()} / {ONBOARDING_GATE_STEP_COUNT}
          </p>

          <Show when={page() === "setup"}>
            <OnboardingStepPage
              data-testid="onboarding-setup"
              aria-labelledby="onboarding-setup-title"
            >
              <header class="onboarding-gate__header">
                <h1 class="onboarding-gate__title" id="onboarding-setup-title" {...chromeProps()}>
                  {MODELS_SETTINGS_COPY.onboardingSetupTitle}
                </h1>
                <p class="onboarding-gate__lede">
                  {MODELS_SETTINGS_COPY.onboardingSetupLede}
                </p>
              </header>
              <div class="onboarding-gate__card">
                {/* Names the providers region between the page title and the
                    provider group headings inside the panel. */}
                <h2 class="sr-only">
                  {MODELS_SETTINGS_COPY.onboardingProvidersHeading}
                </h2>
                <Show
                  when={props.client}
                  keyed
                  fallback={
                    <div
                      class="onboarding-gate__offline"
                      data-testid="onboarding-setup-waiting"
                    >
                      <p class="onboarding-gate__offline-lede">
                        {MODELS_SETTINGS_COPY.onboardingWaitingForEngine}
                      </p>
                      <p class="onboarding-gate__offline-hint">
                        {MODELS_SETTINGS_COPY.onboardingWaitingForEngineHint}
                      </p>
                    </div>
                  }
                >
                  {(client) => (
                    <ProvidersSettingsPanel
                      client={client}
                      settingsStore={props.settingsStore}
                      variant="onboarding"
                    />
                  )}
                </Show>
              </div>
              <div class="onboarding-gate__actions onboarding-gate__actions--split">
                <Show when={validationError()}>
                  <p
                    class="onboarding-gate__action-error den-settings-warn"
                    role="alert"
                    data-testid="onboarding-setup-error"
                  >
                    {validationError()}
                  </p>
                </Show>
                <DenButton
                  variant="ghost"
                  data-testid="onboarding-setup-skip"
                  disabled={dismissing()}
                  aria-busy={dismissing()}
                  onClick={() => void finishOnboarding()}
                >
                  {MODELS_SETTINGS_COPY.onboardingSkip}
                </DenButton>
                <Show when={validating()}>
                  <DenButton
                    variant="ghost"
                    data-testid="onboarding-setup-stop-check"
                    onClick={stopValidation}
                  >
                    {MODELS_SETTINGS_COPY.onboardingCancelCheck}
                  </DenButton>
                </Show>
                <Show when={continueAnyway() && !validating()}>
                  <DenButton
                    variant="secondary"
                    data-testid="onboarding-setup-continue-anyway"
                    onClick={() => setPage("summarizer")}
                  >
                    {MODELS_SETTINGS_COPY.onboardingContinueAnyway}
                  </DenButton>
                </Show>
                {/* Keep blocked Continue focusable so its reason can be announced. */}
                <Show when={setupBlockerHint()}>
                  {(hint) => (
                    <p
                      class="onboarding-gate__action-error onboarding-gate__field-hint"
                      id={SETUP_CONTINUE_HINT_ID}
                      data-testid="onboarding-setup-continue-hint"
                    >
                      {hint()}
                    </p>
                  )}
                </Show>
                <DenButton
                  variant="primary"
                  data-testid="onboarding-setup-continue"
                  disabled={validating()}
                  aria-disabled={setupBlockerHint() ? "true" : undefined}
                  aria-describedby={
                    setupBlockerHint() ? SETUP_CONTINUE_HINT_ID : undefined
                  }
                  data-tip={setupBlockerHint()}
                  aria-busy={validating()}
                  onClick={() => void runSetupContinue()}
                >
                  {validating()
                    ? MODELS_SETTINGS_COPY.onboardingContinueValidating
                    : MODELS_SETTINGS_COPY.onboardingContinue}
                </DenButton>
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

                <div class="onboarding-gate__add-provider">
                  <p class="onboarding-gate__field-hint onboarding-gate__add-provider-hint">
                    {MODELS_SETTINGS_COPY.onboardingSummarizerAddProviderHint}
                  </p>
                  <DenButton
                    variant="secondary"
                    class="onboarding-gate__add-provider-btn"
                    data-testid="onboarding-summarizer-add-provider"
                    onClick={() => setShowAddProvider((open) => !open)}
                  >
                    {showAddProvider()
                      ? MODELS_SETTINGS_COPY.onboardingSummarizerHideProvider
                      : MODELS_SETTINGS_COPY.onboardingSummarizerAddProvider}
                  </DenButton>
                </div>

                <Show when={showAddProvider() && props.client} keyed>
                  {(client) => (
                    <div
                      class="onboarding-gate__provider-embed"
                      data-testid="onboarding-summarizer-provider-embed"
                    >
                      <h2 class="sr-only">
                        {MODELS_SETTINGS_COPY.onboardingProvidersHeading}
                      </h2>
                      <ProvidersPanel
                        client={client}
                        settingsStore={props.settingsStore}
                        providers={props.settingsStore.state.providers}
                        onRemoveProvider={removeProvider}
                      />
                    </div>
                  )}
                </Show>

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
                    {MODELS_SETTINGS_COPY.summarizerModelHint}
                  </p>
                  <ModelSlotPicker
                    compact
                    id="onboarding-summarizer-select"
                    label={MODELS_SETTINGS_COPY.defaultSummarizerModelLabel}
                    providers={props.settingsStore.state.providers}
                    roleSlot="lite"
                    showAllModels={showAllModelsPref()}
                    value={summarizerRef()}
                    onChange={(ref) => void applySummarizerRef(ref)}
                    data-testid="default-summarizer-model-select"
                  />
                </div>
              </div>
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
                  onClick={() => void runSummarizerContinue()}
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
