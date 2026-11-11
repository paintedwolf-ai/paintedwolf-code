import { describe, expect, it } from "vitest";
import { filterProviderKindGroups } from "./provider-form-model.ts";

type Groups = Parameters<typeof filterProviderKindGroups>[0];

const groups = [
  {
    group: "cloud",
    label: "First group",
    templates: [
      { kind: "alpha_kind", label: "Harbor" },
      { kind: "beta_kind", label: "Lantern" },
    ],
  },
  {
    group: "local",
    label: "Second group",
    templates: [{ kind: "gamma_kind", label: "Meadow" }],
  },
] as unknown as Groups;

const shownKinds = (query: string) =>
  filterProviderKindGroups(groups, query).map((group) => [
    group.label,
    group.templates.map((template) => template.kind),
  ]);

describe("provider kind filter", () => {
  it("shows every group for a blank query", () => {
    expect(filterProviderKindGroups(groups, "   ")).toBe(groups);
  });

  it("matches a template's label or kind in any case and ignores surrounding space", () => {
    expect(shownKinds("  hARBor ")).toEqual([["First group", ["alpha_kind"]]]);
    expect(shownKinds("GAMMA_")).toEqual([["Second group", ["gamma_kind"]]]);
  });

  it("keeps every match in its group and drops groups with none", () => {
    expect(shownKinds("_kind")).toEqual([
      ["First group", ["alpha_kind", "beta_kind"]],
      ["Second group", ["gamma_kind"]],
    ]);
    expect(shownKinds("absent")).toEqual([]);
  });
});
