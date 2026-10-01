// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  CONTEXT_NAV_CATALOG,
  type ContextNavItemId,
} from "../../../shared/app-state-types.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import {
  STAGE_REGISTRY,
  availabilityFromProject,
  stageLabelFor,
} from "./stage-registry.tsx";

describe("stage registry", () => {
  it("is the sole label / navTestId / feature SSOT for every ContextNavItemId", () => {
    const expectedLabels: Record<ContextNavItemId, string> = {
      search: "Search",
      files: "Files",
      security: SECURITY_SCANNERS_SECTION_LABEL,
      cost: "Cost",
      artifacts: "Artifacts",
      blueprints: "Blueprints",
      extensions: "Extensions",
    };
    for (const id of CONTEXT_NAV_CATALOG) {
      const def = STAGE_REGISTRY[id];
      expect(def?.id).toBe(id);
      expect(stageLabelFor(id)).toBe(expectedLabels[id]);
      expect(def.label).toBe(expectedLabels[id]);
      expect(def.navTestId).toBe(`project-${id}-entry`);
      expect(typeof def.icon).toBe("function");
      expect(["titlebar", "stage"]).toContain(def.backPlacement);
      expect(typeof def.render).toBe("function");
      expect(typeof def.featureEnabled).toBe("function");
      expect(typeof def.available).toBe("function");
    }
    expect(Object.keys(STAGE_REGISTRY).sort()).toEqual(
      [...CONTEXT_NAV_CATALOG].sort(),
    );
    expect(STAGE_REGISTRY.files.backPlacement).toBe("titlebar");
    expect(
      CONTEXT_NAV_CATALOG.filter(
        (id) => id !== "files" && STAGE_REGISTRY[id].backPlacement !== "stage",
      ),
    ).toEqual([]);
  });

  it("featureEnabled gates security for Context nav", () => {
    expect(
      STAGE_REGISTRY.security.featureEnabled({
        securityScannersEnabled: false,
      }),
    ).toBe(false);
    expect(
      STAGE_REGISTRY.extensions.featureEnabled({
        securityScannersEnabled: true,
      }),
    ).toBe(true);
    expect(
      STAGE_REGISTRY.files.featureEnabled({
        securityScannersEnabled: false,
      }),
    ).toBe(true);
  });

  it("availability requires a project and gates security", () => {
    const base = availabilityFromProject({
      project: {
        roots_generation: 1,
        roots: [
          {
            id: "r",
            path: "/tmp",
            label: "tmp",
            is_primary: true,
            added_at: "2026-01-01T00:00:00Z",
            kind: "attached",
          },
        ],
      },
      securityScannersEnabled: true,
    });
    expect(STAGE_REGISTRY.security.available(base)).toBe(true);
    expect(STAGE_REGISTRY.extensions.available(base)).toBe(true);

    expect(
      STAGE_REGISTRY.security.available({
        ...base,
        securityScannersEnabled: false,
      }),
    ).toBe(false);
    expect(STAGE_REGISTRY.security.available({ ...base })).toBe(true);
  });
});
