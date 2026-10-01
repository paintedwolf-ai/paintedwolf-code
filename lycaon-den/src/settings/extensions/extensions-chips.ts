import type {
  ExtensionDiagnostic,
  ExtensionPackKind,
  ExtensionPackSummary,
  ExtensionUnitStatus,
} from "../../api/types.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

export type ExtensionChipClass =
  | "stock"
  | "add"
  | "selected"
  | "disabled"
  | "conflict"
  | "profile"
  | "feature"
  | "platform"
  | "security"
  | "scanner"
  | "invalid"
  | "integrity"
  | "reload"
  | "warning";

export type ExtensionChip = {
  label: string;
  className: ExtensionChipClass;
};

export type UnitChipInput = {
  status: ExtensionUnitStatus;
  contributions: readonly { pack_id: string }[];
  packKinds?: Readonly<Record<string, ExtensionPackKind>>;
};

export function unitStatusChip(unit: UnitChipInput): ExtensionChip {
  if (unit.status === "disabled") {
    return { label: "Disabled", className: "disabled" };
  }
  if (unit.status === "conflict") {
    return { label: "Conflict", className: "conflict" };
  }
  if (unit.status === "owned") {
    return { label: "Selected", className: "selected" };
  }
  if (unit.contributions.length === 1) {
    const packId = unit.contributions[0]?.pack_id;
    const kind = packId ? unit.packKinds?.[packId] : undefined;
    if (kind === "stock") {
      return { label: "Stock", className: "stock" };
    }
    return { label: "Provided", className: "add" };
  }
  return { label: "Provided", className: "add" };
}

export function unitViaLabel(
  unit: UnitChipInput & {
    winner_pack_id?: string;
    packNames?: Readonly<Record<string, string>>;
  },
): string {
  if (unit.status === "disabled") return "Disabled here";
  if (unit.status === "conflict") return "Needs selection";
  const id = unit.winner_pack_id || unit.contributions[0]?.pack_id;
  if (!id) return "—";
  return unit.packNames?.[id] ?? id;
}

export function packRowChips(
  pack: ExtensionPackSummary,
  diagnostics: readonly ExtensionDiagnostic[] = [],
): ExtensionChip[] {
  const chips: ExtensionChip[] = [];
  if (pack.kind === "stock") {
    chips.push({ label: "Stock", className: "stock" });
  } else if (pack.installation_state === "development") {
    chips.push({ label: "Development", className: "add" });
  } else if (pack.installation_state === "pinned") {
    chips.push({ label: "Pinned", className: "add" });
  } else if (pack.installation_state === "release") {
    chips.push({ label: `v${pack.version}`, className: "add" });
  }
  const feature = (pack.feature ?? "").trim().toLowerCase();
  if (feature === "platform") {
    chips.push({ label: "Platform", className: "platform" });
  } else if (feature === "security") {
    chips.push({ label: "Security", className: "security" });
  } else if (feature === "core") {
    chips.push({ label: "Core", className: "feature" });
  } else if (feature) {
    const label = formatSentenceCase(feature);
    if (label.toLowerCase() !== pack.name.trim().toLowerCase()) {
      chips.push({ label, className: "feature" });
    }
  }
  if (pack.has_profile) {
    chips.push({ label: "Profile", className: "profile" });
  }
  if (!pack.enabled) {
    chips.push({ label: "Disabled", className: "disabled" });
  }
  if (pack.needs_reload) {
    chips.push({ label: "Disk changed", className: "reload" });
  }
  if (pack.blocked_reason === "invalid") {
    chips.push({ label: "Has an error", className: "invalid" });
  }
  if (pack.blocked_reason === "integrity") {
    chips.push({ label: "Won't load", className: "integrity" });
  }
  if (
    pack.blocked_reason === "requires_scanners" ||
    (pack.unmet_requires_scanners?.length ?? 0) > 0
  ) {
    chips.push({ label: "Needs scanner", className: "scanner" });
  }
  // Pack failures take precedence over unit diagnostics.
  if (!pack.blocked_reason) {
    if (diagnostics.some((d) => d.severity === "error")) {
      chips.push({ label: "Has an error", className: "invalid" });
    } else if (diagnostics.some((d) => d.severity === "warning")) {
      chips.push({ label: "Has a warning", className: "warning" });
    }
  }
  return chips;
}
