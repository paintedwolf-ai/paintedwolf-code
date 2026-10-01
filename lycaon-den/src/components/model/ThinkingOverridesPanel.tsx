import { settingAnchor, settingLabel } from "../../settings/settings-registry.ts";
import { For, Show, createEffect, createMemo, createSignal, on } from "solid-js";
import type { ModelPolicy, ModelRef, ProviderMeta, ThinkingOverride } from "../../api/types.ts";
import { modelRefKey, parseModelRefKey } from "../../settings/providers/models-editor-model.ts";
import {
  replaceThinkingOverride,
  thinkingCapabilitiesFor,
  thinkingModelRefs,
  thinkingOverrideFor,
} from "../../settings/providers/thinking-overrides.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { SettingsListChrome } from "../settings/SettingsListChrome.tsx";
import { SettingsListGroup } from "../settings/SettingsListGroup.tsx";
import { ThinkingOverrideRow } from "./ThinkingOverrideRow.tsx";

type Props = {
  policy: ModelPolicy;
  inherited?: ModelPolicy;
  providers: readonly ProviderMeta[];
  project?: boolean;
  scopeKey?: string;
  saving?: boolean;
  onChange: (overrides: ThinkingOverride[]) => Promise<void>;
};

export function ThinkingOverridesPanel(props: Props) {
  const [extra, setExtra] = createSignal<ModelRef[]>([]);
  const [adding, setAdding] = createSignal(false);
  const [enabledLocally, setEnabledLocally] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal<string>();
  const enabled = () => enabledLocally() || (props.policy.thinking_overrides?.length ?? 0) > 0;
  const busy = () => saving() || props.saving;

  const rows = createMemo(() => [
    ...new Set([...thinkingModelRefs(props.policy, props.inherited), ...extra()].map(modelRefKey)),
  ]);
  const remaining = () => props.providers.flatMap((provider) =>
    provider.models.map((model) => ({
      value: modelRefKey({ provider_id: provider.id, model: model.id }),
      label: `${provider.label || provider.id} · ${model.id}`,
    })),
  ).filter((option) => !rows().includes(option.value));

  const close = () => {
    setEnabledLocally(false);
    setExtra([]);
    setAdding(false);
  };
  createEffect(on(() => props.scopeKey, () => {
    close();
    setError(undefined);
  }));

  const save = async (overrides: ThinkingOverride[]) => {
    setSaving(true);
    setError(undefined);
    try {
      await props.onChange(overrides);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setSaving(false);
    }
  };
  const change = (ref: ModelRef, override: ThinkingOverride | undefined) => {
    setEnabledLocally(true);
    void save(replaceThinkingOverride(props.policy, ref, override));
  };
  const toggle = async (checked: boolean) => {
    if (checked) {
      setEnabledLocally(true);
      return;
    }
    if (!props.policy.thinking_overrides?.length || await save([])) close();
  };

  return (
    <section
      class="den-settings-section"
      data-testid="thinking-overrides"
      {...(props.project ? {} : settingAnchor("thinking-overrides"))}
    >
      <DenCheckbox
        checked={enabled()}
        disabled={busy()}
        onChange={(event) => void toggle(event.currentTarget.checked)}
      >
        {props.project ? "Override thinking in this project" : settingLabel("thinking-overrides")}
      </DenCheckbox>
      <Show when={!enabled()}>
        <p class="den-settings-hint">
          {props.project
            ? "Uses device thinking settings."
            : "Painted Wolf Code chooses thinking for each request."}
        </p>
      </Show>
      <Show when={enabled()}>
        <SettingsListGroup label="Thinking overrides" meta="Optional">
          <p class="den-settings-hint">
            {props.project
              ? "Inherit device settings, or choose an override for this project."
              : "Choose which models use a fixed thinking setting."}
          </p>
          <For each={rows()}>{(key) => {
            const ref = parseModelRefKey(key);
            if (!ref) return null;
            return (
              <ThinkingOverrideRow
                model={ref}
                providerLabel={props.providers.find((provider) => provider.id === ref.provider_id)?.label || ref.provider_id}
                capabilities={thinkingCapabilitiesFor(props.providers, ref)}
                override={thinkingOverrideFor(props.policy, ref)}
                inherited={thinkingOverrideFor(props.inherited, ref)}
                project={props.project}
                saving={busy()}
                onChange={(override) => change(ref, override)}
              />
            );
          }}</For>
        </SettingsListGroup>
        <SettingsListChrome
          count={`${props.policy.thinking_overrides?.length ?? 0} ${props.policy.thinking_overrides?.length === 1 ? "override" : "overrides"}`}
          action={
            <DenButton
              variant="secondary"
              disabled={busy() || remaining().length === 0}
              onClick={() => setAdding(!adding())}
            >
              {adding() ? "Cancel" : "Add a model override"}
            </DenButton>
          }
        />
        <Show when={adding()}>
          <DenSelect
            aria-label="Model for thinking override"
            placeholder="Choose a model"
            options={remaining()}
            disabled={busy()}
            onValueChange={(key) => {
              const ref = parseModelRefKey(key);
              if (ref) setExtra([...extra(), ref]);
              setAdding(false);
            }}
          />
        </Show>
        <p class="den-settings-hint">
          Applies to every use of that provider and model, including agents, summaries,
          and retries. Changes take effect on the next turn.
        </p>
      </Show>
      <Show when={error()}>
        <p class="den-settings-warn" role="alert">{error()}</p>
      </Show>
    </section>
  );
}
