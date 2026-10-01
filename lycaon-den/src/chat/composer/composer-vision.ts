import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";

/** Host-declared vision support for the coordinator model. */
export type CoordinatorVisionSupport = "supported" | "unsupported" | "unknown";

/** Missing catalog entries leave vision support unknown. */
export function coordinatorModelVisionSupport(
  providers: readonly ProviderMeta[],
  policy: ModelPolicy | undefined | null,
): CoordinatorVisionSupport {
  const model = policy?.coordinator?.model?.trim();
  const providerId = policy?.coordinator?.provider_id?.trim();
  if (!model) return "unknown";
  for (const provider of providers) {
    if (providerId && provider.id !== providerId) continue;
    for (const row of provider.models ?? []) {
      if (row.id === model) {
        const state = row.capabilities?.vision.state;
        return state === "supported" || state === "unsupported" ? state : "unknown";
      }
    }
  }
  return "unknown";
}
