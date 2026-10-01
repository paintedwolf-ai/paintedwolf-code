import { describe, expect, it } from "vitest";
import type {
  ExtensionMetaPackSummary,
  ExtensionPackSummary,
} from "../../api/types.ts";
import { EXTENSIONS_SETTINGS_COPY } from "./extensions-settings-copy.ts";
import {
  groupPacksByMeta,
  metaPackStatusChips,
  metaPackIdSafe,
  metaPackStatusChip,
} from "./extensions-meta.ts";

const pack = (
  id: string,
  extras: Partial<ExtensionPackSummary> = {},
): ExtensionPackSummary => ({
  id,
  name: id,
  version: "1.0.0",
  extension_api: "^1.0.0",
  installation_state: "stock",
  dependencies: {},
  kind: "stock",
  enabled: true,
  removable: false,
  unit_count: 1,
  has_profile: false,
  contributing: true,
  meta_pack_ids: [],
  ...extras,
  installation_scope: extras.installation_scope ?? "stock",
});

describe("extensions-meta", () => {
  it("maps meta id to test-id safe form", () => {
    expect(metaPackIdSafe("painted-wolf/stock")).toBe("painted-wolf-stock");
  });

  it("groups stock first and orphans into Other packs", () => {
    const packs = [
      pack("painted-wolf/plan"),
      pack("acme/orphan", { kind: "git", removable: true }),
      pack("painted-wolf/platform"),
    ];
    const metas: ExtensionMetaPackSummary[] = [
      {
        id: "acme/kit",
        name: "Kit",
        version: "1.0.0",
        kind: "path",
        status: "inactive",
        members: [],
        conflicts_with: [],
        extends: [],
        diagnostics: [],
        removable: true,
      },
      {
        id: "painted-wolf/stock",
        name: "Painted Wolf stock",
        version: "1.0.0",
        kind: "stock",
        status: "complete",
        members: ["painted-wolf/platform", "painted-wolf/plan"],
        conflicts_with: [],
        extends: [],
        diagnostics: [],
        removable: false,
      },
    ];
    const g = groupPacksByMeta(packs, metas);
    expect(g.sections.map((s) => s.meta.id)).toEqual([
      "painted-wolf/stock",
      "acme/kit",
    ]);
    expect(g.sections[0]?.packs.map((p) => p.id)).toEqual([
      "painted-wolf/platform",
      "painted-wolf/plan",
    ]);
    expect(g.other.map((p) => p.id)).toEqual(["acme/orphan"]);
  });

  it("maps status and diagnostic codes to chip labels", () => {
    expect(metaPackStatusChip("complete").label).toBe(
      EXTENSIONS_SETTINGS_COPY.statusComplete,
    );
    expect(metaPackStatusChip("partial").className).toBe("conflict");
    expect(metaPackStatusChip("inactive").label).toBe(
      EXTENSIONS_SETTINGS_COPY.statusInactive,
    );

    const chips = metaPackStatusChips({
      id: "acme/kit",
      name: "Kit",
      version: "1.0.0",
      kind: "path",
      status: "partial",
      members: ["a/b"],
      conflicts_with: [],
      extends: [],
      diagnostics: [
        { severity: "error", code: "suite_conflict", message: "x", pack_id: "other" },
        { severity: "warning", code: "member_disabled", message: "y", pack_id: "a/b" },
        { severity: "error", code: "member_missing", message: "z", pack_id: "a/c" },
      ],
      removable: true,
    });
    expect(chips.map((c) => c.label)).toEqual([
      EXTENSIONS_SETTINGS_COPY.chipSuiteConflict,
      EXTENSIONS_SETTINGS_COPY.chipPartialSuite,
      EXTENSIONS_SETTINGS_COPY.chipMissingMember,
    ]);
  });
});
