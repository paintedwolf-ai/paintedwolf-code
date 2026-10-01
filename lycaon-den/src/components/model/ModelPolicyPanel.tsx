import { Index, Show } from "solid-js";
import type {
  ModelPolicy,
  ModelRef,
  ProviderMeta,
} from "../../api/types.ts";
import {
  addPoolRow,
  defaultModelRef,
  modelAssignmentOptions,
  POOL_SELECTION_LABELS,
  removePoolRow,
  setWorkerModel,
  setWorkerPoolEnabled,
  summarizerOverrideRef,
  usesWorkerPool,
  workerModelRef,
} from "../../settings/providers/models-editor-model.ts";
import { showAllModelsPref } from "../../settings/appearance/display-prefs.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { SETTINGS_EDITOR_COPY } from "../../settings/editor/settings-editor-copy.ts";
import { ModelSlotPicker } from "./ModelSlotPicker.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenField } from "../primitives/DenField.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";

type Props = {
  policy: ModelPolicy;
  providers: readonly ProviderMeta[];
  hasStoredAssignments: boolean;
  saving: boolean;
  onChange: (policy: ModelPolicy) => void;
  /** Project Configuration — inherit empty slots from Settings. */
  projectLocal?: boolean;
};

export function ModelPolicyPanel(props: Props) {
  const projectLocal = () => props.projectLocal === true;
  const showAll = () => showAllModelsPref();

  const hasAssignableModels = () =>
    modelAssignmentOptions(props.providers).length > 0 ||
    props.hasStoredAssignments;

  const setSlot = (
    slot: "coordinator" | "lite",
    ref: ModelRef,
  ) => {
    props.onChange({ ...props.policy, [slot]: ref });
  };

  const setPoolRow = (index: number, ref: ModelRef) => {
    const models = props.policy.agent_pool.models.map((m, i) =>
      i === index ? ref : m,
    );
    props.onChange({
      ...props.policy,
      agent_pool: { ...props.policy.agent_pool, models },
    });
  };

  return (
    <div class="den-settings-model-policy" data-testid="model-policy-panel">
      <Show
        when={hasAssignableModels()}
        fallback={
          <p
            class="den-settings-hint den-settings-models-empty"
            data-testid="models-policy-empty"
          >
            {props.providers.length === 0
              ? MODELS_SETTINGS_COPY.noProvidersLoaded
              : MODELS_SETTINGS_COPY.modelPolicyEmpty}
          </p>
        }
      >
        <Show when={!projectLocal()}>
          <p class="den-settings-hint">{MODELS_SETTINGS_COPY.modelPolicyIntro}</p>
        </Show>

        <section class="den-settings-section" data-testid="models-role-section">
          <ModelSlotPicker
 disabled={props.saving}
            label={MODELS_SETTINGS_COPY.coordinatorSlotLabel}
            hint={
              projectLocal()
                ? undefined
                : MODELS_SETTINGS_COPY.coordinatorSlotHint
            }
            providers={props.providers}
            roleSlot="coordinator"
            showAllModels={showAll()}
            value={defaultModelRef(props.policy)}
            onChange={(ref) => setSlot("coordinator", ref)}
            data-testid="coordinator-model-picker"
          />
          <ModelSlotPicker
 disabled={props.saving}
            label={MODELS_SETTINGS_COPY.liteSlotLabel}
            hint={
              projectLocal() ? undefined : MODELS_SETTINGS_COPY.summarizerModelHint
            }
            providers={props.providers}
            roleSlot="lite"
            showAllModels={showAll()}
            value={summarizerOverrideRef(props.policy)}
            onChange={(ref) => setSlot("lite", ref)}
            data-testid="lite-model-picker"
          />
        </section>

        <section class="den-settings-section" data-testid="models-worker-section">
          <h3 class="den-settings-subhead" {...chromeProps()}>{MODELS_SETTINGS_COPY.workerSectionTitle}</h3>
          <Show when={!projectLocal()}>
            <p class="den-settings-hint">{MODELS_SETTINGS_COPY.workerSectionHint}</p>
          </Show>
          <DenCheckbox
 disabled={props.saving}
            checked={usesWorkerPool(props.policy)}
            data-testid="models-worker-pool-toggle"
            onChange={(e) =>
              props.onChange(
                setWorkerPoolEnabled(props.policy, e.currentTarget.checked),
              )
            }
          >
            {MODELS_SETTINGS_COPY.workerPoolToggle}
          </DenCheckbox>
          <Show
            when={usesWorkerPool(props.policy)}
            fallback={
              <ModelSlotPicker
 disabled={props.saving}
                label={MODELS_SETTINGS_COPY.workerSlotLabel}
                hint={
                  projectLocal()
                    ? undefined
                    : MODELS_SETTINGS_COPY.workerSlotHint
                }
                providers={props.providers}
                roleSlot="agent_pool"
                showAllModels={showAll()}
                value={workerModelRef(props.policy)}
                onChange={(ref) =>
                  props.onChange(setWorkerModel(props.policy, ref))
                }
                data-testid="worker-model-picker"
              />
            }
          >
            <div
              class="den-settings-subsection"
              data-testid="models-worker-pool-panel"
            >
              <Show when={!projectLocal()}>
                <p class="den-settings-hint">{MODELS_SETTINGS_COPY.workerPoolIntro}</p>
              </Show>
              <ul class="den-settings-rules den-settings-pool-list">
                <li class="den-settings-rules-head" aria-hidden="true">
                  <span>{MODELS_SETTINGS_COPY.workerPoolListHeader}</span>
                  <span />
                </li>
                <Index each={props.policy.agent_pool.models}>
                  {(ref, index) => (
                    <li>
                      <ModelSlotPicker
 disabled={props.saving}
                        compact
                        label={`${MODELS_SETTINGS_COPY.workerPoolListHeader} ${index + 1}`}
                        providers={props.providers}
                        roleSlot="agent_pool"
                        showAllModels={showAll()}
                        value={ref()}
                        onChange={(r) => setPoolRow(index, r)}
                        data-testid={`pool-model-${index}`}
                      />
                      <DenButton
                        variant="danger"
                        disabled={props.saving || props.policy.agent_pool.models.length <= 1}
                        data-testid={`pool-remove-${index}`}
                        onClick={() =>
                          props.onChange(removePoolRow(props.policy, index))
                        }
                      >
                        Remove
                      </DenButton>
                    </li>
                  )}
                </Index>
              </ul>
              <div class="den-settings-actions">
                <DenButton
                  variant="secondary"
                  disabled={props.saving}
 data-testid="pool-add-row"
                  onClick={() =>
                    props.onChange(
                      addPoolRow(props.policy),
                    )
                  }
                >
                  {MODELS_SETTINGS_COPY.workerPoolAddLabel}
                </DenButton>
              </div>
              <div class="den-settings-worker-pool-selection">
                <DenField label={MODELS_SETTINGS_COPY.workerPoolSelectionLabel}>
                  <DenSelect
 disabled={props.saving}
                    class="w-full max-w-[480px]"
                    aria-label={MODELS_SETTINGS_COPY.workerPoolSelectionLabel}
                    data-testid="pool-selection-mode"
                    value={props.policy.agent_pool.selection}
                    options={(
                      Object.keys(
                        POOL_SELECTION_LABELS,
                      ) as ModelPolicy["agent_pool"]["selection"][]
                    ).map((mode) => ({
                      value: mode,
                      label: POOL_SELECTION_LABELS[mode],
                    }))}
                    onValueChange={(selection) =>
                      props.onChange({
                        ...props.policy,
                        agent_pool: {
                          ...props.policy.agent_pool,
                          selection:
                            selection as ModelPolicy["agent_pool"]["selection"],
                        },
                      })
                    }
                  />
                </DenField>
                <Show when={!projectLocal()}>
                  <p class="den-settings-hint">
                    {MODELS_SETTINGS_COPY.workerPoolSelectionHint}
                  </p>
                  <p class="den-settings-hint">
                    {MODELS_SETTINGS_COPY.workerPoolHint}
                  </p>
                </Show>
              </div>
            </div>
          </Show>
        </section>

        <Show when={props.saving}>
          <p class="den-settings-hint" data-testid="models-saving">
            {SETTINGS_EDITOR_COPY.saving}
          </p>
        </Show>
      </Show>
    </div>
  );
}
