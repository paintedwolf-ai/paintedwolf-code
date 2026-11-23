import { expect, it } from "vitest";
import { includedProvider, publishedTier } from "../../test/local-generation-fixture.ts";
import { modelAssignmentOptionGroupsForPicker, modelAssignmentOptions } from "./models-editor-model.ts";

it("names an installed or unavailable local assignment from the host catalog", () => {
  const provider = includedProvider({state: "ready", recovery_action: "none", available_models: [publishedTier]});
  const ref = {provider_id: provider.id, model: publishedTier.id};
  expect(modelAssignmentOptions([provider])).toContainEqual(expect.objectContaining({...ref, title: expect.stringContaining(" — Small")}));
  const groups = modelAssignmentOptionGroupsForPicker([{...provider, configured:false}], ref);
  expect(groups.flatMap(group => group.options)).toContainEqual(expect.objectContaining({...ref, disabled:true, title: expect.stringContaining(" — Small")}));
});
