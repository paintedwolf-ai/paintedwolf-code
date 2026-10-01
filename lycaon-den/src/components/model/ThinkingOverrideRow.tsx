import { Show, createEffect, createMemo, createSignal, createUniqueId, on } from "solid-js";
import type { ModelRef, ThinkingCapabilities, ThinkingOverride } from "../../api/types.ts";
import { thinkingOptions, thinkingOverrideValid, thinkingValue, thinkingValueLabel } from "../../settings/providers/thinking-overrides.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenNumberInput } from "../primitives/DenNumberInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { SettingsListRow } from "../settings/SettingsListRow.tsx";

type Props = {
  model: ModelRef;
  providerLabel: string;
  capabilities: ThinkingCapabilities;
  override?: ThinkingOverride;
  inherited?: ThinkingOverride;
  project?: boolean;
  saving?: boolean;
  onChange: (override: ThinkingOverride | undefined) => void;
};

export function ThinkingOverrideRow(props: Props) {
  const id = createUniqueId();
  const [editing, setEditing] = createSignal(false);
  const [budget, setBudget] = createSignal<number>();
  const [budgetMode, setBudgetMode] = createSignal(false);
  const options = () => thinkingOptions(props.capabilities, Boolean(props.project || props.override?.mode === "application"));
  const effective = () => props.override ?? props.inherited;
  const valid = () => thinkingOverrideValid(effective(), props.capabilities);
  const enabled = () => Boolean(props.override || editing());
  const source = () => props.override ? (props.project ? "Project override" : "Device override") : props.inherited ? "Inherited from device settings" : "Chosen for each request";
  const budgetValid = () => {
    const value = budget();
    const range = props.capabilities.budget;
    return value != null && Number.isSafeInteger(value) && range != null && value >= range.min && (!range.max || value <= range.max);
  };
  const savedBudget = createMemo(() => props.override?.budget_tokens);
  const minimumBudget = createMemo(() => props.capabilities.budget?.min);
  const savedChoice = createMemo(() => thinkingValue(props.override));
  createEffect(on([savedBudget, minimumBudget], ([saved, minimum]) => {
    setBudget(saved ?? minimum);
  }));
  createEffect(on(savedChoice, (choice) => {
    setBudgetMode(choice === "budget");
    if (choice) setEditing(false);
  }));
  const choose = (value: string) => {
    setBudgetMode(value === "budget");
    if (value === "budget") return;
    const override: ThinkingOverride = { ...props.model, mode: value === "application" ? "application" : "fixed" };
    if (value.startsWith("effort:")) override.effort = value.slice(7);
    if (value === "on" || value === "off") override.enabled = value === "on";
    props.onChange(override);
  };
  const toggle = (checked: boolean) => {
    setEditing(checked);
    if (!checked) {
      setBudgetMode(false);
      if (props.override) props.onChange(undefined);
    }
  };
  const sourceLabel = () => {
    switch (props.capabilities.source) {
      case "provider-config": return "Provider configuration";
      case "provider-discovery": return "Provider model information";
      case "model-rule": return "Model family configuration";
      case "transport": return "Provider connection capabilities";
      default: return "No capability information";
    }
  };
  return (
    <div class="den-settings-subsection" data-testid="thinking-override-row">
      <SettingsListRow
        primary={props.model.model}
        secondary={props.providerLabel}
        variant={valid() ? "default" : "warning"}
        trailing={
          <DenCheckbox
            checked={enabled()}
            disabled={props.saving || (!enabled() && options().length === 0)}
            aria-label={`Override thinking for ${props.model.model} on ${props.providerLabel}`}
            onChange={(event) => toggle(event.currentTarget.checked)}
          >Override this model</DenCheckbox>
        }
      />
      <Show when={enabled() || props.inherited || options().length === 0 || props.capabilities.state !== "supported"}>
      <div class="den-settings-section">
        <Show when={enabled()}>
          <label class="den-settings-default-model-label" for={`${id}-level`}>Thinking setting</label>
          <DenSelect
            class="w-full max-w-[480px]"
            id={`${id}-level`}
            aria-label={`Thinking setting for ${props.model.model}`}
            value={budgetMode() ? "budget" : thinkingValue(props.override)}
            placeholder="Choose a thinking setting"
            options={options()}
            disabled={props.saving}
            onValueChange={choose}
          />
          <Show when={budgetMode() && props.capabilities.budget}>
            <div class="den-settings-provider-actions">
              <label for={`${id}-budget`}>Thinking tokens</label>
              <DenNumberInput
                id={`${id}-budget`}
                aria-label={`Thinking tokens for ${props.model.model}`}
                min={props.capabilities.budget?.min}
                max={props.capabilities.budget?.max}
                step={1}
                value={budget() ?? ""}
                disabled={props.saving}
                onInput={(event) => setBudget(event.currentTarget.value === "" ? undefined : event.currentTarget.valueAsNumber)}
              />
              <DenButton variant="secondary" disabled={props.saving || !budgetValid()} onClick={() => props.onChange({ ...props.model, mode: "fixed", budget_tokens: budget() })}>Apply budget</DenButton>
            </div>
            <p class="den-settings-hint">Minimum {props.capabilities.budget?.min.toLocaleString()} tokens{props.capabilities.budget?.max ? `; maximum ${props.capabilities.budget.max.toLocaleString()}` : ""}. The output limit must also leave room for an answer.</p>
          </Show>
        </Show>
        <p class="den-settings-hint" aria-live="polite">{valid() ? "Effective" : "Saved"}: {thinkingValueLabel(effective())} · {source()}</p>
        <Show when={!valid()}>
          <p class="den-settings-warn" role="alert">This override is unavailable for the current model configuration. Choose a supported setting or remove the override before using this model.</p>
        </Show>
        <Show when={props.capabilities.state === "unknown"}>
          <p class="den-settings-hint">Supported thinking settings are unknown. Refresh models or declare the controls in provider configuration.</p>
        </Show>
        <Show when={props.capabilities.state === "unsupported"}>
          <p class="den-settings-hint">This model connection does not offer thinking controls.</p>
        </Show>
        <Show when={props.capabilities.state === "supported" && thinkingOptions(props.capabilities, false).length === 0}>
          <p class="den-settings-hint">No adjustable thinking settings are available for this model configuration.</p>
        </Show>
        <Show when={props.capabilities.state === "supported" && options().length > 0}>
          <details class="den-settings-hint">
            <summary><span class="den-disclosure-caret" aria-hidden="true" />Supported settings</summary>
            <p>{sourceLabel()}: {thinkingOptions(props.capabilities, false).map((option) => option.label).join(", ") || "No adjustable controls"}.</p>
            <Show when={!props.capabilities.can_disable}><p>Thinking cannot be turned off with this configuration.</p></Show>
          </details>
        </Show>
      </div>
      </Show>
    </div>
  );
}
