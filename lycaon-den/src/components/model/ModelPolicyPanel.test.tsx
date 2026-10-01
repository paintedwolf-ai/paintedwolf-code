import { createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import type { ModelPolicy, ProviderMeta } from "../../api/types.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import { ModelPolicyPanel } from "./ModelPolicyPanel.tsx";

afterEach(cleanup);

it.each([0, 1])("preserves an open pool picker across an acknowledged policy refresh at row %s", (index) => {
  const initial: ModelPolicy = {
    coordinator: { provider_id: "test", model: "old" },
    lite: { provider_id: "test", model: "old" },
    agent_pool: { selection: "random", models: [{ provider_id: "test", model: "old" }, { provider_id: "", model: "" }] },
  };
  const [policy, setPolicy] = createSignal(initial);
  const change = vi.fn<(value: ModelPolicy) => void>();
  const provider = { id: "test", kind: "together", configured: true, ready_to_assign: true, models: [providerModel("old"), providerModel("new")] } as ProviderMeta;
  render(() => <ModelPolicyPanel policy={policy()} providers={[provider]} hasStoredAssignments saving={false} onChange={change} projectLocal />);
  fireEvent.click(screen.getByTestId(`pool-model-${index}`));
  const dialog = screen.getByRole("dialog", { name: `Model ${index + 1}` });
  const filter = screen.getByTestId(`pool-model-${index}-filter`) as HTMLInputElement;
  fireEvent.input(filter, { target: { value: "new" } });
  setPolicy({ ...structuredClone(initial), agent_pool: { ...structuredClone(initial.agent_pool), selection: "first" } });
  expect(screen.getByRole("dialog", { name: `Model ${index + 1}` })).toBe(dialog);
  expect(filter.value).toBe("new");
  fireEvent.click(screen.getByTestId(`pool-model-${index}-option-test-new`));
  expect(change).toHaveBeenCalledOnce();
  const next = change.mock.calls[0]?.[0];
  if (!next) throw new Error("Expected the selected model policy");
  expect(next.agent_pool.selection).toBe("first");
  expect(next.agent_pool.models[index]).toEqual({ provider_id: "test", model: "new" });
  expect(next.agent_pool.models[1 - index]).toEqual(initial.agent_pool.models[1 - index]);
});
