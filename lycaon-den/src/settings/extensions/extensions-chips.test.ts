import { describe, expect, it } from "vitest";
import {
  packRowChips,
  unitStatusChip,
  unitViaLabel,
} from "./extensions-chips.ts";
import type { ExtensionPackSummary } from "../../api/types.ts";

describe("extensions-chips", () => {
  it("maps unit statuses to Decision 4 labels/classes", () => {
    expect(
      unitStatusChip({
        status: "disabled",
        contributions: [{ pack_id: "painted-wolf/plan" }],
      }),
    ).toEqual({ label: "Disabled", className: "disabled" });

    expect(
      unitStatusChip({
        status: "conflict",
        contributions: [
          { pack_id: "painted-wolf/plan" },
          { pack_id: "acme/extras" },
        ],
      }),
    ).toEqual({ label: "Conflict", className: "conflict" });

    expect(
      unitStatusChip({
        status: "owned",
        contributions: [
          { pack_id: "painted-wolf/plan" },
          { pack_id: "acme/extras" },
        ],
      }),
    ).toEqual({ label: "Selected", className: "selected" });

    expect(
      unitStatusChip({
        status: "loaded",
        contributions: [{ pack_id: "painted-wolf/plan" }],
        packKinds: { "painted-wolf/plan": "stock" },
      }),
    ).toEqual({ label: "Stock", className: "stock" });

    expect(
      unitStatusChip({
        status: "loaded",
        contributions: [{ pack_id: "acme/extras" }],
        packKinds: { "acme/extras": "git" },
      }),
    ).toEqual({ label: "Provided", className: "add" });
  });

  it("builds via labels from status and pack names", () => {
    expect(
      unitViaLabel({
        status: "disabled",
        contributions: [],
      }),
    ).toBe("Disabled here");
    expect(
      unitViaLabel({
        status: "conflict",
        contributions: [{ pack_id: "a" }, { pack_id: "b" }],
      }),
    ).toBe("Needs selection");
    expect(
      unitViaLabel({
        status: "loaded",
        contributions: [{ pack_id: "painted-wolf/plan" }],
        winner_pack_id: "painted-wolf/plan",
        packNames: { "painted-wolf/plan": "Plan" },
      }),
    ).toBe("Plan");
  });

  it("builds pack row chips from wire summary", () => {
    const pack: ExtensionPackSummary = {
      id: "painted-wolf/plan",
      name: "Plan",
      version: "1.0.0",
      extension_api: "^1.0.0",
      installation_state: "stock",
      installation_scope: "stock",
      dependencies: {},
      kind: "stock",
      enabled: true,
      removable: false,
      unit_count: 2,
      has_profile: false,
      contributing: true,
      feature: "plan",
      meta_pack_ids: ["painted-wolf/stock"],
    };
    expect(packRowChips(pack).map((c) => c.label)).toEqual(["Stock"]);

    expect(
      packRowChips({ ...pack, feature: undefined }).map((c) => c.label),
    ).toEqual(["Stock"]);

    expect(
      packRowChips({ ...pack, name: "Recon", feature: "plan" }).map(
        (c) => c.label,
      ),
    ).toEqual(["Stock", "Plan"]);

    const scanners: ExtensionPackSummary = {
      ...pack,
      blocked_reason: "requires_scanners",
      unmet_requires_scanners: ["lycaon-sast"],
    };
    expect(packRowChips(scanners).map((c) => c.label)).toEqual([
      "Stock",
      "Needs scanner",
    ]);

    const invalid: ExtensionPackSummary = {
      ...pack,
      blocked_reason: "invalid",
    };
    expect(packRowChips(invalid).map((c) => c.label)).toEqual([
      "Stock",
      "Has an error",
    ]);

    const integrity: ExtensionPackSummary = {
      ...pack,
      blocked_reason: "integrity",
    };
    expect(packRowChips(integrity).map((c) => c.label)).toEqual([
      "Stock",
      "Won't load",
    ]);

    const dirty: ExtensionPackSummary = {
      ...pack,
      id: "acme/linked",
      kind: "path",
      installation_state: "development",
      installation_scope: "device",
      feature: undefined,
      removable: true,
      needs_reload: true,
    };
    expect(packRowChips(dirty).map((c) => c.label)).toEqual([
      "Development",
      "Disk changed",
    ]);

    expect(
      packRowChips({ ...pack, name: "Browser", feature: "browser" }).map(
        (c) => c.label,
      ),
    ).toEqual(["Stock"]);

    expect(
      packRowChips({
        ...pack,
        id: "acme/linked",
        kind: "path",
        installation_state: "development",
        installation_scope: "device",
        feature: undefined,
        removable: true,
      }).map((c) => c.label),
    ).toEqual(["Development"]);

    expect(
      packRowChips({
        ...pack,
        id: "acme/git",
        kind: "git",
        installation_state: "release",
        installation_scope: "device",
        feature: undefined,
        removable: true,
      }).map((c) => c.label),
    ).toEqual(["v1.0.0"]);
  });

  it("chips a still-contributing pack that has an error- or warning-severity diagnostic", () => {
    const pack: ExtensionPackSummary = {
      id: "foxden/brokenref",
      name: "Broken Reference",
      version: "1.0.0",
      extension_api: "^1.0.0",
      installation_state: "pinned",
      installation_scope: "device",
      dependencies: {},
      kind: "git",
      enabled: true,
      removable: true,
      unit_count: 2,
      has_profile: false,
      contributing: true,
      meta_pack_ids: [],
    };

    expect(
      packRowChips(pack, [
        { code: "skill_name_mismatch", message: "bad", severity: "error", pack_id: pack.id },
      ]).map((c) => ({ label: c.label, className: c.className })),
    ).toEqual([
      { label: "Pinned", className: "add" },
      { label: "Has an error", className: "invalid" },
    ]);

    expect(
      packRowChips(pack, [
        { code: "some_warning", message: "heads up", severity: "warning", pack_id: pack.id },
      ]).map((c) => ({ label: c.label, className: c.className })),
    ).toEqual([
      { label: "Pinned", className: "add" },
      { label: "Has a warning", className: "warning" },
    ]);

    // info-severity diagnostics don't chip the row.
    expect(
      packRowChips(pack, [
        { code: "fyi", message: "noted", severity: "info", pack_id: pack.id },
      ]).map((c) => c.label),
    ).toEqual(["Pinned"]);

    // A pack already blocked_reason="invalid" doesn't get a second, redundant chip.
    expect(
      packRowChips(
        { ...pack, blocked_reason: "invalid" },
        [{ code: "skill_name_mismatch", message: "bad", severity: "error", pack_id: pack.id }],
      ).map((c) => c.label),
    ).toEqual(["Pinned", "Has an error"]);
  });
});
