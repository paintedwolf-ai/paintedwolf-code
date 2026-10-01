import { describe, expect, it } from "vitest";
import { modelEligibilityForSlot, providerModelsVisibleForRoleSlot } from "./models-role-filter.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import type { ProviderMeta } from "../../api/types.ts";

describe("host model eligibility", () => {
  it("uses resolved role decisions even when capability metadata differs", () => {
    const model = providerModel("tiny-1b", { context_length: 8192 });
    model.capabilities.tools.state = "unknown";
    expect(modelEligibilityForSlot(model,"coordinator").state).toBe("eligible");
    model.eligibility.coordinator = {state:"incompatible",selectable:false,code:"role_excluded",reason:"Specialty model"};
    expect(modelEligibilityForSlot(model,"lite").state).toBe("eligible");
    const provider = { models: [model] } as ProviderMeta;
    expect(providerModelsVisibleForRoleSlot(provider,"coordinator",false)).toEqual([]);
    expect(providerModelsVisibleForRoleSlot(provider,"coordinator",true)).toEqual([model]);
  });
  it("keeps unknown models visible for verification", () => {
    const model=providerModel("new-model");
    model.eligibility.coordinator={state:"unverified",selectable:true,code:"tools_unknown",reason:"Tool support is unknown"};
    expect(providerModelsVisibleForRoleSlot({models:[model]} as ProviderMeta,"coordinator",false)).toEqual([model]);
  });
});
