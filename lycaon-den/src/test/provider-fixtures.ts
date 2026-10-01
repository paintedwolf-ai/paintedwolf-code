import type {
  ProviderModelCapabilities,
  ProviderModelMeta,
  ModelRoleEligibilitySet,
} from "../api/types.ts";

export function assignableModelCapabilities(
  overrides: Partial<ProviderModelCapabilities> = {},
): ProviderModelCapabilities {
  return {
    chat: { state: "supported" },
    streaming: { state: "supported" },
    tools: { state: "supported" },
    vision: { state: "unknown" },
    reasoning: { state: "unknown" },
    structured_output: { state: "unknown" },
    prompt_caching: { state: "unknown" },
    ...overrides,
  };
}

export function assignableModelEligibility(): ModelRoleEligibilitySet {
  const decision = { state: "eligible" as const, selectable: true, code: "compatible", reason: "The model supports this role." };
  return { coordinator: { ...decision }, agent_pool: { ...decision }, lite: { ...decision } };
}

export function providerModel(
  id: string,
  fields: Omit<Partial<ProviderModelMeta>, "id"> = {},
): ProviderModelMeta {
  return { id, capabilities: assignableModelCapabilities(), eligibility: assignableModelEligibility(), ...fields };
}
