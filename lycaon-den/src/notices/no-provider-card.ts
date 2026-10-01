/** Host readiness code suppressed while the provider recovery card is visible. */
export const NO_PROVIDER_PREFLIGHT_CODE = "NO_PROVIDER_CONFIGURED";

export type ProviderConfigInput = {
  needsConfig: boolean;
  hasReadyProvider: boolean;
  sidecarConnected: boolean;
  modelsChecked: boolean;
  firstRunSetupCompleted: boolean;
};

export type ProviderGap = "no_provider" | "no_default_model";

/** Returns the missing configuration step after provider readiness settles. */
export function providerConfigGap(
  input: ProviderConfigInput,
): ProviderGap | undefined {
  // Wait for provider state before classifying missing configuration.
  if (!input.sidecarConnected || !input.modelsChecked) return undefined;
  if (!input.firstRunSetupCompleted) return undefined;
  if (!input.needsConfig) return undefined;
  return input.hasReadyProvider ? "no_default_model" : "no_provider";
}
