import { Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ModelPolicyPatch, ModelRef } from "../../../api/types.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import {
  defaultModelPatch,
  defaultModelRef,
  hasDefaultModel,
  providerRemovalPatch,
  readyProviders,
  summarizerOverrideRef,
  summarizerPatch,
} from "../../../settings/providers/models-editor-model.ts";
import { showAllModelsPref } from "../../../settings/appearance/display-prefs.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { onboardingModelHintForProviders } from "../../../settings/providers/provider-onboarding-hints.ts";
import { AI_PROVIDERS_SECTION_LABEL } from "../../../settings/settings-nav-model.ts";
import { ModelSlotPicker } from "../../model/ModelSlotPicker.tsx";
import { ThinkingOverridesPanel } from "../../model/ThinkingOverridesPanel.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { SettingsEditorTitle, ProvidersIcon } from "../SettingsEditorTitle.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { ProvidersPanel } from "./ProvidersPanel.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client: LycaonClient;
  settingsStore: SettingsStore;
  /** `onboarding` hides settings chrome and optional summarizer for first-run focus. */
  variant?: "settings" | "onboarding";
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
};

/** Connect model providers and pick the default model — the first-run setup step. */
export function ProvidersSettingsPanel(props: Props) {
  const isOnboarding = () => props.variant === "onboarding";
  const providers = () => props.settingsStore.state.providers;
  const [addOpen, setAddOpen] = createSignal(false);
 const [savingPolicy, setSavingPolicy] = createSignal(false);

  const removeProvider = async (id: string) => {
    const current = policy();
    if (!current) return;
    const patch = providerRemovalPatch(current, id);
    if (patch) {
      await applyModelPolicy(patch, true);
    }
  };

  const policy = () => props.settingsStore.state.modelPolicy;
  const showAll = () => showAllModelsPref();
  const defaultRef = () => {
    const p = policy();
    return p ? defaultModelRef(p) : { provider_id: "", model: "" };
  };
  const hasReady = () => readyProviders(providers()).length > 0;

  const summarizerRef = () => {
    const p = policy();
    return p ? summarizerOverrideRef(p) : { provider_id: "", model: "" };
  };

  const applyModelPolicy = async (next: ModelPolicyPatch, propagateError = false) => {
 setSavingPolicy(true);
    props.settingsStore.actions.setError(undefined);
    try {
      const updated = await props.client.updateModelPolicySettings(next);
      props.settingsStore.actions.setModelPolicy(updated);
    } catch (err) {
      if (propagateError) throw err;
      props.settingsStore.actions.setError(
        err instanceof Error ? err.message : String(err),
      );
    } finally { setSavingPolicy(false); }
  };

  const selectModel = async (ref: ModelRef) => {
    if (!policy()) return;
    await applyModelPolicy(defaultModelPatch(ref));
  };

  const selectSummarizerModel = async (ref: ModelRef) => {
    if (!policy()) return;
    await applyModelPolicy(summarizerPatch(ref));
  };

  const defaultModelBlock = () => (
    <Show when={hasReady()}>
      <div class="den-settings-default-model" data-testid="default-model-settings">
        <Show when={policy() && !hasDefaultModel(policy())}>
          <p
            class="den-settings-warn"
            role="status"
            data-testid="default-model-required-warning"
          >
            {MODELS_SETTINGS_COPY.defaultModelRequired}
          </p>
        </Show>
        <label class="den-settings-default-model-label" for="default-model-select">
          Default model
        </label>
        <Show
          when={
            isOnboarding()
              ? onboardingModelHintForProviders(providers(), {
                  defaultProviderId: defaultRef().provider_id,
                })
              : MODELS_SETTINGS_COPY.defaultModelHint
          }
        >
          {(hint) => (
            <p class="den-settings-hint" data-testid="default-model-hint">
              {hint()}
            </p>
          )}
        </Show>
        <ModelSlotPicker
 disabled={savingPolicy()}
          compact
          id="default-model-select"
          label="Default model"
          providers={providers()}
          roleSlot="coordinator"
          showAllModels={showAll()}
          value={defaultRef()}
          onChange={(ref) => void selectModel(ref)}
          data-testid="default-model-select"
        />

        <Show when={!isOnboarding()}>
          <label
            class="den-settings-default-model-label"
            for="default-summarizer-model-select"
          >
            {MODELS_SETTINGS_COPY.defaultSummarizerModelLabel}
          </label>
          <p class="den-settings-hint">{MODELS_SETTINGS_COPY.summarizerModelHint}</p>
          <ModelSlotPicker
 disabled={savingPolicy()}
            compact
            id="default-summarizer-model-select"
            label={MODELS_SETTINGS_COPY.defaultSummarizerModelLabel}
            providers={providers()}
            roleSlot="lite"
            showAllModels={showAll()}
            value={summarizerRef()}
            onChange={(ref) => void selectSummarizerModel(ref)}
            data-testid="default-summarizer-model-select"
          />
        </Show>
      </div>
    </Show>
  );

  const settingsPrefsBand = () => (
    <div class="den-settings-prefs-band" data-testid="providers-toolbar">
      <div data-testid="default-model-settings" style={{ display: "contents" }}>
        <Show when={hasReady()}>
          <Show when={policy() && !hasDefaultModel(policy())}>
            <p
              class="den-settings-warn den-settings-prefs-band__warn"
              role="status"
              data-testid="default-model-required-warning"
            >
              {MODELS_SETTINGS_COPY.defaultModelRequired}
            </p>
          </Show>
          <div class="den-settings-prefs-band__field" {...settingAnchor("default-model")}>
            <label
              class="den-settings-default-model-label"
              for="default-model-select"
            >
              {settingLabel("default-model")}
            </label>
            <ModelSlotPicker
 disabled={savingPolicy()}
              compact
              id="default-model-select"
              label={settingLabel("default-model")}
              providers={providers()}
              roleSlot="coordinator"
              showAllModels={showAll()}
              value={defaultRef()}
              onChange={(ref) => void selectModel(ref)}
              data-testid="default-model-select"
            />
          </div>
          <div class="den-settings-prefs-band__field" {...settingAnchor("summarizer-model")}>
            <label
              class="den-settings-default-model-label"
              for="default-summarizer-model-select"
            >
              {settingLabel("summarizer-model")}
            </label>
            <ModelSlotPicker
 disabled={savingPolicy()}
              compact
              id="default-summarizer-model-select"
              label={settingLabel("summarizer-model")}
              providers={providers()}
              roleSlot="lite"
              showAllModels={showAll()}
              value={summarizerRef()}
              onChange={(ref) => void selectSummarizerModel(ref)}
              data-testid="default-summarizer-model-select"
            />
          </div>
        </Show>
      </div>
    </div>
  );

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--onboarding": isOnboarding() }}
      data-testid="providers-settings"
    >
      <Show when={!isOnboarding()}>
        <SettingsEditorTitle icon={<ProvidersIcon />}>
          {AI_PROVIDERS_SECTION_LABEL}
        </SettingsEditorTitle>
      </Show>

      <Show
        when={!isOnboarding()}
        fallback={
          <p class="den-settings-hint">{MODELS_SETTINGS_COPY.onboardingProvidersIntro}</p>
        }
      >
        <SettingsScopeLede
          scope="device"
          counterpartLabel={props.counterpartLabel}
          onOpenCounterpart={props.onOpenCounterpart}
        >
          <p class="den-settings-hint">{MODELS_SETTINGS_COPY.providersIntro}</p>
        </SettingsScopeLede>
      </Show>

      <Show when={!isOnboarding()}>
        {settingsPrefsBand()}
        <ShowLatest when={policy()}>{(current) => <ThinkingOverridesPanel
          policy={current()}
          saving={savingPolicy()}
          providers={providers()}
          onChange={(thinking_overrides) => applyModelPolicy({ thinking_overrides }, true)}
        />}</ShowLatest>
        <Show when={providers().length === 0}>
          <p class="den-settings-hint">{MODELS_SETTINGS_COPY.noProvidersLoaded}</p>
        </Show>
      </Show>

      <ProvidersPanel
        client={props.client}
        settingsStore={props.settingsStore}
        providers={providers()}
        onRemoveProvider={removeProvider}
        addOpen={addOpen()}
        onAddOpenChange={setAddOpen}
      />

      <Show when={isOnboarding()}>{defaultModelBlock()}</Show>
    </div>
  );
}
