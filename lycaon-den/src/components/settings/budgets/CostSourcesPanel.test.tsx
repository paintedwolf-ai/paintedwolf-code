import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { SettingsPricing, SettingsPricingResponse } from "../../../api/types.ts";
import { COST_SETTINGS_COPY } from "../../../settings/budgets/cost-settings-copy.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { CostSourcesPanel } from "./CostSourcesPanel.tsx";

const basePricing: SettingsPricingResponse = {
  cost_tracking_enabled: false,
  sources: [
    { id: "models-dev", enabled: true },
    { id: "litellm", enabled: false },
  ],
  available_sources: [
    {
      id: "models-dev",
      label: "models.dev",
      kind: "models-dev",
      enabled: true,
      status: "offline",
    },
    {
      id: "litellm",
      label: "LiteLLM",
      kind: "litellm",
      enabled: false,
      status: "offline",
    },
  ],
};

const trackingOn: SettingsPricingResponse = {
  ...basePricing,
  cost_tracking_enabled: true,
};

function echo(body: SettingsPricing): SettingsPricingResponse {
  const nextSources = body.sources ?? basePricing.sources;
  return {
    ...basePricing,
    ...body,
    cost_tracking_enabled: body.cost_tracking_enabled ?? basePricing.cost_tracking_enabled,
    sources: nextSources,
    available_sources: basePricing.available_sources.map((s) => ({
      ...s,
      enabled: nextSources.find((x) => x.id === s.id)?.enabled ?? s.enabled,
    })),
  };
}

function mockClient(overrides: Record<string, unknown> = {}) {
  return {
    updatePricingSettings: vi.fn().mockImplementation(async (body: SettingsPricing) => echo(body)),
    refreshPricingSource: vi.fn().mockResolvedValue({
      id: "models-dev",
      label: "models.dev",
      kind: "models-dev",
      enabled: true,
      status: "ok",
      model_count: 3,
    }),
    ...overrides,
  };
}

function renderPanel(pricing: SettingsPricingResponse, client = mockClient()) {
  // The store adopts and reconciles its initial objects in place; keep fixtures pristine.
  const store = createSettingsStore({ providers: [], pricing: structuredClone(pricing) });
  const view = render(() => (
    <CostSourcesPanel client={client as never} settingsStore={store} />
  ));
  return { ...view, store, client };
}

describe("CostSourcesPanel", () => {
  it("greys source cards when tracking is off", () => {
    const { getByTestId } = renderPanel(basePricing);
    expect(getByTestId("cost-source-card-models-dev").className).toContain(
      "den-cost-source-card--idle",
    );
    expect(getByTestId("cost-tracking-off-hint").textContent).toContain(
      COST_SETTINGS_COPY.mainOffSourcesHint.slice(0, 20),
    );
  });

  it("offers sources as one radio group", () => {
    const { getByTestId } = renderPanel(trackingOn);
    expect(getByTestId("cost-sources-list").getAttribute("role")).toBe("radiogroup");
    const models = getByTestId("cost-source-select-models-dev") as HTMLInputElement;
    const litellm = getByTestId("cost-source-select-litellm") as HTMLInputElement;
    expect(models.type).toBe("radio");
    expect(models.name).toBe(litellm.name);
    expect(models.checked).toBe(true);
    expect(litellm.checked).toBe(false);
  });

  it("applies a selection before the save returns and keeps controls live", async () => {
    let resolvePut: ((value: SettingsPricingResponse) => void) | undefined;
    const client = mockClient({
      updatePricingSettings: vi.fn().mockImplementation(
        (body: SettingsPricing) =>
          new Promise<SettingsPricingResponse>((resolve) => {
            resolvePut = () => resolve(echo(body));
          }),
      ),
    });
    const { getByTestId, store } = renderPanel(trackingOn, client);

    fireEvent.click(getByTestId("cost-source-select-litellm"));

    expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("litellm");
    expect((getByTestId("cost-source-select-litellm") as HTMLInputElement).checked).toBe(true);
    expect((getByTestId("cost-source-select-models-dev") as HTMLInputElement).disabled).toBe(false);
    expect((getByTestId("cost-tracking-toggle") as HTMLInputElement).disabled).toBe(false);

    await waitFor(() => expect(resolvePut).toBeDefined());
    resolvePut?.(trackingOn);
    await waitFor(() => expect(client.updatePricingSettings).toHaveBeenCalledTimes(1));
  });

  it("saves rapid choices in order and keeps the last one", async () => {
    const releases: Array<() => void> = [];
    const client = mockClient({
      updatePricingSettings: vi.fn().mockImplementation(
        (body: SettingsPricing) =>
          new Promise<SettingsPricingResponse>((resolve) => {
            releases.push(() => resolve(echo(body)));
          }),
      ),
    });
    const { getByTestId, store } = renderPanel(trackingOn, client);

    fireEvent.click(getByTestId("cost-source-select-litellm"));
    expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("litellm");
    fireEvent.click(getByTestId("cost-source-select-models-dev"));
    expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("models-dev");

    await waitFor(() => expect(releases).toHaveLength(1));
    releases[0]!();
    await vi.waitFor(() =>
      expect({ puts: releases.length, error: store.state.error }).toEqual({
        puts: 2,
        error: undefined,
      }),
    );
    expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("models-dev");
    releases[1]!();
    await waitFor(() => expect(client.updatePricingSettings).toHaveBeenCalledTimes(2));
    expect(client.updatePricingSettings.mock.calls[1]![0].sources).toEqual([
      { id: "models-dev", enabled: true },
      { id: "litellm", enabled: false },
    ]);
    await waitFor(() =>
      expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("models-dev"),
    );
  });

  it("shows fetch progress from the host and disables refresh meanwhile", () => {
    const { getByTestId } = renderPanel({
      ...trackingOn,
      available_sources: trackingOn.available_sources.map((s) =>
        s.id === "models-dev" ? { ...s, refreshing: true } : s,
      ),
    });
    expect(getByTestId("cost-source-hint-models-dev").textContent).toContain(
      COST_SETTINGS_COPY.fetching,
    );
    const refresh = getByTestId("cost-source-refresh-models-dev") as HTMLButtonElement;
    expect(refresh.disabled).toBe(true);
    expect(refresh.textContent).toBe(COST_SETTINGS_COPY.refreshing);
  });

  it("blocks enabling tracking with no sources and does not PUT", async () => {
    const { getByTestId, client } = renderPanel({
      ...basePricing,
      sources: basePricing.sources.map((source) => ({ ...source, enabled: false })),
    });

    const toggle = getByTestId("cost-tracking-toggle") as HTMLInputElement;
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(getByTestId("cost-tracking-needs-source")).toBeTruthy(),
    );
    expect(client.updatePricingSettings).not.toHaveBeenCalled();
    expect(screen.getByTestId("cost-tracking-needs-source").textContent).toBe(
      COST_SETTINGS_COPY.needsSource,
    );
    expect(toggle.checked).toBe(false);
  });

  it("refreshes only the selected source", async () => {
    const { getByTestId, queryByTestId, client, store } = renderPanel(trackingOn);

    expect(queryByTestId("cost-source-refresh-litellm")).toBeNull();
    fireEvent.click(getByTestId("cost-source-refresh-models-dev"));
    await waitFor(() =>
      expect(client.refreshPricingSource).toHaveBeenCalledWith("models-dev"),
    );
    await waitFor(() =>
      expect(store.state.pricing?.available_sources[0]?.status).toBe("ok"),
    );
  });

  it("reverts a failed source choice to the confirmed settings", async () => {
    const client = mockClient({
      updatePricingSettings: vi.fn().mockRejectedValue(new Error("save failed")),
    });
    const { getByTestId, store } = renderPanel(trackingOn, client);
    const litellm = getByTestId("cost-source-select-litellm") as HTMLInputElement;

    fireEvent.click(litellm);

    await waitFor(() => expect(store.state.error).toContain("save failed"));
    expect(store.state.pricing?.sources.find((s) => s.enabled)?.id).toBe("models-dev");
    expect(litellm.checked).toBe(false);
    expect((getByTestId("cost-source-select-models-dev") as HTMLInputElement).checked).toBe(true);
  });

  it("reverts a failed tracking toggle", async () => {
    const client = mockClient({
      updatePricingSettings: vi.fn().mockRejectedValue(new Error("save failed")),
    });
    const { getByTestId, store } = renderPanel(basePricing, client);
    const toggle = getByTestId("cost-tracking-toggle") as HTMLInputElement;

    fireEvent.click(toggle);

    await waitFor(() => expect(store.state.error).toContain("save failed"));
    await waitFor(() => expect(toggle.checked).toBe(false));
    expect(store.state.pricing?.cost_tracking_enabled).toBe(false);
  });
});
