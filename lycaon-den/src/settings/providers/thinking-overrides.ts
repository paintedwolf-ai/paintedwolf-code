import type { ModelPolicy, ModelRef, ProviderMeta, ThinkingCapabilities, ThinkingOverride } from "../../api/types.ts";
import { modelRefKey } from "./models-editor-model.ts";

export function thinkingOverrideFor(policy: ModelPolicy | undefined, ref: ModelRef): ThinkingOverride | undefined {
  return policy?.thinking_overrides?.find((item) => modelRefKey(item) === modelRefKey(ref));
}

export function replaceThinkingOverride(policy: ModelPolicy, ref: ModelRef, override: ThinkingOverride | undefined): ThinkingOverride[] {
  const next = (policy.thinking_overrides ?? []).filter((item) => modelRefKey(item) !== modelRefKey(ref));
  if (override) next.push(override);
  return next;
}

export function thinkingModelRefs(policy: ModelPolicy, inherited?: ModelPolicy): ModelRef[] {
  const refs: ModelRef[] = [];
  for (const source of [policy, inherited]) {
    if (!source) continue;
    for (const slot of [source.coordinator, source.lite]) if (slot) refs.push(slot);
    refs.push(...source.agent_pool.models, ...(source.thinking_overrides ?? []));
  }
  return [...new Map(refs.filter((ref) => ref.provider_id && ref.model).map((ref) => [modelRefKey(ref), ref])).values()];
}

export function thinkingCapabilitiesFor(providers: readonly ProviderMeta[], ref: ModelRef): ThinkingCapabilities {
  return providers.find((provider) => provider.id === ref.provider_id)?.models.find((model) => model.id === ref.model)?.thinking ?? { state: "unknown" };
}

export function thinkingValue(override: ThinkingOverride | undefined): string {
  if (!override) return "";
  if (override.mode === "application") return "application";
  if (override.effort) return `effort:${override.effort}`;
  if (override.enabled != null) return override.enabled ? "on" : "off";
  if (override.budget_tokens != null) return "budget";
  return "";
}

export function thinkingValueLabel(override: ThinkingOverride | undefined): string {
  if (!override || override.mode === "application") return "Application behavior";
  if (override.effort) return override.effort.charAt(0).toUpperCase() + override.effort.slice(1);
  if (override.budget_tokens != null) return `${override.budget_tokens.toLocaleString()} thinking tokens`;
  return override.enabled ? "Thinking on" : "Thinking off";
}

export function thinkingOptions(capabilities: ThinkingCapabilities, project: boolean) {
  const options: { value: string; label: string }[] = project ? [{ value: "application", label: "Use application behavior" }] : [];
  if (capabilities.state !== "supported") return options;
  for (const effort of capabilities.efforts ?? []) options.push({ value: `effort:${effort}`, label: effort.charAt(0).toUpperCase() + effort.slice(1) });
  if (capabilities.can_enable) options.push({ value: "on", label: "On" });
  if (capabilities.can_disable) options.push({ value: "off", label: "Off" });
  if (capabilities.budget) options.push({ value: "budget", label: "Token budget" });
  return options;
}

export function thinkingOverrideValid(override: ThinkingOverride | undefined, capabilities: ThinkingCapabilities): boolean {
  if (!override || override.mode === "application") return true;
  if (!thinkingOptions(capabilities, false).some((option) => option.value === thinkingValue(override))) return false;
  if (override.budget_tokens != null) return Boolean(capabilities.budget && override.budget_tokens >= capabilities.budget.min && (!capabilities.budget.max || override.budget_tokens <= capabilities.budget.max));
  return true;
}
