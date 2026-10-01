import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { ThinkingOverrideRow } from "./ThinkingOverrideRow.tsx";

const model = { provider_id: "cloudflare-1", model: "glm-5.3-flash" };
const capabilities = { state: "supported" as const, efforts: ["low", "high", "max"], source: "model-rule" };

describe("thinking override controls", () => {
  it("requires opting in and choosing a native value before saving", () => {
    const change = vi.fn();
    render(() => <ThinkingOverrideRow model={model} providerLabel="Cloudflare" capabilities={capabilities} onChange={change} />);
    const checkbox = screen.getByRole("checkbox") as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    expect(screen.queryByRole("button", { name: /Thinking setting/ })).toBeNull();
    fireEvent.click(checkbox);
    expect(change).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Thinking setting/ }));
    expect(screen.queryByRole("option", { name: "Medium" })).toBeNull();
    expect(screen.queryByRole("option", { name: "Off" })).toBeNull();
    fireEvent.click(screen.getByRole("option", { name: "High" }));
    expect(change).toHaveBeenCalledWith({ ...model, mode: "fixed", effort: "high" });
  });
  it("lets a project clear a device override with application behavior", () => {
    const change = vi.fn();
    render(() => <ThinkingOverrideRow model={model} providerLabel="Cloudflare" capabilities={capabilities} project inherited={{ ...model, mode: "fixed", effort: "max" }} onChange={change} />);
    expect(screen.getByText(/Effective: Max/).textContent).toContain("Inherited from device settings");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /Thinking setting/ }));
    fireEvent.click(screen.getByRole("option", { name: "Use application behavior" }));
    expect(change).toHaveBeenCalledWith({ ...model, mode: "application" });
  });
  it("keeps an unavailable saved setting visible and removable", () => {
    const change = vi.fn();
    render(() => <ThinkingOverrideRow model={model} providerLabel="Cloudflare" capabilities={{ state: "unknown" }} override={{ ...model, mode: "fixed", effort: "max" }} onChange={change} />);
    expect(screen.getByRole("alert").textContent).toContain("unavailable");
    expect(screen.getByText(/Saved: Max/)).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(change).toHaveBeenCalledWith(undefined);
  });
  it("applies only a valid token budget", () => {
    const change = vi.fn();
    render(() => <ThinkingOverrideRow model={model} providerLabel="Provider" capabilities={{ state: "supported", budget: { min: 1024, max: 8192 } }} onChange={change} />);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /Thinking setting/ }));
    fireEvent.click(screen.getByRole("option", { name: "Token budget" }));
    const input = screen.getByRole("spinbutton");
    fireEvent.input(input, { target: { value: "512" } });
    expect((screen.getByRole("button", { name: "Apply budget" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.input(input, { target: { value: "4096" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply budget" }));
    expect(change).toHaveBeenCalledWith({ ...model, mode: "fixed", budget_tokens: 4096 });
  });
  it("keeps an unsaved budget when model metadata refreshes", () => {
    const [metadata, setMetadata] = createSignal({ state: "supported" as const, budget: { min: 1024, max: 8192 } });
    render(() => <ThinkingOverrideRow model={model} providerLabel="Provider" capabilities={metadata()} onChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /Thinking setting/ }));
    fireEvent.click(screen.getByRole("option", { name: "Token budget" }));
    fireEvent.input(screen.getByRole("spinbutton"), { target: { value: "4096" } });
    setMetadata({ state: "supported", budget: { min: 1024, max: 8192 } });
    expect((screen.getByRole("spinbutton") as HTMLInputElement).value).toBe("4096");
  });
});
