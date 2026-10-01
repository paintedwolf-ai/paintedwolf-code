import { describe, expect, it } from "vitest";
import { providerConfigGap } from "./no-provider-card.ts";

const ready = {
  needsConfig: true,
  hasReadyProvider: false,
  sidecarConnected: true,
  modelsChecked: true,
  firstRunSetupCompleted: true,
};

describe("providerConfigGap", () => {
  it("names the missing provider when setup was skipped", () => {
    expect(providerConfigGap(ready)).toBe("no_provider");
  });

  // Provider readiness distinguishes the missing configuration step.
  it("names the missing default model when a provider is already ready", () => {
    expect(providerConfigGap({ ...ready, hasReadyProvider: true })).toBe(
      "no_default_model",
    );
  });

  it("stays silent once a provider and default model exist", () => {
    expect(providerConfigGap({ ...ready, needsConfig: false })).toBeUndefined();
  });

  // Wait for provider state to avoid cold-start flashes.
  it("waits for the sidecar before judging provider state", () => {
    expect(
      providerConfigGap({ ...ready, sidecarConnected: false }),
    ).toBeUndefined();
  });

  it("waits for the model list to be fetched", () => {
    expect(providerConfigGap({ ...ready, modelsChecked: false })).toBeUndefined();
  });

  // First-run setup shows the provider prompt.
  it("defers to the first-run gate while it still controls the screen", () => {
    expect(
      providerConfigGap({ ...ready, firstRunSetupCompleted: false }),
    ).toBeUndefined();
  });
});
