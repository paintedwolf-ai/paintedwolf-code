import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import type { ModelPolicy } from "../../api/types.ts";
import { ThinkingOverridesPanel } from "./ThinkingOverridesPanel.tsx";

const ref = { provider_id: "provider", model: "model" };
const policy: ModelPolicy = { coordinator: ref, lite: ref, agent_pool: { selection: "first", models: [] } };

describe("thinking section opt-in", () => {
  it("hides the whole section until enabled without writing a policy", () => {
    const change = vi.fn();
    render(() => <ThinkingOverridesPanel policy={policy} providers={[]} onChange={change} />);
    expect(screen.queryByRole("heading", { name: "Thinking overrides" })).toBeNull();
    expect(screen.queryByText("model")).toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: "Override thinking" }));
    expect(screen.getByRole("heading", { name: "Thinking overrides" })).toBeTruthy();
    expect(change).not.toHaveBeenCalled();
  });
  it("turns off every local override and hides the section", async () => {
    const [saved, setSaved] = createSignal<ModelPolicy>({ ...policy, thinking_overrides: [{ ...ref, mode: "application" }] });
    const change = vi.fn(async (thinking_overrides) => { setSaved({ ...saved(), thinking_overrides }); });
    render(() => <ThinkingOverridesPanel policy={saved()} providers={[]} project onChange={change} />);
    expect(screen.getByRole("heading", { name: "Thinking overrides" })).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox", { name: "Override thinking in this project" }));
    await waitFor(() => expect(change).toHaveBeenCalledWith([]));
    expect(screen.queryByRole("heading", { name: "Thinking overrides" })).toBeNull();
    expect(screen.getByText("Uses device thinking settings.")).toBeTruthy();
  });
  it("keeps the section open if clearing overrides fails", async () => {
    const change = vi.fn().mockRejectedValue(new Error("Could not save"));
    render(() => <ThinkingOverridesPanel policy={{ ...policy, thinking_overrides: [{ ...ref, mode: "application" }] }} providers={[]} onChange={change} />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Override thinking" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("Could not save"));
    expect(screen.getByRole("heading", { name: "Thinking overrides" })).toBeTruthy();
  });
});
