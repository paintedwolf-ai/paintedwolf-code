import { describe, expect, it } from "vitest";
import {
  WEB_RESEARCH_CATALOG,
  WEB_RESEARCH_PROVIDER_IDS,
} from "../web-research-catalog.generated.ts";
import {
  DIRECT_PROVIDER_ID,
  catalogProvidersAddable,
  directStatusLabel,
  enabledWebResearchProviders,
  groupAddProviderCandidates,
  groupWebResearchProvidersByKind,
  hasAddProviderCandidates,
  providerStatusLabel,
  resolveInitialEnabledProviderIds,
} from "./web-research-providers-model.ts";
import type {
  WebResearchProviderMeta,
  WebResearchProvidersResponse,
  WebResearchSettings,
} from "../../api/types.ts";

type SampleStatus = WebResearchProvidersResponse & WebResearchSettings;

function sampleStatus(
  overrides: Partial<SampleStatus> = {},
): SampleStatus {
  const providers: WebResearchProviderMeta[] = WEB_RESEARCH_CATALOG.map((entry) => ({
    id: entry.id,
    kind: entry.kind,
    label: entry.label,
    roles: [...entry.roles],
    default_enabled: entry.default_enabled,
    configured: false,
    credential_present: false,
    credential_slot: entry.credential_slot,
  }));
  return {
    warming: true,
    guess_domains: true,
    search_enabled: true,
    direct: {
      configured: false,
      card: {
        provider_id: "direct",
        kind: "direct",
        label: "Direct search",
      },
    },
    providers,
    enabled_providers: ["direct", "brave", "serper"],
    ...overrides,
  };
}

describe("web-research-providers-model", () => {
  it("resolveInitialEnabledProviderIds reads backend prefs including direct", () => {
    const ids = resolveInitialEnabledProviderIds(sampleStatus());
    expect([...ids].sort()).toEqual(["brave", "direct", "serper"]);
  });

  it("resolveInitialEnabledProviderIds drops unknown ids", () => {
    const ids = resolveInitialEnabledProviderIds(
      sampleStatus({ enabled_providers: ["direct", "brave", "nonsense"] }),
    );
    expect([...ids].sort()).toEqual(["brave", "direct"]);
  });

  it("resolveInitialEnabledProviderIds tolerates null prefs arrays", () => {
    const ids = resolveInitialEnabledProviderIds(
      sampleStatus({ enabled_providers: null as unknown as string[] }),
    );
    expect([...ids]).toEqual([]);
  });

  it("resolveInitialEnabledProviderIds strips Direct-bundled keyless ids", () => {
    const ids = resolveInitialEnabledProviderIds(
      sampleStatus({
        enabled_providers: ["direct", "brave", "wikipedia", "hn", "arxiv", "npm"],
      }),
    );
    expect([...ids].sort()).toEqual(["brave", "direct"]);
  });

  it("groups enabled providers excluding Direct-bundled keyless", () => {
    const status = sampleStatus();
    const enabled = new Set(["direct", "brave", "google_cse", "searxng", "arxiv"]);
    const groups = groupWebResearchProvidersByKind(status.providers, enabled);
    expect(groups.map((g) => g.kind)).toEqual([
      "keyed",
      "keyed_extra",
      "keyless_endpoint",
    ]);
    expect(groups.flatMap((g) => g.providers.map((p) => p.id)).sort()).toEqual([
      "brave",
      "google_cse",
      "searxng",
    ]);
  });

  it("grouping never lists keyless providers as separate cards", () => {
    const status = sampleStatus();
    const groups = groupWebResearchProvidersByKind(
      status.providers,
      new Set(["direct", "hn", "wikipedia", "arxiv", "brave"]),
    );
    const ids = groups.flatMap((g) => g.providers.map((p) => p.id));
    expect(ids).toEqual(["brave"]);
    expect(ids).not.toContain("hn");
    expect(ids).not.toContain("wikipedia");
    expect(ids).not.toContain("arxiv");
  });

  it("grouping ignores the direct id — it has no catalog row", () => {
    const status = sampleStatus();
    const groups = groupWebResearchProvidersByKind(
      status.providers,
      new Set([DIRECT_PROVIDER_ID]),
    );
    expect(groups).toEqual([]);
  });

  it("enabledWebResearchProviders filters to enabled ids", () => {
    const enabled = enabledWebResearchProviders(
      sampleStatus().providers,
      new Set(["tavily"]),
    );
    expect(enabled).toHaveLength(1);
    expect(enabled[0]?.id).toBe("tavily");
  });

  it("catalogProvidersAddable omits Direct-bundled keyless but keeps opt-in keyless", () => {
    const addable = catalogProvidersAddable(new Set(["direct"])).map((entry) => entry.id);
    expect(addable).toContain("serper");
    expect(addable).toContain("searxng");
    expect(addable).toContain("mwmbl");
    expect(addable).not.toContain("wikipedia");
    expect(addable).not.toContain("hn");
    expect(addable).not.toContain("arxiv");
    expect(addable).not.toContain("npm");
  });

  it("groupAddProviderCandidates keeps a No-key group for opt-in keyless only", () => {
    const groups = groupAddProviderCandidates(new Set(["direct"]), "Direct search");
    expect(groups.map((g) => g.id)).toEqual(["api_key", "endpoint", "no_key"]);
    expect(groups[0]?.label).toBe("Requires API key");
    expect(groups[0]?.options.map((o) => o.id)).toContain("brave");
    expect(groups[0]?.options.map((o) => o.id)).not.toContain("wikipedia");
    const noKey = groups.find((g) => g.id === "no_key");
    expect(noKey?.options.map((o) => o.id)).toEqual(["mwmbl"]);
  });

  it("groupAddProviderCandidates includes built-in direct when removed", () => {
    const groups = groupAddProviderCandidates(new Set(["brave"]), "Direct search");
    expect(groups[0]?.id).toBe("builtin");
    expect(groups[0]?.options).toEqual([{ id: "direct", label: "Direct search" }]);
  });

  it("hasAddProviderCandidates is true while direct or catalog rows remain", () => {
    expect(hasAddProviderCandidates(new Set(["direct"]))).toBe(true);
    expect(hasAddProviderCandidates(new Set(["direct", "brave", "serper"]))).toBe(true);
    expect(
      hasAddProviderCandidates(
        new Set(["direct", ...WEB_RESEARCH_CATALOG.map((entry) => entry.id)]),
      ),
    ).toBe(false);
  });

  it("providerStatusLabel matches catalog-driven states", () => {
    const ready: WebResearchProviderMeta = {
      id: "brave",
      kind: "keyed",
      label: "Brave Search",
      roles: ["results"],
      configured: true,
      credential_present: true,
    };
    expect(providerStatusLabel(ready)).toBe("Ready");

    const needsKey: WebResearchProviderMeta = {
      id: "serper",
      kind: "keyed",
      label: "Serper",
      roles: ["results"],
      configured: false,
      credential_present: false,
    };
    expect(providerStatusLabel(needsKey)).toBe("Needs API key");

    const keyless: WebResearchProviderMeta = {
      id: "hn",
      kind: "keyless",
      label: "Hacker News",
      roles: ["results", "seeds"],
      configured: true,
      credential_present: false,
    };
    expect(providerStatusLabel(keyless)).toBe("Ready");
  });

  it("directStatusLabel reflects Direct enablement", () => {
    expect(directStatusLabel(true)).toBe("Ready");
    expect(directStatusLabel(false)).toBe("Not enabled");
  });

  it("status provider ids align with generated catalog", () => {
    const status = sampleStatus();
    expect(status.providers).toHaveLength(WEB_RESEARCH_CATALOG.length);
    expect(status.providers.map((p) => p.id)).toEqual(WEB_RESEARCH_PROVIDER_IDS);
    for (const row of status.providers) {
      expect(row.id).not.toBe("direct");
      expect(row.id).not.toBe("docs");
    }
  });
});
