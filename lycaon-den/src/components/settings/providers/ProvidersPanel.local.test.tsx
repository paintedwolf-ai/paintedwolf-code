import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { includedProvider } from "../../../test/local-generation-fixture.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";

describe("included local model settings", () => {
  it("shows the admitted context without remote setup controls", async () => {
    const provider = includedProvider();
    const store = createSettingsStore({ providers: [provider] });
    render(() => <ProvidersPanel
      client={{ listProviders: vi.fn().mockResolvedValue([provider]), listProviderKinds: vi.fn().mockResolvedValue([]) } as never}
      settingsStore={store} providers={store.state.providers} onRemoveProvider={() => undefined}
    />);
    await waitFor(() => expect(screen.getByTestId("providers-row-nativegen")).toBeTruthy());
    fireEvent.click(screen.getByTestId("providers-row-nativegen"));
    expect((await screen.findByTestId("provider-name-nativegen")).textContent).toBe("Painted Wolf Code (included)");
    expect(screen.getByTestId("provider-included-summary-nativegen").classList.contains("den-activity-span")).toBe(true);
    expect(screen.getByTestId("local-model-context").textContent).toContain((16384).toLocaleString());
    expect(screen.queryByTestId("provider-remove-nativegen")).toBeNull();
    expect(screen.queryByTestId("provider-rename-trigger")).toBeNull();
    expect(screen.queryByTestId("provider-endpoint-nativegen")).toBeNull();
    expect(screen.getByTestId("provider-secret-trust-nativegen")).toBeTruthy();
  });

  it("offers repair while unconfigured and displays the host result", async () => {
    const damaged = includedProvider({ state: "repair_required", recovery_action: "repair" });
    const ready = includedProvider();
    const store = createSettingsStore({ providers: [damaged] });
    const repair = vi.fn().mockResolvedValue({ ok: true, status: ready.local_runtime });
    render(() => <ProvidersPanel
      client={{ listProviders: vi.fn().mockResolvedValue([ready]), listProviderKinds: vi.fn().mockResolvedValue([]), repairLocalGeneration: repair } as never}
      settingsStore={store} providers={store.state.providers} onRemoveProvider={() => undefined}
    />);
    await waitFor(() => expect(screen.getByTestId("providers-row-nativegen")).toBeTruthy());
    fireEvent.click(screen.getByTestId("providers-row-nativegen"));
    fireEvent.click(await screen.findByRole("button", { name: "Repair model" }));
    await waitFor(() => expect(repair).toHaveBeenCalledWith("nativegen"));
    await waitFor(() => expect(screen.getByTestId("local-model-context")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Repair model" })).toBeNull();
  });
});
