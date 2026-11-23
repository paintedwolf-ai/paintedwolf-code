import type { LocalGenerationStatus, LocalModelTier, ProviderMeta } from "../api/types.ts";
import { providerModel } from "./provider-fixtures.ts";

export const publishedTier: LocalModelTier = {
  id: "gemma4-e4b-paintedwolf", display_name: "Small", description: "Leaves the most memory for your other apps.", download_bytes: 1024,
  min_memory_bytes: 8192, eligible: true, recommended: true, download_available: true,
};

export function includedProvider(status: LocalGenerationStatus = {
  state: "ready", recovery_action: "none", context_tokens: 16384, revision: "fixture-v1",
}): ProviderMeta {
  return {
    id: "nativegen", kind: "nativegen", label: "Painted Wolf Code (included)",
    base_url: "", endpoint_style: "derived", configured: status.state === "ready",
    ready_to_assign: status.state === "ready", credential_present: false, requires_api_key: false,
    features: { tool_calls: true, thinking: false, prompt_cache: "local_kv" },
    models: [providerModel("gemma4-e4b-paintedwolf", { context_length: 65536 })],
    local_runtime: status,
  };
}
