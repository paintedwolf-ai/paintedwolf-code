import { describe, expect, it } from "vitest";
import {
  WEB_RESEARCH_CATALOG,
  WEB_RESEARCH_PROVIDER_IDS,
  catalogByKind,
  catalogEntry,
} from "./web-research-catalog.generated.ts";

describe("web-research-catalog.generated", () => {
  it("lists the bundled custom providers", () => {
    expect(WEB_RESEARCH_CATALOG.length).toBeGreaterThan(0);
    expect(WEB_RESEARCH_PROVIDER_IDS).toHaveLength(WEB_RESEARCH_CATALOG.length);
  });

  it("every entry has label and hint; excludes direct/docs", () => {
    for (const entry of WEB_RESEARCH_CATALOG) {
      expect(entry.label.trim().length).toBeGreaterThan(0);
      expect(entry.hint.trim().length).toBeGreaterThan(0);
      expect(entry.id).not.toBe("direct");
      expect(entry.id).not.toBe("docs");
    }
  });

  it("catalogEntry returns rows by id", () => {
    expect(catalogEntry("brave").label).toBe("Brave Search");
    expect(catalogEntry("serper").kind).toBe("keyed");
  });

  it("catalogByKind groups keyed providers", () => {
    const keyed = catalogByKind("keyed");
    const ids = keyed.map((e) => e.id);
    expect(ids).toContain("brave");
    expect(ids).toContain("serper");
    expect(ids).not.toContain("google_cse");
  });

  it("matches WEB_RESEARCH_PROVIDER_IDS order to catalog ids", () => {
    const fromCatalog = WEB_RESEARCH_CATALOG.map((e) => e.id);
    expect(WEB_RESEARCH_PROVIDER_IDS).toEqual(fromCatalog);
  });
});
