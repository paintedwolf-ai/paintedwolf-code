import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import type { ProviderMeta } from "../../api/types.ts";
import { ModelSlotPicker } from "./ModelSlotPicker.tsx";
import { providerModel } from "../../test/provider-fixtures.ts";
import { createSignal } from "solid-js";
afterEach(cleanup);

it("reflects a save hold on every open picker choice and permits selection after it settles", () => {
  const [saving, setSaving] = createSignal(false);
  const provider = { id: "test", kind: "together", configured: true, ready_to_assign: true, models: [providerModel("new-model")] } as ProviderMeta;
  const change = vi.fn();
  render(() => <ModelSlotPicker label="Worker" providers={[provider]} roleSlot="agent_pool" disabled={saving()} value={{provider_id:"",model:""}} onChange={change} />);
  fireEvent.click(screen.getByRole("button", { name: "Worker" }));
  const option = screen.getByTestId("model-slot-picker-option-test-new-model");
  const unset = screen.getByTestId("model-slot-picker-unset");
  setSaving(true);
  expect(option).toHaveProperty("disabled", true);
  expect(unset).toHaveProperty("disabled", true);
  fireEvent.click(option);
  fireEvent.click(unset);
  expect(change).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "Worker" })).toBeTruthy();
  setSaving(false);
  expect(option).toHaveProperty("disabled", false);
  expect(unset).toHaveProperty("disabled", false);
  fireEvent.click(option);
  expect(change).toHaveBeenCalledExactlyOnceWith({ provider_id: "test", model: "new-model" });
});

it("allows an unknown tool capability without invoking a diagnostic", () => {
  const model=providerModel("new-model");
  model.eligibility.coordinator={state:"unverified",selectable:true,code:"tools_unknown",reason:"Tool support is unknown"};
  const provider={id:"test",kind:"together",configured:true,ready_to_assign:false,models:[model]} as ProviderMeta;
  const change=vi.fn();
  render(()=><ModelSlotPicker label="Coordinator" providers={[provider]} roleSlot="coordinator" value={{provider_id:"",model:""}} onChange={change}/>);
  fireEvent.click(screen.getByRole("button",{name:"Coordinator"}));
  const option=screen.getByTestId("model-slot-picker-option-test-new-model");
  expect(option).toHaveProperty("disabled",false);
  expect(option.textContent).toContain("Tool support is unknown");
  fireEvent.click(option);
  expect(change).toHaveBeenCalledWith({provider_id:"test",model:"new-model"});
});

it("Show all reveals an incompatible model but cannot select it", () => {
  const model=providerModel("special");
  model.eligibility.coordinator={state:"incompatible",selectable:false,code:"role_excluded",reason:"Specialty interface"};
  const provider={id:"test",kind:"together",configured:true,ready_to_assign:true,models:[model]} as ProviderMeta;
  render(()=><ModelSlotPicker label="Coordinator" providers={[provider]} roleSlot="coordinator" showAllModels value={{provider_id:"",model:""}} onChange={()=>{}}/>);
  fireEvent.click(screen.getByRole("button",{name:"Coordinator"}));
  expect(screen.getByTestId("model-slot-picker-option-test-special")).toHaveProperty("disabled",true);
});
