import type { ProviderMeta, ProviderModelMeta, ModelRoleEligibility } from "../../api/types.ts";

export type ModelRoleSlot = "coordinator" | "agent_pool" | "lite";

export function modelEligibilityForSlot(model: ProviderModelMeta, slot: ModelRoleSlot): ModelRoleEligibility {
  return model.eligibility[slot];
}

export function providerModelsVisibleForRoleSlot(provider: ProviderMeta, slot: ModelRoleSlot, showAll: boolean): ProviderMeta["models"] {
  return provider.models.filter((model) => showAll || modelEligibilityForSlot(model, slot).state !== "incompatible");
}
