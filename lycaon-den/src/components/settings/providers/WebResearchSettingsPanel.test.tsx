import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { WebResearchSettingsPanel } from "./WebResearchSettingsPanel.tsx";
import { WEB_RESEARCH_ACTIVITY_PAGE_SIZE } from "../../../settings/research/web-research-activity.ts";
import { WEB_RESEARCH_SETTINGS_COPY } from "../../../settings/research/web-research-settings-copy.ts";
import { WEB_RESEARCH_CATALOG } from "../../../settings/web-research-catalog.generated.ts";
import type {
  WebResearchProvidersResponse,
  WebResearchSettings,
} from "../../../api/types.ts";

afterEach(cleanup);

type SampleStatus = WebResearchProvidersResponse & WebResearchSettings;

function sampleStatus(
  overrides: Partial<SampleStatus> = {},
): SampleStatus {
  return {
    warming: true,
    guess_domains: true,
    search_enabled: true,
    direct: {
      configured: true,
      card: { provider_id: "direct", kind: "direct", label: "Direct search" },
    },
    providers: WEB_RESEARCH_CATALOG.slice(0, 2).map((entry) => ({
      id: entry.id,
      kind: entry.kind,
      label: entry.label,
      roles: [...entry.roles],
      default_enabled: entry.default_enabled,
      configured: true,
      credential_present: true,
      credential_source: "stored",
      credential_slot: entry.credential_slot,
    })),
    enabled_providers: ["direct"],
    ...overrides,
  };
}

describe("WebResearchSettingsPanel", () => {
  it("does not persist preferences merely by opening settings", async () => {
    const put = vi.fn().mockResolvedValue(sampleStatus());
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: put,
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));
    await waitFor(() =>
      expect(getByTestId("web-research-enabled-toggle")).toBeTruthy()
    );
    await new Promise((resolve) => setTimeout(resolve, 350));
    expect(put).not.toHaveBeenCalled();
  });

  it("finishes a user-triggered preference write after unmount", async () => {
    const put = vi.fn().mockResolvedValue(
      sampleStatus({ search_enabled: false }),
    );
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: put,
    };

    const view = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));
    await waitFor(() =>
      expect(view.getByTestId("web-research-enabled-toggle")).toBeTruthy()
    );
    const toggle = view.getByTestId("web-research-enabled-toggle");
    fireEvent.click(toggle);
    view.unmount();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put.mock.calls[0]?.[0]).toMatchObject({ search_enabled: false });
  });

  it("hydrates and clears a saved custom provider endpoint", async () => {
    const status = sampleStatus({
      enabled_providers: ["searxng"],
      providers: [
        {
          id: "searxng",
          kind: "keyless_endpoint",
          label: "SearxNG",
          roles: ["results"],
          default_enabled: false,
          configured: true,
          credential_present: false,
          credential_source: "none",
          allow_private_endpoint: true,
          config: { endpoint: "https://search.example.test" },
        },
      ],
    });
    const cleared = {
      ...status,
      providers: status.providers.map((provider) => ({
        ...provider,
        config: { endpoint: "https://default.example.test" },
      })),
    };
    const putConfig = vi.fn().mockResolvedValue(cleared.providers[0]);
    const client = {
      getWebResearchProviders: vi
        .fn()
        .mockResolvedValueOnce(status)
        .mockResolvedValue(cleared),
      getWebResearchSettings: vi.fn().mockResolvedValue(status),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(status),
      updateWebResearchProvider: putConfig,
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));
    await waitFor(() =>
      expect(getByTestId("web-research-row-searxng")).toBeTruthy()
    );
    fireEvent.click(getByTestId("web-research-row-searxng"));
    await waitFor(() =>
      expect(getByTestId("web-research-endpoint-searxng")).toBeTruthy()
    );
    const endpoint = getByTestId(
      "web-research-endpoint-searxng",
    ) as HTMLInputElement;
    expect(endpoint.value).toBe("https://search.example.test");
    fireEvent.input(endpoint, { target: { value: "" } });
    fireEvent.click(getByTestId("web-research-save-config-searxng"));

    await waitFor(() => expect(putConfig).toHaveBeenCalledTimes(1));
    expect(putConfig.mock.calls[0]?.[1]).toEqual({ config: { endpoint: "" } });
    await waitFor(() =>
      expect(
        (getByTestId("web-research-endpoint-searxng") as HTMLInputElement)
          .value,
      ).toBe("https://default.example.test")
    );
  });

  it("defaults to Providers tab with Local index available", async () => {
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-tab-providers")).toBeTruthy();
    });
    expect(
      getByTestId("web-research-tab-providers").getAttribute("data-active"),
    ).toBe(
      "true",
    );
    expect(getByTestId("web-research-panel-providers").hidden).toBe(false);
    expect(getByTestId("web-research-panel-index").hidden).toBe(true);

    fireEvent.click(getByTestId("web-research-tab-index"));
    expect(getByTestId("web-research-panel-index").hidden).toBe(false);
    expect(getByTestId("web-research-panel-providers").hidden).toBe(true);
  });

  it("shows Allow web research under Providers covering search, pages, and worker", async () => {
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-enabled-toggle")).toBeTruthy();
    });
    const providers = getByTestId("web-research-panel-providers");
    const enable = getByTestId("web-research-enable");
    expect(providers.contains(enable)).toBe(true);
    expect(enable.textContent).toContain(
      WEB_RESEARCH_SETTINGS_COPY.enableLabel,
    );
    expect(
      (getByTestId("web-research-enabled-toggle") as HTMLInputElement).checked,
    ).toBe(true);

    fireEvent.click(getByTestId("web-research-tab-index"));
    expect(getByTestId("web-research-panel-providers").hidden).toBe(true);
    expect(getByTestId("web-research-panel-index").contains(enable)).toBe(
      false,
    );
  });

  it("disables details and shows note when main toggle is off", async () => {
    const put = vi.fn().mockImplementation(async (
      req: { search_enabled: boolean },
    ) => sampleStatus({ search_enabled: req.search_enabled }));
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: put,
    };

    const { getByTestId, queryByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-details")).toBeTruthy();
    });
    expect(
      (getByTestId("web-research-details") as HTMLFieldSetElement).disabled,
    ).toBe(
      false,
    );
    expect(queryByTestId("web-research-disabled-note")).toBeNull();

    fireEvent.click(getByTestId("web-research-enabled-toggle"));

    await waitFor(() => {
      expect(
        (getByTestId("web-research-details") as HTMLFieldSetElement).disabled,
      ).toBe(true);
      expect(getByTestId("web-research-disabled-note").textContent).toContain(
        WEB_RESEARCH_SETTINGS_COPY.disabledNote.slice(0, 24),
      );
    });

    fireEvent.click(getByTestId("web-research-tab-index"));
    expect(
      (getByTestId("web-research-warming-toggle") as HTMLInputElement).disabled,
    ).toBe(true);

    await waitFor(() => {
      expect(put).toHaveBeenCalled();
      const calls = put.mock.calls as Array<[{ search_enabled: boolean }]>;
      const last = calls[calls.length - 1]?.[0];
      expect(last?.search_enabled).toBe(false);
    });
  });

  it("renders Direct in the shared list with Add in list chrome", async () => {
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
    };

    const { getByTestId, queryByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-list")).toBeTruthy();
    });
    expect(getByTestId("web-research-list-chrome")).toBeTruthy();
    expect(getByTestId("web-research-add")).toBeTruthy();
    expect(getByTestId("web-research-row-direct")).toBeTruthy();
    expect(queryByTestId("web-research-detail")).toBeNull();

    fireEvent.click(getByTestId("web-research-row-direct"));
    await waitFor(() => {
      expect(getByTestId("web-research-direct-card")).toBeTruthy();
      expect(getByTestId("web-research-back")).toBeTruthy();
      expect(queryByTestId("web-research-add")).toBeNull();
    });

    fireEvent.click(getByTestId("web-research-back"));
    await waitFor(() => {
      expect(getByTestId("web-research-add")).toBeTruthy();
    });
    fireEvent.click(getByTestId("web-research-add"));
    await waitFor(() => {
      expect(screen.getByTestId("web-research-add-dialog")).toBeTruthy();
      expect(screen.getByTestId("web-research-add-form")).toBeTruthy();
      expect(getByTestId("web-research-list")).toBeTruthy();
      expect(getByTestId("web-research-add")).toBeTruthy();
    });
    expect(queryByTestId("web-research-detail")).toBeNull();
    expect(queryByTestId("web-research-back")).toBeNull();

    fireEvent.click(getByTestId("web-research-tab-index"));
    expect(getByTestId("web-research-index-section")).toBeTruthy();
    expect(getByTestId("web-research-warming-row")).toBeTruthy();
    expect(
      (getByTestId("web-research-warming-toggle") as HTMLInputElement).checked,
    ).toBe(true);
  });

  it("shows credentials for keyless providers with an optional credential slot", async () => {
    const status = sampleStatus({
      enabled_providers: ["direct", "github"],
      providers: [
        {
          id: "github",
          kind: "keyless",
          label: "GitHub",
          roles: ["results", "seeds"],
          configured: true,
          credential_present: false,
          credential_source: "none",
          credential_slot: "github",
        },
      ],
    });
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(status),
      getWebResearchSettings: vi.fn().mockResolvedValue(status),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(status),
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));
    await waitFor(() =>
      expect(getByTestId("web-research-row-direct")).toBeTruthy()
    );
    fireEvent.click(getByTestId("web-research-row-direct"));
    await waitFor(() =>
      expect(getByTestId("web-research-key-github")).toBeTruthy()
    );
    expect(getByTestId("web-research-key-status-github").textContent).toBe(
      WEB_RESEARCH_SETTINGS_COPY.apiKeyOptionalStatus,
    );
    expect(
      getByTestId("web-research-key-status-github").classList.contains(
        "den-settings-warn",
      ),
    ).toBe(false);
  });

  it("puts Guess domains on Direct detail and Warming on Local index", async () => {
    const put = vi.fn().mockImplementation(
      async (req: { warming: boolean; guess_domains: boolean }) =>
        sampleStatus({
          warming: req.warming,
          guess_domains: req.guess_domains,
        }),
    );
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: put,
    };

    const { getByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-row-direct")).toBeTruthy();
    });
    fireEvent.click(getByTestId("web-research-row-direct"));
    await waitFor(() => {
      expect(getByTestId("web-research-domain-guess-toggle")).toBeTruthy();
    });
    expect(getByTestId("web-research-direct-options")).toBeTruthy();
    expect(
      (getByTestId("web-research-domain-guess-toggle") as HTMLInputElement)
        .checked,
    ).toBe(true);
    expect(getByTestId("web-research-direct-card").textContent).toContain(
      WEB_RESEARCH_SETTINGS_COPY.domainGuessLabel,
    );

    fireEvent.click(getByTestId("web-research-domain-guess-toggle"));
    await waitFor(() => {
      const calls = put.mock.calls as Array<
        [{ warming: boolean; guess_domains: boolean }]
      >;
      const last = calls[calls.length - 1]?.[0];
      expect(last?.warming).toBe(true);
      expect(last?.guess_domains).toBe(false);
    });

    fireEvent.click(getByTestId("web-research-tab-index"));
    expect(
      (getByTestId("web-research-warming-toggle") as HTMLInputElement).checked,
    ).toBe(true);

    fireEvent.click(getByTestId("web-research-warming-toggle"));
    await waitFor(() => {
      const calls = put.mock.calls as Array<
        [{ warming: boolean; guess_domains: boolean }]
      >;
      const last = calls[calls.length - 1]?.[0];
      expect(last?.warming).toBe(false);
      expect(last?.guess_domains).toBe(false);
    });
  });

  it("renders recent activity in a dense paginated read-only list", async () => {
    const pageSize = WEB_RESEARCH_ACTIVITY_PAGE_SIZE;
    const activity = Array.from({ length: pageSize + 2 }, (_, i) => ({
      at: `2026-07-22T${String(12 - Math.floor(i / 60)).padStart(2, "0")}:${
        String(i % 60).padStart(2, "0")
      }:00Z`,
      trigger: `trigger_${i}`,
      tier: "seed",
      topic: `topic_${i}`,
      pages: i,
    }));
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
      getWebResearchIndex: vi.fn().mockResolvedValue({
        available: true,
        docs: 4,
        hosts: 2,
        bytes: 1 << 20,
        verified: 1,
        warmed: 2,
        warm_hits: 0,
        warming: true,
        health: {
          writes_applied: 1,
          writes_dropped: 0,
          queue_high_water: 0,
          last_evict_ms: 0,
          last_evict_rows: 0,
          last_evict_bytes: 0,
          last_search_ms: 1,
          max_search_ms: 2,
        },
        activity,
      }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
    };

    const { getByTestId, queryByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} initialTab="index" />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-activity-list")).toBeTruthy();
    });
    const firstId = `0-2026-07-22T12:00:00Z-trigger_0`;
    const overflowId = `${pageSize}-2026-07-22T12:${
      String(pageSize).padStart(2, "0")
    }:00Z-trigger_${pageSize}`;
    expect(getByTestId(`web-research-activity-list-row-${firstId}`))
      .toBeTruthy();
    expect(queryByTestId(`web-research-activity-list-row-${overflowId}`))
      .toBeNull();
    expect(getByTestId("web-research-activity-list-pager")).toBeTruthy();

    fireEvent.click(getByTestId("web-research-activity-list-pager-next"));
    expect(getByTestId(`web-research-activity-list-row-${overflowId}`))
      .toBeTruthy();
    expect(queryByTestId(`web-research-activity-list-row-${firstId}`))
      .toBeNull();
  });

  it("keeps bundled seed sources on Direct, not as separate rows", async () => {
    const client = {
      getWebResearchProviders: vi.fn().mockResolvedValue(
        sampleStatus({
          enabled_providers: ["direct", "wikipedia", "hn", "brave"],
        }),
      ),
      getWebResearchSettings: vi.fn().mockResolvedValue(
        sampleStatus({
          enabled_providers: ["direct", "wikipedia", "hn", "brave"],
        }),
      ),
      getWebResearchIndex: vi.fn().mockResolvedValue({ available: false }),
      updateWebResearchSettings: vi.fn().mockResolvedValue(sampleStatus()),
    };

    const { getByTestId, queryByTestId } = render(() => (
      <WebResearchSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("web-research-row-direct")).toBeTruthy();
    });
    fireEvent.click(getByTestId("web-research-row-direct"));
    await waitFor(() => {
      expect(getByTestId("web-research-direct-card")).toBeTruthy();
    });
    expect(getByTestId("web-research-direct-card").textContent).toContain(
      WEB_RESEARCH_SETTINGS_COPY.directHint,
    );
    expect(queryByTestId("web-research-row-wikipedia")).toBeNull();
    expect(queryByTestId("web-research-row-hn")).toBeNull();
    expect(queryByTestId("web-research-card-wikipedia")).toBeNull();
    expect(queryByTestId("web-research-card-hn")).toBeNull();
  });
});
