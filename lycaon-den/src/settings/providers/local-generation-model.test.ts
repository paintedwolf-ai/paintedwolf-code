import { describe, expect, it } from "vitest";
import { includedProvider } from "../../test/local-generation-fixture.ts";
import { groupProvidersByKind, isIncludedProvider, providerPickerGroup, providerStatusLabel } from "./models-editor-model.ts";
import { localGenerationView } from "./local-generation-model.ts";

describe("included model readiness", () => {
  it("keeps the bundled provider first in the local group", () => {
    const included = includedProvider();
    expect(isIncludedProvider(included)).toBe(true);
    expect(providerPickerGroup(included)).toBe("local");
    expect(groupProvidersByKind([included])[0]?.kind).toBe("nativegen");
  });

  it("uses host recovery facts even when a catalog still claims readiness", () => {
    const included = { ...includedProvider({ state: "repair_required", recovery_action: "repair" }), ready_to_assign: true };
    expect(localGenerationView(included)).toMatchObject({ ready: false, action: "repair", actionLabel: "Repair model" });
    expect(providerStatusLabel(included)).toBe("Repair needed");
  });

  it("does not infer recovery from diagnostic prose", () => {
    const included = includedProvider({ state: "unavailable", recovery_action: "retry", detail: "repair required; reinstall" });
    expect(localGenerationView(included)).toMatchObject({ action: "retry", reinstall: false });
    expect(localGenerationView({})).toMatchObject({ ready: false, action: "none" });
  });

  it("distinguishes loading, memory admission and reinstall", () => {
    expect(localGenerationView(includedProvider({ state: "loading", recovery_action: "none" })).busy).toBe(true);
    expect(providerStatusLabel(includedProvider({ state: "insufficient_memory", recovery_action: "retry" }))).toBe("Not enough memory");
    expect(localGenerationView(includedProvider({ state: "unavailable", recovery_action: "reinstall" })).reinstall).toBe(true);
  });
});
